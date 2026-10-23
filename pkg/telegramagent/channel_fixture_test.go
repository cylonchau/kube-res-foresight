package telegramagent

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

// channelFor finds or makes the forward channel a test rule writes to. A rule is written the short way
// (a kind and a destination: "redis" and a stream, "kafka" and a topic, "job" and a job type, "workflow"
// and a key) and gets the channel that kind implies.
func channelFor(t *testing.T, db *gorm.DB, kind, ref string) int64 {
	t.Helper()
	dao := rdb.NewTelegramForwardChannelDAO(db)
	name := kind + ":" + ref
	var existing model.TelegramForwardChannel
	if err := db.Where("name = ?", name).First(&existing).Error; err == nil {
		return existing.ID
	}
	ch := model.TelegramForwardChannel{Name: name, Enabled: true, ConsumerEnabled: true}
	switch kind {
	case "job":
		ch.Transport, ch.ConsumerKind, ch.ConsumerRef = model.ChannelTransportDirect, model.ChannelConsumerJob, ref
	case "workflow":
		ch.Transport, ch.ConsumerKind, ch.ConsumerRef = model.ChannelTransportDirect, model.ChannelConsumerWorkflow, ref
		// the workflow need not exist for the channel to be saved in these tests
		require.NoError(t, db.Where("workflow_key = ?", ref).FirstOrCreate(&model.WorkflowDef{WorkflowKey: ref, Name: ref, InitialState: "new", PipelineJSON: "{}", Enabled: true}).Error)
	case "redis":
		ch.Transport, ch.Stream, ch.ConsumerGroup, ch.ConsumerKind = model.ChannelTransportRedis, ref, "kubeforecast", model.ChannelConsumerExternal
	case "kafka":
		ch.Transport, ch.Brokers, ch.Topic, ch.ConsumerGroup, ch.ConsumerKind = model.ChannelTransportKafka, "k:9092", ref, "kubeforecast", model.ChannelConsumerExternal
	default:
		t.Fatalf("no channel for kind %q", kind)
	}
	created, err := dao.Create(context.Background(), rdb.ChannelInput{TelegramForwardChannel: ch})
	require.NoError(t, err)
	return created.ID
}

// legacyRule turns the short form of a rule into a channel rule.
func legacyRule(t *testing.T, db *gorm.DB, r model.TelegramForwardRule) model.TelegramForwardRule {
	t.Helper()
	switch r.TargetKind {
	case model.ForwardTargetJob, model.ForwardTargetWorkflow, model.ForwardTargetRedis, model.ForwardTargetKafka:
		r.ChannelID = channelFor(t, db, r.TargetKind, r.TargetRef)
		r.TargetKind, r.TargetRef = model.ForwardTargetChannel, ""
	}
	return r
}
