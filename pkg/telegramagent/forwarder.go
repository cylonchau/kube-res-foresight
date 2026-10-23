package telegramagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/job/tasks/notifyjob"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegramclient"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"gorm.io/gorm"
	"regexp"
	"sync/atomic"
	"time"
)

const (
	// forwardBatch is how many cached messages one pass handles.
	forwardBatch = 100
	// maxDeliveryAttempts is how often a failing delivery is retried before the
	// message is marked failed.
	maxDeliveryAttempts = 5
	// noRuleRecheck is how long a message that no rule applies to waits before
	// the rules are looked at again (a rule may be added later).
	noRuleRecheck = time.Minute
)

// Forwarder is the TG Agent's forward job. It consumes the message cache: every
// pending message is handed to the targets of the rules that apply to it, and
// once all of them have taken it the message is marked consumed (the cleanup
// job deletes it). It holds no forwarding, rating or filtering logic of its own;
// each target is an existing capability (Notify, a Job, a Workflow, Redis or Kafka).
type Forwarder struct {
	messages  *rdb.TelegramClientDAO
	rules     *rdb.TelegramForwardRuleDAO
	jobs      *rdb.JobDAO
	channels  *rdb.TelegramForwardChannelDAO
	engine    *workflow.Engine
	publisher Publisher
	board     *jobBoard
	now       func() time.Time

	wake      chan struct{}
	forwarded atomic.Uint64
	failed    atomic.Uint64
}

func NewForwarder(db *gorm.DB, engine *workflow.Engine, publisher Publisher, board *jobBoard) *Forwarder {
	if board == nil {
		board = newJobBoard()
	}
	return &Forwarder{
		messages:  rdb.NewTelegramClientDAO(db),
		rules:     rdb.NewTelegramForwardRuleDAO(db),
		jobs:      rdb.NewJobDAO(db),
		channels:  rdb.NewTelegramForwardChannelDAO(db),
		engine:    engine,
		publisher: publisher,
		board:     board,
		now:       time.Now,
		wake:      make(chan struct{}, 1),
	}
}

// Handle implements message.Sink: a message was just cached, so look at the
// cache now instead of waiting for the next tick. It never blocks.
func (f *Forwarder) Handle(event message.Event) {
	switch event.Type {
	case message.EventChannelMessage, message.EventPrivateMessage, message.EventSystemMessage:
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}
}

// Forwarded and Failed are the delivery counters since the agent started.
func (f *Forwarder) Forwarded() uint64 { return f.forwarded.Load() }
func (f *Forwarder) Failed() uint64    { return f.failed.Load() }

// Run drives the job until ctx ends.
func (f *Forwarder) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		f.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-f.wake:
		}
	}
}

// Pass handles one batch of due messages and records the job state.
func (f *Forwarder) Pass(ctx context.Context) int {
	rows, err := f.messages.ClaimDueMessages(ctx, f.now(), forwardBatch)
	if err != nil {
		f.board.Record(JobForward, err, "")
		logger.Warn("Telegram forward job could not read the cache", logger.Err(err))
		return 0
	}
	if len(rows) == 0 {
		return 0
	}
	var firstErr error
	consumed := 0
	for _, row := range rows {
		ok, err := f.handle(ctx, row)
		if ok {
			consumed++
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	f.board.Record(JobForward, firstErr, fmt.Sprintf("%d cached, %d consumed", len(rows), consumed))
	return len(rows)
}

// handle delivers one cached message and reports whether it was consumed.
func (f *Forwarder) handle(ctx context.Context, row model.TelegramClientMessage) (bool, error) {
	now := f.now()
	// The cache keeps the text compressed; decode it once for matching and delivery.
	if err := row.LoadText(); err != nil {
		return false, errors.Join(err, f.messages.FailMessage(ctx, row.ID, err.Error()))
	}
	std := telegramclient.StandardMessage(row)
	rules, err := f.rules.ForSource(ctx, row.AccountID, row.SubscriptionID, row.SourceType)
	if err != nil {
		return false, err
	}
	applicable := rules[:0:0]
	for _, rule := range rules {
		if matches(rule.Pattern, row.Text) {
			applicable = append(applicable, rule)
		}
	}
	if len(applicable) == 0 {
		return false, f.messages.DeferMessage(ctx, row.ID, now.Add(noRuleRecheck))
	}

	done := map[int64]bool{}
	var doneIDs []int64
	_ = json.Unmarshal([]byte(row.DoneRules), &doneIDs)
	for _, id := range doneIDs {
		done[id] = true
	}
	var firstErr error
	for _, rule := range applicable {
		if done[rule.ID] {
			continue
		}
		if err := f.deliver(ctx, rule, std); err != nil {
			f.failed.Add(1)
			logger.Warn("Telegram forward rule failed", logger.Int64("rule_id", rule.ID), logger.Err(err))
			if firstErr == nil {
				firstErr = fmt.Errorf("rule %q: %w", rule.Name, err)
			}
			continue
		}
		f.forwarded.Add(1)
		done[rule.ID] = true
	}
	doneIDs = doneIDs[:0]
	for _, rule := range applicable {
		if done[rule.ID] {
			doneIDs = append(doneIDs, rule.ID)
		}
	}
	raw, _ := json.Marshal(doneIDs)
	if firstErr == nil {
		return true, f.messages.MarkMessageConsumed(ctx, row.ID, string(raw), now)
	}
	attempts := row.Attempts + 1
	if err := f.messages.RetryMessage(ctx, row.ID, attempts, maxDeliveryAttempts, now.Add(retryDelay(attempts)), string(raw), firstErr.Error()); err != nil {
		return false, err
	}
	return false, firstErr
}

// retryDelay is the wait before the next delivery attempt: 10s, 20s, 40s ... up to 5 minutes.
func retryDelay(attempts uint8) time.Duration {
	delay := 10 * time.Second << (attempts - 1)
	if delay > 5*time.Minute || delay <= 0 {
		return 5 * time.Minute
	}
	return delay
}

func matches(pattern, text string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile(pattern)
	return err == nil && re.MatchString(text)
}

func idempotencyKey(rule model.TelegramForwardRule, msg message.Message) string {
	return fmt.Sprintf("tgfwd-%d-%s-%s-%s", rule.ID, msg.Account, msg.Metadata["peer_id"], msg.MessageID)
}

// deliverToChannel writes a message to the forward channel a rule names. A direct channel hands it to
// its consumer's module at once; a Redis or Kafka channel writes it to the stream or topic, where the
// channel's consumer picks it up.
func (f *Forwarder) deliverToChannel(ctx context.Context, rule model.TelegramForwardRule, msg message.Message) error {
	channel, err := f.channels.Get(ctx, rule.ChannelID)
	if err != nil {
		return fmt.Errorf("forward channel %d: %w", rule.ChannelID, err)
	}
	if !channel.Enabled {
		return fmt.Errorf("forward channel %q is disabled", channel.Name)
	}
	var writeErr error
	if channel.Transport == model.ChannelTransportDirect {
		writeErr = f.dispatch(ctx, *channel, msg, idempotencyKey(rule, msg))
		if writeErr == nil {
			f.channels.RecordConsumed(ctx, channel.ID)
		}
	} else if f.publisher == nil {
		writeErr = errNoPublisher
	} else if payload, err := json.Marshal(msg); err != nil {
		writeErr = err
	} else {
		writeErr = f.publisher.Publish(ctx, *channel, msg.Source, payload)
	}
	if writeErr != nil {
		f.channels.RecordError(ctx, channel.ID, writeErr.Error())
		return writeErr
	}
	f.channels.RecordProduced(ctx, channel.ID)
	return nil
}

// dispatch is what a channel's consumer does with a message: it hands it to the module the channel
// names. The idempotency key makes a repeat (a retried delivery, a redelivered stream entry) harmless.
func (f *Forwarder) dispatch(ctx context.Context, channel model.TelegramForwardChannel, msg message.Message, key string) error {
	return dispatchMessage(ctx, f.jobs, f.engine, channel, msg, key)
}

func dispatchMessage(ctx context.Context, jobs *rdb.JobDAO, engine *workflow.Engine, channel model.TelegramForwardChannel, msg message.Message, key string) error {
	switch channel.ConsumerKind {
	case model.ChannelConsumerJob:
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return jobs.CreateIdempotent(ctx, &model.Job{
			JobType:        channel.ConsumerRef,
			TargetType:     "telegram_message",
			TargetName:     msg.Source,
			IdempotencyKey: key,
			Payload:        string(payload),
			MaxRetry:       3,
		})
	case model.ChannelConsumerWorkflow:
		if engine == nil {
			return errors.New("no workflow engine")
		}
		vars := map[string]string{
			"text":        msg.Text,
			"source":      msg.Source,
			"source_type": msg.SourceType,
			"sender":      msg.Sender,
			"message_id":  msg.MessageID,
			"platform":    msg.Platform,
		}
		_, err := engine.StartWorkflow(ctx, channel.ConsumerRef, "telegram_message", 0, nil, 0, msg.Sender, vars)
		return err
	}
	return fmt.Errorf("channel %q has no consumer that handles messages (consumer %q)", channel.Name, channel.ConsumerKind)
}

// deliver hands one message to one rule's target.
func (f *Forwarder) deliver(ctx context.Context, rule model.TelegramForwardRule, msg message.Message) error {
	switch rule.TargetKind {
	case model.ForwardTargetNotify:
		payload, err := json.Marshal(notifyjob.Payload{
			Channel:   rule.TargetRef,
			Recipient: rule.TargetRecipient,
			Title:     "Telegram · " + msg.Source,
			Content:   msg.Text,

			SendAccountID: rule.SendAccountID,
		})
		if err != nil {
			return err
		}
		return f.jobs.CreateIdempotent(ctx, &model.Job{
			JobType:        notifyjob.JobType,
			TargetType:     "notify_channel",
			TargetName:     rule.TargetRecipient,
			IdempotencyKey: idempotencyKey(rule, msg),
			Payload:        string(payload),
			MaxRetry:       3,
		})
	case model.ForwardTargetChannel:
		return f.deliverToChannel(ctx, rule, msg)
	}
	return fmt.Errorf("unknown target kind %q", rule.TargetKind)
}
