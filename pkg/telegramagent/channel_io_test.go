package telegramagent

import (
	"context"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"gorm.io/gorm"
	"testing"
	"time"
)

// serialize keeps every query of the test on one connection: an in-memory SQLite database refuses
// concurrent readers and writers, and a consumer plus a polling test are exactly that.
func serialize(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
}

func newChannel(t *testing.T, db *gorm.DB, ch model.TelegramForwardChannel) model.TelegramForwardChannel {
	t.Helper()
	ch.Enabled, ch.ConsumerEnabled = true, true
	created, err := rdb.NewTelegramForwardChannelDAO(db).Create(context.Background(), rdb.ChannelInput{TelegramForwardChannel: ch})
	require.NoError(t, err)
	return *created
}

func sampleMessage(id string) message.Message {
	return message.Message{Platform: "telegram", Account: "1", Source: "newsfeed", SourceType: "channel", MessageID: id, Text: "BTC breaks out " + id, Metadata: map[string]string{"peer_id": "77"}}
}

func TestMessagesAreAddedToTheRedisStreamOfTheChannel(t *testing.T) {
	db := testDB(t)
	server := miniredis.RunT(t)
	ch := newChannel(t, db, model.TelegramForwardChannel{Name: "stream", Transport: model.ChannelTransportRedis, RedisAddr: server.Addr(), Stream: "tg:messages", ConsumerGroup: "kubeforecast", ConsumerKind: model.ChannelConsumerExternal})
	publisher := NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), nil)
	t.Cleanup(publisher.Close)

	require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", []byte(`{"text":"one"}`)))
	entries, err := server.Stream("tg:messages")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Values, "payload")
	assert.Contains(t, entries[0].Values, "newsfeed")

	// A channel without an address of its own writes to the platform Redis.
	platform := miniredis.RunT(t)
	own := newChannel(t, db, model.TelegramForwardChannel{Name: "platform", Transport: model.ChannelTransportRedis, Stream: "tg:p", ConsumerGroup: "g", ConsumerKind: model.ChannelConsumerExternal})
	viaPlatform := NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), func() config.RedisConfig { return config.RedisConfig{Addr: platform.Addr()} })
	t.Cleanup(viaPlatform.Close)
	require.NoError(t, viaPlatform.Publish(context.Background(), own, "k", []byte("x")))
	got, err := platform.Stream("tg:p")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// With neither, it says what is missing instead of hanging.
	assert.ErrorContains(t, publisher.Publish(context.Background(), own, "k", []byte("x")), "platform Redis is not configured")
}

func TestMessagesAreProducedToTheKafkaTopicOfTheChannel(t *testing.T) {
	db := testDB(t)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "telegram.task"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	ch := newChannel(t, db, model.TelegramForwardChannel{Name: "kafka", Transport: model.ChannelTransportKafka, Brokers: cluster.ListenAddrs()[0], Topic: "telegram.task", ConsumerGroup: "kubeforecast", ConsumerKind: model.ChannelConsumerExternal})
	publisher := NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), nil)
	t.Cleanup(publisher.Close)

	require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", []byte(`{"text":"hello"}`)))
	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics("telegram.task"), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	require.NoError(t, err)
	t.Cleanup(consumer.Close)
	fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	records := consumer.PollFetches(fetchCtx).Records()
	require.Len(t, records, 1)
	assert.Equal(t, "newsfeed", string(records[0].Key))

	assert.NoError(t, CheckChannel(context.Background(), rdb.NewTelegramForwardChannelDAO(db), nil, ch), "a reachable broker passes the check")
	dead := ch
	dead.Brokers = "127.0.0.1:1"
	assert.Error(t, CheckChannel(context.Background(), rdb.NewTelegramForwardChannelDAO(db), nil, dead))
}

func TestARedisChannelConsumerHandsEveryEntryToTheJobModuleOnce(t *testing.T) {
	db := testDB(t)
	serialize(t, db)
	server := miniredis.RunT(t)
	ch := newChannel(t, db, model.TelegramForwardChannel{Name: "tasks", Transport: model.ChannelTransportRedis, RedisAddr: server.Addr(), Stream: "tg:task", ConsumerGroup: "task-consumer", ConsumerKind: model.ChannelConsumerJob, ConsumerRef: "llm_rating"})
	publisher := NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), nil)
	t.Cleanup(publisher.Close)
	// Written before the consumer ever ran: the group starts at the beginning of the stream.
	for _, id := range []string{"1", "2"} {
		payload, _ := json.Marshal(sampleMessage(id))
		require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", payload))
	}

	consumers := NewChannelConsumers(rdb.NewTelegramForwardChannelDAO(db), rdb.NewJobDAO(db), nil, nil, nil, "agent-test")
	consumers.every = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumers.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return countJobs(t, db, "llm_rating") == 2 }, 10*time.Second, 50*time.Millisecond)

	payload, _ := json.Marshal(sampleMessage("2")) // the same message again (a redelivery)
	require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", payload))
	payload, _ = json.Marshal(sampleMessage("3"))
	require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", payload))
	require.Eventually(t, func() bool { return countJobs(t, db, "llm_rating") == 3 }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, 3, countJobs(t, db, "llm_rating"), "a repeated message makes no second job")

	require.Eventually(t, func() bool {
		got, err := rdb.NewTelegramForwardChannelDAO(db).Get(context.Background(), ch.ID)
		return err == nil && got.Consumed == 4
	}, 10*time.Second, 50*time.Millisecond)
	check := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = check.Close() })
	pending, err := check.XPending(context.Background(), "tg:task", "task-consumer").Result()
	require.NoError(t, err)
	assert.Zero(t, pending.Count, "everything taken was acknowledged")
	cancel()
	<-done
}

func TestAKafkaChannelConsumerHandsEveryRecordToTheJobModule(t *testing.T) {
	db := testDB(t)
	serialize(t, db)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "telegram.task"))
	require.NoError(t, err)
	t.Cleanup(cluster.Close)
	ch := newChannel(t, db, model.TelegramForwardChannel{Name: "kafka tasks", Transport: model.ChannelTransportKafka, Brokers: cluster.ListenAddrs()[0], Topic: "telegram.task", ConsumerGroup: "task-consumer", ConsumerKind: model.ChannelConsumerJob, ConsumerRef: "llm_rating"})
	publisher := NewChannelPublisher(rdb.NewTelegramForwardChannelDAO(db), nil)
	t.Cleanup(publisher.Close)
	for _, id := range []string{"1", "2"} {
		payload, _ := json.Marshal(sampleMessage(id))
		require.NoError(t, publisher.Publish(context.Background(), ch, "newsfeed", payload))
	}

	consumers := NewChannelConsumers(rdb.NewTelegramForwardChannelDAO(db), rdb.NewJobDAO(db), nil, nil, nil, "agent-test")
	consumers.every = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumers.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return countJobs(t, db, "llm_rating") == 2 }, 20*time.Second, 100*time.Millisecond)
	cancel()
	<-done
}

func TestOnlyChannelsWithABuiltInConsumerAreRead(t *testing.T) {
	base := model.TelegramForwardChannel{Enabled: true, ConsumerEnabled: true, Transport: model.ChannelTransportRedis, ConsumerKind: model.ChannelConsumerJob}
	assert.True(t, consumes(base))
	direct := base
	direct.Transport = model.ChannelTransportDirect
	assert.False(t, consumes(direct), "a direct channel is consumed by the module itself")
	external := base
	external.ConsumerKind = model.ChannelConsumerExternal
	assert.False(t, consumes(external), "an outside consumer reads it")
	off := base
	off.ConsumerEnabled = false
	assert.False(t, consumes(off))
	disabled := base
	disabled.Enabled = false
	assert.False(t, consumes(disabled))
}
