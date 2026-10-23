package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChannelsMustSayWhereTheyWriteWhoConsumesAndWhatHandlesTheMessage(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardChannelDAO(db)
	ctx := context.Background()
	require.NoError(t, db.Create(&model.WorkflowDef{WorkflowKey: "review", Name: "Review", InitialState: "new", PipelineJSON: "{}", Enabled: true}).Error)
	base := model.TelegramForwardChannel{Name: "c", Transport: model.ChannelTransportRedis, Stream: "tg:messages", ConsumerGroup: "kubeforecast", ConsumerKind: model.ChannelConsumerJob, ConsumerRef: "llm_rating", Enabled: true, ConsumerEnabled: true}
	create := func(mut func(c *model.TelegramForwardChannel)) error {
		c := base
		mut(&c)
		_, err := dao.Create(ctx, ChannelInput{TelegramForwardChannel: c})
		return err
	}

	require.NoError(t, create(func(c *model.TelegramForwardChannel) {}))
	invalid := map[string]func(c *model.TelegramForwardChannel){
		"no name":               func(c *model.TelegramForwardChannel) { c.Name = "" },
		"unknown transport":     func(c *model.TelegramForwardChannel) { c.Name, c.Transport = "a", "carrier-pigeon" },
		"redis without stream":  func(c *model.TelegramForwardChannel) { c.Name, c.Stream = "b", "" },
		"redis without group":   func(c *model.TelegramForwardChannel) { c.Name, c.ConsumerGroup = "c2", "" },
		"bad redis address":     func(c *model.TelegramForwardChannel) { c.Name, c.RedisAddr = "d", "not-an-address" },
		"job without a type":    func(c *model.TelegramForwardChannel) { c.Name, c.ConsumerRef = "e", "" },
		"unknown consumer":      func(c *model.TelegramForwardChannel) { c.Name, c.ConsumerKind = "f", "magic" },
		"unknown workflow":      func(c *model.TelegramForwardChannel) { c.Name, c.ConsumerKind, c.ConsumerRef = "g", "workflow", "nope" },
		"kafka without brokers": func(c *model.TelegramForwardChannel) { c.Name, c.Transport, c.Brokers, c.Topic = "h", "kafka", "", "t" },
		"kafka bad broker": func(c *model.TelegramForwardChannel) {
			c.Name, c.Transport, c.Brokers, c.Topic = "i", "kafka", "broker", "t"
		},
		"kafka without topic": func(c *model.TelegramForwardChannel) {
			c.Name, c.Transport, c.Brokers, c.Topic = "j", "kafka", "k:9092", ""
		},
		"direct with outside consumer": func(c *model.TelegramForwardChannel) {
			c.Name, c.Transport, c.ConsumerKind = "k", model.ChannelTransportDirect, model.ChannelConsumerExternal
		},
		"duplicate name": func(c *model.TelegramForwardChannel) {},
	}
	for name, mut := range invalid {
		assert.ErrorAs(t, create(mut), &InvalidInputError{}, name)
	}

	// Valid shapes.
	require.NoError(t, create(func(c *model.TelegramForwardChannel) {
		c.Name, c.Transport, c.Brokers, c.Topic, c.Stream = "kafka tasks", "kafka", "k1:9092, k2:9092", "telegram.task", ""
	}))
	require.NoError(t, create(func(c *model.TelegramForwardChannel) {
		c.Name, c.Transport, c.Stream, c.ConsumerKind, c.ConsumerRef = "outside", "redis_stream", "tg:out", model.ChannelConsumerExternal, ""
	}))
	flow, err := dao.Create(ctx, ChannelInput{TelegramForwardChannel: model.TelegramForwardChannel{
		Name: "flow", Transport: model.ChannelTransportDirect, ConsumerKind: model.ChannelConsumerWorkflow, ConsumerRef: "review", Enabled: true, ConsumerEnabled: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, model.ChannelFormatV1, flow.Format)
}

func TestChannelPasswordsAreWriteOnlyAndEncrypted(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardChannelDAO(db)
	ctx := context.Background()
	ch, err := dao.Create(ctx, ChannelInput{
		TelegramForwardChannel: model.TelegramForwardChannel{Name: "r", Transport: "redis_stream", Stream: "s", ConsumerGroup: "g", ConsumerKind: "external", Enabled: true, ConsumerEnabled: true},
		RedisPassword:          "hunter2",
	})
	require.NoError(t, err)
	assert.True(t, ch.HasRedisPassword)
	assert.NotContains(t, ch.RedisPasswordEnc, "hunter2")
	plain, _, err := dao.Passwords(ch)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", plain)

	// Editing without a password keeps the stored one; clearing removes it.
	edited, err := dao.Update(ctx, ch.ID, ChannelInput{TelegramForwardChannel: model.TelegramForwardChannel{Name: "r2", Transport: "redis_stream", Stream: "s", ConsumerGroup: "g", ConsumerKind: "external", Enabled: true, ConsumerEnabled: true}})
	require.NoError(t, err)
	assert.True(t, edited.HasRedisPassword)
	cleared, err := dao.Update(ctx, ch.ID, ChannelInput{TelegramForwardChannel: model.TelegramForwardChannel{Name: "r2", Transport: "redis_stream", Stream: "s", ConsumerGroup: "g", ConsumerKind: "external", Enabled: true, ConsumerEnabled: true}, ClearSecrets: true})
	require.NoError(t, err)
	assert.False(t, cleared.HasRedisPassword)
}

func TestAChannelInUseCannotBeDeletedAndCountersAreKept(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardChannelDAO(db)
	rules := NewTelegramForwardRuleDAO(db)
	ctx := context.Background()
	id := directChannel(t, db, "jobs")
	require.NoError(t, rules.Create(ctx, &model.TelegramForwardRule{Name: "r", TargetKind: "channel", ChannelID: id, Enabled: true}))

	_, err := dao.Delete(ctx, []int64{id})
	assert.ErrorAs(t, err, &InvalidInputError{})
	dao.RecordProduced(ctx, id)
	dao.RecordProduced(ctx, id)
	dao.RecordConsumed(ctx, id)
	dao.RecordError(ctx, id, "boom")
	got, err := dao.Get(ctx, id)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Produced)
	assert.EqualValues(t, 1, got.Consumed)
	assert.Equal(t, "boom", got.LastError)
	list, total, err := dao.List(ctx, 1, 10, TelegramForwardChannelFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.EqualValues(t, 1, list[0].Rules)

	_, err = rules.Delete(ctx, []int64{1})
	require.NoError(t, err)
	n, err := dao.Delete(ctx, []int64{id})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}
