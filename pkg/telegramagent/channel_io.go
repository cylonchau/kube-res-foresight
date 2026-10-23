package telegramagent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"strings"
	"sync"
	"time"
)

// Publisher writes a message to the place a forward channel names: a Redis Stream or a Kafka
// topic. The key (the message source) keeps one source's messages in order on Kafka.
type Publisher interface {
	Publish(ctx context.Context, channel model.TelegramForwardChannel, key string, payload []byte) error
	Close()
}

// streamMaxLen bounds a Redis Stream: older entries are trimmed once it holds about this many.
const streamMaxLen = 100000

// publishTimeout bounds one delivery.
const publishTimeout = 15 * time.Second

var errNoPublisher = errors.New("no forward channel is available")

// connections opens and keeps the Redis and Kafka clients channels use, one per distinct
// address and credentials, and rebuilds them when those change.
type connections struct {
	channels *rdb.TelegramForwardChannelDAO
	platform func() config.RedisConfig

	mu    sync.Mutex
	redis map[string]*redis.Client
	kafka map[string]*kgo.Client
}

func newConnections(channels *rdb.TelegramForwardChannelDAO, platform func() config.RedisConfig) *connections {
	return &connections{channels: channels, platform: platform, redis: map[string]*redis.Client{}, kafka: map[string]*kgo.Client{}}
}

func (c *connections) redisClient(ch model.TelegramForwardChannel) (*redis.Client, error) {
	password, _, err := c.channels.Passwords(&ch)
	if err != nil {
		return nil, fmt.Errorf("decrypt the Redis password of channel %q: %w", ch.Name, err)
	}
	addr, db := ch.RedisAddr, ch.RedisDB
	if addr == "" { // the platform Redis
		platform := config.RedisConfig{}
		if c.platform != nil {
			platform = c.platform()
		}
		addr, password, db = platform.Addr, platform.Password, platform.DB
	}
	if addr == "" {
		return nil, fmt.Errorf("channel %q has no Redis address and the platform Redis is not configured", ch.Name)
	}
	key := fmt.Sprintf("%s|%s|%d", addr, password, db)
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.redis[key]; ok {
		return client, nil
	}
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	c.redis[key] = client
	return client, nil
}

// kafkaOpts are the connection options of a Kafka channel; extra options (a consumer group, the
// topic to read) are added by the caller.
func (c *connections) kafkaOpts(ch model.TelegramForwardChannel) ([]kgo.Opt, string, error) {
	_, password, err := c.channels.Passwords(&ch)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt the Kafka password of channel %q: %w", ch.Name, err)
	}
	brokers := splitBrokers(ch.Brokers)
	if len(brokers) == 0 {
		return nil, "", fmt.Errorf("channel %q has no Kafka brokers", ch.Name)
	}
	opts := []kgo.Opt{kgo.SeedBrokers(brokers...)}
	if ch.KafkaUsername != "" {
		opts = append(opts, kgo.SASL(plain.Auth{User: ch.KafkaUsername, Pass: password}.AsMechanism()))
	}
	if ch.KafkaTLS {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	return opts, fmt.Sprintf("%s|%s|%s|%t", strings.Join(brokers, ","), ch.KafkaUsername, password, ch.KafkaTLS), nil
}

func (c *connections) kafkaProducer(ch model.TelegramForwardChannel) (*kgo.Client, error) {
	opts, key, err := c.kafkaOpts(ch)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.kafka[key]; ok {
		return client, nil
	}
	client, err := kgo.NewClient(append(opts, kgo.RequiredAcks(kgo.AllISRAcks()))...)
	if err != nil {
		return nil, err
	}
	c.kafka[key] = client
	return client, nil
}

func splitBrokers(raw string) []string {
	var out []string
	for _, broker := range strings.Split(raw, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			out = append(out, broker)
		}
	}
	return out
}

func (c *connections) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, client := range c.redis {
		_ = client.Close()
		delete(c.redis, key)
	}
	for key, client := range c.kafka {
		client.Close()
		delete(c.kafka, key)
	}
}

// ChannelPublisher publishes through go-redis and franz-go.
type ChannelPublisher struct{ conns *connections }

func NewChannelPublisher(channels *rdb.TelegramForwardChannelDAO, platform func() config.RedisConfig) *ChannelPublisher {
	return &ChannelPublisher{conns: newConnections(channels, platform)}
}

func (p *ChannelPublisher) Publish(ctx context.Context, ch model.TelegramForwardChannel, key string, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	switch ch.Transport {
	case model.ChannelTransportRedis:
		client, err := p.conns.redisClient(ch)
		if err != nil {
			return err
		}
		return client.XAdd(ctx, &redis.XAddArgs{
			Stream: ch.Stream, MaxLen: streamMaxLen, Approx: true,
			Values: map[string]any{"format": ch.Format, "source": key, "payload": payload},
		}).Err()
	case model.ChannelTransportKafka:
		client, err := p.conns.kafkaProducer(ch)
		if err != nil {
			return err
		}
		return client.ProduceSync(ctx, &kgo.Record{Topic: ch.Topic, Key: []byte(key), Value: payload}).FirstErr()
	}
	return fmt.Errorf("channel %q has transport %q, which is not written through a broker", ch.Name, ch.Transport)
}

// Close releases the open connections.
func (p *ChannelPublisher) Close() { p.conns.close() }

// CheckChannel tries the broker of a channel and reports why it cannot be used. A direct channel has no
// broker to try.
func CheckChannel(ctx context.Context, channels *rdb.TelegramForwardChannelDAO, platform func() config.RedisConfig, ch model.TelegramForwardChannel) error {
	conns := newConnections(channels, platform)
	defer conns.close()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	switch ch.Transport {
	case model.ChannelTransportRedis:
		client, err := conns.redisClient(ch)
		if err != nil {
			return err
		}
		return client.Ping(ctx).Err()
	case model.ChannelTransportKafka:
		opts, _, err := conns.kafkaOpts(ch)
		if err != nil {
			return err
		}
		client, err := kgo.NewClient(opts...)
		if err != nil {
			return err
		}
		defer client.Close()
		return client.Ping(ctx)
	}
	return nil
}
