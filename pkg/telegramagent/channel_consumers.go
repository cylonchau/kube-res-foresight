package telegramagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"sync"
	"time"
)

// ChannelConsumers runs the built-in consumer of every Redis and Kafka forward channel: it reads the
// stream or topic with the channel's consumer group and hands each message to the module the channel
// names (the Job module or the Workflow engine). A message is acknowledged only after the module has
// taken it, and is retried until it does, so a stop or a failing module loses nothing. Channels whose
// consumer is external are only written to; whatever reads them is outside this process.
type ChannelConsumers struct {
	channels *rdb.TelegramForwardChannelDAO
	jobs     *rdb.JobDAO
	engine   *workflow.Engine
	conns    *connections
	board    *jobBoard
	agentID  string
	every    time.Duration

	mu      sync.Mutex
	running map[int64]*runningConsumer
}

type runningConsumer struct {
	fingerprint string
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewChannelConsumers(channels *rdb.TelegramForwardChannelDAO, jobs *rdb.JobDAO, engine *workflow.Engine, platform func() config.RedisConfig, board *jobBoard, agentID string) *ChannelConsumers {
	if board == nil {
		board = newJobBoard()
	}
	return &ChannelConsumers{
		channels: channels, jobs: jobs, engine: engine, conns: newConnections(channels, platform),
		board: board, agentID: agentID, every: 10 * time.Second, running: map[int64]*runningConsumer{},
	}
}

// consumes says whether the agent runs a consumer for the channel.
func consumes(ch model.TelegramForwardChannel) bool {
	return ch.Enabled && ch.ConsumerEnabled && ch.Transport != model.ChannelTransportDirect && ch.ConsumerKind != model.ChannelConsumerExternal
}

func fingerprintOf(ch model.TelegramForwardChannel) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s|%s|%s|%s|%s|%s|%t|%s", ch.Transport, ch.RedisAddr, ch.Stream, ch.RedisDB, ch.Brokers, ch.Topic,
		ch.KafkaUsername, ch.ConsumerGroup, ch.ConsumerKind, ch.ConsumerRef, ch.RedisPasswordEnc+ch.KafkaPasswordEnc, ch.KafkaTLS, ch.UpdatedAt.Format(time.RFC3339Nano))
}

// Run keeps the consumers in line with the channels until ctx ends.
func (c *ChannelConsumers) Run(ctx context.Context) {
	ticker := time.NewTicker(c.every)
	defer ticker.Stop()
	for {
		c.Reconcile(ctx)
		select {
		case <-ctx.Done():
			c.stopAll()
			c.conns.close()
			return
		case <-ticker.C:
		}
	}
}

// Reconcile starts the consumers that are wanted and stops the ones that are not or whose channel changed.
func (c *ChannelConsumers) Reconcile(ctx context.Context) {
	channels, err := c.channels.ListActive(ctx)
	if err != nil {
		c.board.Record(JobConsumers, err, "")
		return
	}
	wanted := map[int64]model.TelegramForwardChannel{}
	for _, ch := range channels {
		if consumes(ch) {
			wanted[ch.ID] = ch
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, run := range c.running {
		ch, keep := wanted[id]
		select {
		case <-run.done:
			delete(c.running, id) // ended on its own: rebuilt below
			continue
		default:
		}
		if !keep || run.fingerprint != fingerprintOf(ch) {
			run.cancel()
			<-run.done
			delete(c.running, id)
		}
	}
	for id, ch := range wanted {
		if _, ok := c.running[id]; ok {
			continue
		}
		runCtx, cancel := context.WithCancel(ctx)
		run := &runningConsumer{fingerprint: fingerprintOf(ch), cancel: cancel, done: make(chan struct{})}
		c.running[id] = run
		go func(ch model.TelegramForwardChannel) {
			defer close(run.done)
			var err error
			switch ch.Transport {
			case model.ChannelTransportRedis:
				err = c.consumeRedis(runCtx, ch)
			case model.ChannelTransportKafka:
				err = c.consumeKafka(runCtx, ch)
			}
			if err != nil && runCtx.Err() == nil {
				c.channels.RecordError(context.Background(), ch.ID, "consumer stopped: "+err.Error())
				logger.Warn("Telegram channel consumer stopped", logger.String("channel", ch.Name), logger.Err(err))
			}
		}(ch)
	}
	c.board.Record(JobConsumers, nil, fmt.Sprintf("%d consumers", len(c.running)))
}

func (c *ChannelConsumers) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, run := range c.running {
		run.cancel()
		<-run.done
		delete(c.running, id)
	}
}

// Count is how many consumers are running.
func (c *ChannelConsumers) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.running)
}

// take hands one message to the channel's module. It retries with a growing pause until the module has
// taken it or ctx ends; a body that cannot be decoded is reported and dropped, since retrying never helps.
func (c *ChannelConsumers) take(ctx context.Context, ch model.TelegramForwardChannel, payload []byte) error {
	var msg message.Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		c.channels.RecordError(ctx, ch.ID, "unreadable message dropped: "+err.Error())
		return nil
	}
	key := fmt.Sprintf("tgch-%d-%s-%s-%s", ch.ID, msg.Account, msg.Metadata["peer_id"], msg.MessageID)
	delay := time.Second
	for {
		err := dispatchMessage(ctx, c.jobs, c.engine, ch, msg, key)
		if err == nil {
			c.channels.RecordConsumed(ctx, ch.ID)
			return nil
		}
		c.channels.RecordError(ctx, ch.ID, err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func (c *ChannelConsumers) consumeRedis(ctx context.Context, ch model.TelegramForwardChannel) error {
	client, err := c.conns.redisClient(ch)
	if err != nil {
		return err
	}
	// The group starts at the beginning of the stream, so entries written before it existed are not skipped.
	if err := client.XGroupCreateMkStream(ctx, ch.Stream, ch.ConsumerGroup, "0").Err(); err != nil && !isBusyGroup(err) {
		return fmt.Errorf("create consumer group: %w", err)
	}
	consumer := c.agentID
	process := func(entries []redis.XMessage) error {
		for _, entry := range entries {
			raw, _ := entry.Values["payload"].(string)
			if err := c.take(ctx, ch, []byte(raw)); err != nil {
				return err
			}
			if err := client.XAck(ctx, ch.Stream, ch.ConsumerGroup, entry.ID).Err(); err != nil {
				return err
			}
		}
		return nil
	}
	for ctx.Err() == nil {
		// Entries another consumer (or an earlier run) read but never acknowledged are taken over.
		if stuck, _, err := client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: ch.Stream, Group: ch.ConsumerGroup, Consumer: consumer, MinIdle: 30 * time.Second, Start: "0", Count: 10,
		}).Result(); err == nil {
			if err := process(stuck); err != nil {
				return err
			}
		}
		res, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: ch.ConsumerGroup, Consumer: consumer, Streams: []string{ch.Stream, ">"}, Count: 10, Block: 2 * time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.channels.RecordError(ctx, ch.ID, "read stream: "+err.Error())
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, stream := range res {
			if err := process(stream.Messages); err != nil {
				return nil
			}
		}
	}
	return nil
}

func isBusyGroup(err error) bool {
	return err != nil && len(err.Error()) >= 9 && err.Error()[:9] == "BUSYGROUP"
}

func (c *ChannelConsumers) consumeKafka(ctx context.Context, ch model.TelegramForwardChannel) error {
	opts, _, err := c.conns.kafkaOpts(ch)
	if err != nil {
		return err
	}
	client, err := kgo.NewClient(append(opts,
		kgo.ConsumerGroup(ch.ConsumerGroup), kgo.ConsumeTopics(ch.Topic),
		kgo.DisableAutoCommit(), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))...)
	if err != nil {
		return err
	}
	defer client.Close()
	for ctx.Err() == nil {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(topic string, _ int32, err error) {
			c.channels.RecordError(ctx, ch.ID, fmt.Sprintf("read topic %s: %v", topic, err))
		})
		var failed error
		fetches.EachRecord(func(record *kgo.Record) {
			if failed != nil {
				return
			}
			if err := c.take(ctx, ch, record.Value); err != nil {
				failed = err
				return
			}
			// Committed only after the module took the message.
			if err := client.CommitRecords(ctx, record); err != nil {
				failed = err
			}
		})
		if failed != nil {
			return nil
		}
	}
	return nil
}
