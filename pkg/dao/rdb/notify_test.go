package rdb

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func notifyTestDAO(t *testing.T) (*NotificationDAO, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NotificationDelivery{}))
	return NewNotificationDAO(db), db
}
func TestNotificationDAORecordAndAck(t *testing.T) {
	dao, _ := notifyTestDAO(t)
	ctx := context.Background()

	require.Error(t, dao.RecordDelivery(ctx, nil))
	require.Error(t, dao.RecordDelivery(ctx, &model.NotificationDelivery{}))

	record := &model.NotificationDelivery{
		NotificationID:  "notif_1001",
		DedupKey:        "flash_crash_btc_20260929",
		Channel:         "webhook",
		Recipient:       "12345678",
		Title:           "BTC Flash Crash Alert",
		Content:         "Price fell >7% in 1 minute",
		Severity:        "P0",
		Status:          "SENT",
		SentAt:          time.Now().UTC(),
		EscalateChannel: "voice_call",
	}

	err := dao.RecordDelivery(ctx, record)
	require.NoError(t, err)

	found, err := dao.GetByNotificationID(ctx, "notif_1001")
	require.NoError(t, err)
	assert.Equal(t, "flash_crash_btc_20260929", found.DedupKey)
	assert.Equal(t, "SENT", found.Status)
	assert.Equal(t, "P0", found.Severity)

	// Dedup check: identical key within 5 minutes should be detected
	assert.True(t, dao.IsDeduped(ctx, "flash_crash_btc_20260929", 5*time.Minute))
	assert.False(t, dao.IsDeduped(ctx, "different_key", 5*time.Minute))

	// ACK Notification
	err = dao.AckNotification(ctx, "notif_1001")
	require.NoError(t, err)

	acked, err := dao.GetByNotificationID(ctx, "notif_1001")
	require.NoError(t, err)
	assert.Equal(t, "ACKED", acked.Status)
	assert.False(t, acked.AckedAt.IsZero())

	// Test UpdateStatus to SENT and other statuses
	err = dao.UpdateStatus(ctx, "notif_1001", "SENT", "")
	require.NoError(t, err)
	updated, err := dao.GetByNotificationID(ctx, "notif_1001")
	require.NoError(t, err)
	assert.Equal(t, "SENT", updated.Status)

	err = dao.UpdateStatus(ctx, "notif_1001", "FAILED", "network timeout")
	require.NoError(t, err)
	updated, err = dao.GetByNotificationID(ctx, "notif_1001")
	require.NoError(t, err)
	assert.Equal(t, "FAILED", updated.Status)
	assert.Equal(t, "network timeout", updated.LastError)

	// Test RecordDelivery updates existing record
	record.Status = "RESENT"
	err = dao.RecordDelivery(ctx, record)
	require.NoError(t, err)
	updated, err = dao.GetByNotificationID(ctx, "notif_1001")
	require.NoError(t, err)
	assert.Equal(t, "RESENT", updated.Status)

	// Test ListDeliveries
	list, err := dao.ListDeliveries(ctx, 10)
	require.NoError(t, err)
	assert.NotEmpty(t, list)

	listDefault, err := dao.ListDeliveries(ctx, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, listDefault)
}

func TestNotificationDAOUnackedEscalationQuery(t *testing.T) {
	dao, _ := notifyTestDAO(t)
	ctx := context.Background()

	pastTime := time.Now().UTC().Add(-20 * time.Minute)
	unacked := &model.NotificationDelivery{
		NotificationID:  "notif_p0_unacked",
		DedupKey:        "p0_key",
		Channel:         "webhook",
		Recipient:       "8888",
		Title:           "Systemic Shock",
		Content:         "Global Kill Switch Activated",
		Severity:        "P0",
		Status:          "SENT",
		SentAt:          pastTime,
		EscalateChannel: "voice_call",
	}
	require.NoError(t, dao.RecordDelivery(ctx, unacked))

	// Query unacked P0 alerts sent >15m ago
	escalations, err := dao.GetUnackedDeliveries(ctx, "P0", 15*time.Minute)
	require.NoError(t, err)
	assert.Len(t, escalations, 1)
	assert.Equal(t, "notif_p0_unacked", escalations[0].NotificationID)

	// Query unacked P0 alerts sent >30m ago (should be empty since sent 20m ago)
	escalations, err = dao.GetUnackedDeliveries(ctx, "P0", 30*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, escalations)
}

func TestNotificationDAOPaginationAndStats(t *testing.T) {
	dao, _ := notifyTestDAO(t)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		sev := "INFO"
		status := "SENT"
		if i == 1 {
			sev = "P0"
		} else if i == 2 {
			status = "ACKED"
		} else if i == 3 {
			status = "FAILED"
		} else if i == 4 {
			status = "ESCALATED"
		}
		require.NoError(t, dao.RecordDelivery(ctx, &model.NotificationDelivery{
			NotificationID: "n_" + string(rune('0'+i)),
			Channel:        "webhook",
			Recipient:      "user1",
			Title:          "Alert",
			Severity:       sev,
			Status:         status,
			SentAt:         time.Now().UTC(),
		}))
	}

	rows, total, err := dao.ListDeliveriesPaginated(ctx, 1, 2, "webhook", "", "", "Alert")
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, rows, 2)

	// Filter by severity P0
	rowsP0, totalP0, err := dao.ListDeliveriesPaginated(ctx, 0, 0, "", "P0", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalP0)
	assert.Len(t, rowsP0, 1)

	// Filter by status ACKED
	rowsAck, totalAck, err := dao.ListDeliveriesPaginated(ctx, 1, 10, "", "", "ACKED", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), totalAck)
	assert.Len(t, rowsAck, 1)

	stats, err := dao.GetStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), stats.Total)
	assert.Equal(t, int64(2), stats.Sent)
	assert.Equal(t, int64(1), stats.Acked)
	assert.Equal(t, int64(1), stats.Escalated)
	assert.Equal(t, int64(1), stats.Failed)
	assert.Equal(t, int64(1), stats.UnackedP0)
}

func TestDeliverySearchKeywordCombinesWithTheOtherFilters(t *testing.T) {
	dao, db := notifyTestDAO(t)
	ctx := context.Background()
	for i, row := range []model.NotificationDelivery{
		{NotificationID: "n1", Channel: "webhook", Recipient: "alice", Title: "BTC crash", Content: "x", Severity: "P0", Status: "SENT"},
		{NotificationID: "n2", Channel: "discord", Recipient: "bob", Title: "BTC crash", Content: "x", Severity: "P0", Status: "SENT"},
		{NotificationID: "n3", Channel: "webhook", Recipient: "alice", Title: "ETH funding", Content: "btc mentioned", Severity: "INFO", Status: "ACKED"},
	} {
		row.DedupKey = row.NotificationID
		require.NoError(t, db.Create(&row).Error, i)
	}
	rows, total, err := dao.ListDeliveriesPaginated(ctx, 1, 20, "webhook", "", "SENT", "btc")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "the keyword narrows the other filters, it does not widen them")
	require.Len(t, rows, 1)
	assert.Equal(t, "n1", rows[0].NotificationID)

	_, total, err = dao.ListDeliveriesPaginated(ctx, 1, 20, "", "", "", "BTC")
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "case does not matter and content is searched too")
	_, total, err = dao.ListDeliveriesPaginated(ctx, 1, 20, "", "", "", "  alice ")
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "spaces around the keyword are ignored")
}

func TestUnackedFilterAndBatchAck(t *testing.T) {
	dao, db := notifyTestDAO(t)
	ctx := context.Background()
	for _, row := range []model.NotificationDelivery{
		{NotificationID: "a", Channel: "webhook", Title: "t", Content: "c", Severity: "P0", Status: "SENT"},
		{NotificationID: "b", Channel: "webhook", Title: "t", Content: "c", Severity: "INFO", Status: "PENDING"},
		{NotificationID: "c", Channel: "discord", Title: "t", Content: "c", Severity: "P1", Status: "ESCALATED"},
		{NotificationID: "d", Channel: "webhook", Title: "t", Content: "c", Severity: "P0", Status: "FAILED"},
		{NotificationID: "e", Channel: "webhook", Title: "t", Content: "c", Severity: "P0", Status: "ACKED"},
	} {
		row.DedupKey = row.NotificationID
		require.NoError(t, db.Create(&row).Error)
	}
	_, total, err := dao.ListDeliveriesPaginated(ctx, 1, 20, "", "", StatusUnacked, "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "only critical and high notifications wait for an operator; info, failed and acknowledged do not")
	stats, err := dao.GetStats(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, stats.Unacked)

	n, err := dao.AckMany(ctx, []string{"a", "d", "e"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "only deliveries still waiting change")
	n, err = dao.AckMatching(ctx, "webhook", "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "b is only information, so a bulk acknowledge of what needs attention leaves it")
	_, total, _ = dao.ListDeliveriesPaginated(ctx, 1, 20, "", "", StatusUnacked, "")
	assert.EqualValues(t, 1, total, "c (escalated, another channel) still waits")
}
