package telegramagent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAMessageNoRuleAppliesToStaysCachedAndIsCheckedAgainLater(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	row := fx.cache(1, 0, model.TelegramSourcePrivate, "hello")

	require.Equal(t, 1, fx.f.Pass(ctx))
	got := fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgPending, got.Status, "nothing consumed it, so it waits in the cache")
	require.NotNil(t, got.NextAttemptAt)
	assert.Zero(t, fx.f.Pass(ctx), "and is not looked at again right away")

	// A rule added later picks it up once the recheck time has passed.
	fx.rule(model.TelegramForwardRule{Name: "all", TargetKind: model.ForwardTargetRedis, TargetRef: "q"})
	fx.at = fx.at.Add(noRuleRecheck + time.Second)
	require.Equal(t, 1, fx.f.Pass(ctx))
	assert.Equal(t, model.TelegramMsgConsumed, fx.reload(row.ID).Status)
}

func TestAFailingDeliveryIsRetriedWithBackoffAndNeverRepeatsWhatSucceeded(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "job", TargetKind: model.ForwardTargetJob, TargetRef: "llm_rating"})
	fx.rule(model.TelegramForwardRule{Name: "queue", TargetKind: model.ForwardTargetRedis, TargetRef: "q"})
	row := fx.cache(1, 5, model.TelegramSourceChannel, "hello")
	fx.pub.err = errors.New("redis is down")

	require.Equal(t, 1, fx.f.Pass(ctx))
	got := fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgPending, got.Status)
	assert.EqualValues(t, 1, got.Attempts)
	assert.Contains(t, got.LastError, "redis is down")
	assert.Contains(t, got.LastError, `rule "queue"`)
	assert.Equal(t, 1, countJobs(t, fx.db, "llm_rating"), "the rule that worked ran once")
	require.NotNil(t, got.NextAttemptAt)
	assert.WithinDuration(t, fx.at.Add(10*time.Second), *got.NextAttemptAt, time.Second)
	assert.EqualValues(t, 1, fx.f.Failed())
	assert.Equal(t, "rule \"queue\": redis is down", fx.f.board.Snapshot()[0].LastError)

	assert.Zero(t, fx.f.Pass(ctx), "not due yet")

	// Redis comes back: only the failed target is tried again, then the message is consumed.
	fx.pub.err = nil
	fx.at = fx.at.Add(11 * time.Second)
	require.Equal(t, 1, fx.f.Pass(ctx))
	got = fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgConsumed, got.Status)
	assert.Len(t, fx.pub.all(), 1)
	assert.Equal(t, 1, countJobs(t, fx.db, "llm_rating"))
	var done []int64
	require.NoError(t, json.Unmarshal([]byte(got.DoneRules), &done))
	assert.Len(t, done, 2)
	assert.Empty(t, fx.f.board.Snapshot()[0].LastError, "a clean pass clears the job error")
}

func TestAMessageThatKeepsFailingIsMarkedFailed(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "queue", TargetKind: model.ForwardTargetKafka, TargetRef: "t"})
	row := fx.cache(1, 5, model.TelegramSourceChannel, "hello")
	fx.pub.err = errors.New("no brokers")

	for i := 0; i < maxDeliveryAttempts; i++ {
		require.Equal(t, 1, fx.f.Pass(ctx), "attempt %d", i+1)
		fx.at = fx.at.Add(10 * time.Minute)
	}
	got := fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgFailed, got.Status)
	assert.EqualValues(t, maxDeliveryAttempts, got.Attempts)
	assert.Nil(t, got.NextAttemptAt)
	assert.Zero(t, fx.f.Pass(ctx), "a failed message is not retried again")
}

func TestRetryDelayDoublesUpToAFiveMinuteCap(t *testing.T) {
	assert.Equal(t, 10*time.Second, retryDelay(1))
	assert.Equal(t, 20*time.Second, retryDelay(2))
	assert.Equal(t, 40*time.Second, retryDelay(3))
	assert.Equal(t, 5*time.Minute, retryDelay(8))
	assert.Equal(t, 5*time.Minute, retryDelay(250))
}

func TestTargetsReportWhatIsMissing(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	msg := message.Message{Platform: "telegram", Account: "1", Source: "s", MessageID: "1", Text: "x", Metadata: map[string]string{"peer_id": "1"}}

	channelRule := func(kind, ref string) model.TelegramForwardRule {
		return legacyRule(t, fx.db, model.TelegramForwardRule{TargetKind: kind, TargetRef: ref})
	}
	f := NewForwarder(fx.db, nil, nil, nil)
	assert.ErrorIs(t, f.deliver(ctx, channelRule(model.ForwardTargetRedis, "q"), msg), errNoPublisher)
	assert.ErrorContains(t, f.deliver(ctx, channelRule(model.ForwardTargetWorkflow, "review"), msg), "no workflow engine")
	assert.ErrorContains(t, fx.f.deliver(ctx, model.TelegramForwardRule{TargetKind: model.ForwardTargetChannel, ChannelID: 999}, msg), "forward channel 999")
	assert.ErrorContains(t, fx.f.deliver(ctx, model.TelegramForwardRule{TargetKind: "pigeon"}, msg), "unknown target kind")

	disabled := channelRule(model.ForwardTargetJob, "llm")
	require.NoError(t, fx.db.Model(&model.TelegramForwardChannel{}).Where("id = ?", disabled.ChannelID).UpdateColumn("enabled", false).Error)
	assert.ErrorContains(t, fx.f.deliver(ctx, disabled, msg), "is disabled")
}

func TestOnlyMessageEventsWakeTheForwardJob(t *testing.T) {
	fx := newForwardFixture(t)
	fx.f.Handle(message.Event{Type: message.EventCommand})
	select {
	case <-fx.f.wake:
		t.Fatal("a bot command must not wake the forward job")
	default:
	}
	for _, kind := range []string{message.EventChannelMessage, message.EventPrivateMessage, message.EventSystemMessage} {
		fx.f.Handle(message.Event{Type: kind})
		fx.f.Handle(message.Event{Type: kind}) // a second nudge never blocks
		select {
		case <-fx.f.wake:
		default:
			t.Fatalf("%s did not wake the job", kind)
		}
	}
}

func TestTheForwardJobRunsOnTicksAndWakeUps(t *testing.T) {
	fx := newForwardFixture(t)
	fx.f.now = time.Now
	fx.rule(model.TelegramForwardRule{Name: "queue", TargetKind: model.ForwardTargetRedis, TargetRef: "q"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fx.f.Run(ctx, time.Hour); close(done) }()

	row := fx.cache(1, 5, model.TelegramSourceChannel, "hi")
	fx.f.Handle(message.Event{Type: message.EventChannelMessage})
	require.Eventually(t, func() bool { return fx.reload(row.ID).Status == model.TelegramMsgConsumed }, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

func TestPatternMatching(t *testing.T) {
	assert.True(t, matches("", "anything"))
	assert.True(t, matches("^BTC", "BTC up"))
	assert.False(t, matches("^BTC", "ETH up"))
	assert.False(t, matches("(", "anything"), "a broken pattern matches nothing")
}

func TestAMessageWhoseBodyCannotBeReadIsFailedNotRetriedForever(t *testing.T) {
	fx := newForwardFixture(t)
	ctx := context.Background()
	fx.rule(model.TelegramForwardRule{Name: "queue", TargetKind: model.ForwardTargetRedis, TargetRef: "q"})
	row := fx.cache(1, 5, model.TelegramSourceChannel, "hello")
	require.NoError(t, fx.db.Model(&model.TelegramClientMessage{}).Where("id = ?", row.ID).Update("body", []byte{9, 1, 2}).Error)

	require.Equal(t, 1, fx.f.Pass(ctx))
	got := fx.reload(row.ID)
	assert.Equal(t, model.TelegramMsgFailed, got.Status)
	assert.Contains(t, got.LastError, "unreadable")
	assert.Empty(t, fx.pub.all())
	assert.Zero(t, fx.f.Pass(ctx))
}
