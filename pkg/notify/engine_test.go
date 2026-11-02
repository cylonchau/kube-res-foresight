package notify

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

type testNotifier struct {
	channel   string
	sentMsgs  []Message
	recipient string
}

func (t *testNotifier) Channel() string                                { return t.channel }
func (t *testNotifier) Recipients(_ context.Context) ([]string, error) { return []string{}, nil }
func (t *testNotifier) Send(_ context.Context, r string, m Message) error {
	t.recipient = r
	t.sentMsgs = append(t.sentMsgs, m)
	return nil
}

func setupEngineTest(t *testing.T) (*Engine, *rdb.NotificationDAO) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NotificationDelivery{}))
	dao := rdb.NewNotificationDAO(db)
	engine := NewEngine(dao)
	return engine, dao
}

func TestEngineDispatchAndDedup(t *testing.T) {
	engine, dao := setupEngineTest(t)
	ctx := context.Background()

	mockChannel := &testNotifier{channel: "mock_tg"}
	Register(mockChannel)

	req := DeliveryRequest{
		NotificationID: "notif_001",
		DedupKey:       "dedup_flash_crash",
		DedupWindow:    5 * time.Minute,
		Channel:        "mock_tg",
		Recipient:      "12345",
		Title:          "Crash Alert",
		Content:        "BTC -5%",
		Severity:       "P1",
	}

	// 1. Initial Dispatch: should succeed
	res, err := engine.Dispatch(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, StatusSent, res.Status)
	assert.Len(t, mockChannel.sentMsgs, 1)

	// Check DB record
	dbRecord, err := dao.GetByNotificationID(ctx, "notif_001")
	require.NoError(t, err)
	assert.Equal(t, "SENT", dbRecord.Status)

	// 2. Duplicate Dispatch with same dedup key: should be suppressed
	dupReq := req
	dupReq.NotificationID = "notif_002"
	resDup, err := engine.Dispatch(ctx, dupReq)
	require.NoError(t, err)
	assert.Equal(t, StatusDeduped, resDup.Status)
	assert.Len(t, mockChannel.sentMsgs, 1) // not sent again
}

func TestEngineCooldown(t *testing.T) {
	engine, _ := setupEngineTest(t)
	ctx := context.Background()

	mockChan := &testNotifier{channel: "mock_cd"}
	Register(mockChan)

	req := DeliveryRequest{
		Channel:   "mock_cd",
		Recipient: "user1",
		Cooldown:  10 * time.Minute,
		Title:     "Price Alert",
		Content:   "BTC up 1%",
	}

	// First: sends
	res1, err := engine.Dispatch(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, StatusSent, res1.Status)

	// Second immediately: suppressed by cooldown
	res2, err := engine.Dispatch(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, StatusCooldownSuppressed, res2.Status)
}

func TestEngineAckAndEscalation(t *testing.T) {
	engine, dao := setupEngineTest(t)
	ctx := context.Background()

	primaryChan := &testNotifier{channel: "chan_primary"}
	voiceChan := &testNotifier{channel: "chan_voice"}
	Register(primaryChan)
	Register(voiceChan)

	// Send critical P0 alert with secondary escalation channel
	req := DeliveryRequest{
		NotificationID:  "p0_alert_1",
		Channel:         "chan_primary",
		Recipient:       "+123456",
		Title:           "Exchange Incident",
		Content:         "Bybit Security Issue",
		Severity:        "P0",
		EscalateChannel: "chan_voice",
	}

	res, err := engine.Dispatch(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, StatusSent, res.Status)

	// Simulate alert was sent 20 minutes ago and was NOT acked
	sentPast := time.Now().UTC().Add(-20 * time.Minute)
	require.NoError(t, dao.RecordDelivery(ctx, &model.NotificationDelivery{
		NotificationID:  "p0_alert_1",
		DedupKey:        "sec_issue",
		Channel:         "chan_primary",
		Recipient:       "+123456",
		Title:           "Exchange Incident",
		Content:         "Bybit Security Issue",
		Severity:        "P0",
		Status:          "SENT",
		EscalateChannel: "chan_voice",
		SentAt:          sentPast,
	}))

	// Trigger escalation check for unacked >15m
	escalated, err := engine.CheckAndEscalate(ctx, 15*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, escalated)

	// Secondary voice channel received the escalated message!
	require.Len(t, voiceChan.sentMsgs, 1)
	assert.Contains(t, voiceChan.sentMsgs[0].Title, "[ESCALATED P0]")

	// Check DB: status updated to ESCALATED
	rec, err := dao.GetByNotificationID(ctx, "p0_alert_1")
	require.NoError(t, err)
	assert.Equal(t, "ESCALATED", rec.Status)

	// Now operator acknowledges the alert
	err = engine.Acknowledge(ctx, "p0_alert_1")
	require.NoError(t, err)

	ackedRec, err := dao.GetByNotificationID(ctx, "p0_alert_1")
	require.NoError(t, err)
	assert.Equal(t, "ACKED", ackedRec.Status)
}

func TestEngineNilDAOAndErrors(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(nil)

	// nil DAO should be safe and no-op
	require.NoError(t, engine.Acknowledge(ctx, "any_id"))
	count, err := engine.CheckAndEscalate(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Dispatch with un-registered channel
	res, err := engine.Dispatch(ctx, DeliveryRequest{
		NotificationID: "notif_err",
		Channel:        "non_existent_channel",
	})
	require.Error(t, err)
	assert.Equal(t, StatusFailed, res.Status)
}
