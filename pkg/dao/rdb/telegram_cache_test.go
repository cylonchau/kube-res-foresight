package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func cacheRow(id int, status, sourceType string) *model.TelegramClientMessage {
	return &model.TelegramClientMessage{
		AccountID: 1, PeerID: 1, TelegramMessageID: id, SourceType: sourceType, Source: "src", Text: "text", SenderName: "Ann",
		MessageAt: time.Now(), Status: status,
	}
}

func TestTheMessageCacheIsConsumedRetriedAndCleaned(t *testing.T) {
	db := agentTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	now := time.Now()

	for i := 1; i <= 5; i++ {
		require.NoError(t, dao.SaveMessage(ctx, cacheRow(i, model.TelegramMsgPending, model.TelegramSourceChannel)))
	}
	require.NoError(t, dao.SaveMessage(ctx, cacheRow(1, model.TelegramMsgPending, model.TelegramSourceChannel)), "a redelivered message is cached once")

	due, err := dao.ClaimDueMessages(ctx, now, 3)
	require.NoError(t, err)
	require.Len(t, due, 3, "a batch is limited and ordered oldest first")
	assert.Equal(t, 1, due[0].TelegramMessageID)

	require.NoError(t, dao.MarkMessageConsumed(ctx, due[0].ID, "[1]", now))
	require.NoError(t, dao.DeferMessage(ctx, due[1].ID, now.Add(time.Hour)))
	require.NoError(t, dao.RetryMessage(ctx, due[2].ID, 1, 3, now.Add(time.Minute), "[2]", "boom"))

	due, _ = dao.ClaimDueMessages(ctx, now, 10)
	ids := []int{}
	for _, row := range due {
		ids = append(ids, row.TelegramMessageID)
	}
	assert.Equal(t, []int{4, 5}, ids, "consumed, deferred and not-yet-due messages are skipped")
	due, _ = dao.ClaimDueMessages(ctx, now.Add(2*time.Hour), 10)
	assert.Len(t, due, 4, "once their time has come the deferred and retried ones are back")

	var retried model.TelegramClientMessage
	require.NoError(t, db.Where("telegram_message_id = ?", 3).First(&retried).Error)
	assert.EqualValues(t, 1, retried.Attempts)
	assert.Equal(t, "boom", retried.LastError)
	assert.Equal(t, "[2]", retried.DoneRules)
	require.NoError(t, dao.RetryMessage(ctx, retried.ID, 3, 3, now, "[2]", "boom again"))
	var failed model.TelegramClientMessage
	require.NoError(t, db.First(&failed, retried.ID).Error)
	assert.Equal(t, model.TelegramMsgFailed, failed.Status, "the last allowed attempt fails the message")
	assert.Nil(t, failed.NextAttemptAt)

	stats, err := dao.MessageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{model.TelegramMsgPending: 3, model.TelegramMsgConsumed: 1, model.TelegramMsgFailed: 1}, stats)

	require.NoError(t, db.Model(&model.TelegramClientMessage{}).Where("telegram_message_id = ?", 1).Update("consumed_at", now.Add(-time.Hour)).Error)
	require.NoError(t, db.Model(&model.TelegramClientMessage{}).Where("telegram_message_id = ?", 5).UpdateColumn("created_at", now.Add(-48*time.Hour)).Error)
	consumed, expired, err := dao.CleanMessages(ctx, now.Add(-5*time.Minute), now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, consumed)
	assert.EqualValues(t, 1, expired)
	stats, _ = dao.MessageStats(ctx)
	assert.EqualValues(t, 3, stats[model.TelegramMsgPending]+stats[model.TelegramMsgFailed]+stats[model.TelegramMsgConsumed])
}

func TestCachedMessagesAreFilteredForThePlatform(t *testing.T) {
	db := agentTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	require.NoError(t, dao.SaveMessage(ctx, cacheRow(1, model.TelegramMsgPending, model.TelegramSourceChannel)))
	require.NoError(t, dao.SaveMessage(ctx, cacheRow(2, model.TelegramMsgConsumed, model.TelegramSourcePrivate)))
	require.NoError(t, dao.SaveMessage(ctx, cacheRow(3, model.TelegramMsgFailed, model.TelegramSourceSystem)))
	count := func(req apis.TelegramClientMessageListReq) int64 {
		req.Page, req.PageSize = 1, 10
		_, n, err := dao.ListMessages(ctx, &req)
		require.NoError(t, err)
		return n
	}
	assert.EqualValues(t, 3, count(apis.TelegramClientMessageListReq{}))
	assert.EqualValues(t, 1, count(apis.TelegramClientMessageListReq{Status: model.TelegramMsgConsumed}))
	assert.EqualValues(t, 1, count(apis.TelegramClientMessageListReq{SourceType: model.TelegramSourceSystem}))
	assert.EqualValues(t, 3, count(apis.TelegramClientMessageListReq{ListOptions: apis.ListOptions{Keyword: "src"}}), "the source is searchable")
	assert.EqualValues(t, 0, count(apis.TelegramClientMessageListReq{ListOptions: apis.ListOptions{Keyword: "nobody"}}))
}

func TestAgentHeartbeatsAreStoredOncePerAgentAndAgeIntoOffline(t *testing.T) {
	db := agentTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TelegramAgentStatus{}))
	dao := NewTelegramAgentStatusDAO(db)
	ctx := context.Background()

	require.NoError(t, dao.Report(ctx, &model.TelegramAgentStatus{AgentID: "a", Version: "1", StartedAt: time.Now(), Accounts: 1, Pending: 4}))
	require.NoError(t, dao.Report(ctx, &model.TelegramAgentStatus{AgentID: "a", Version: "2", StartedAt: time.Now(), Accounts: 2, Pending: 9, LastError: "boom"}))
	require.NoError(t, dao.Report(ctx, &model.TelegramAgentStatus{AgentID: "b", StartedAt: time.Now()}))

	rows, total, err := dao.List(ctx, 1, 10, TelegramAgentFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 2, total, "a second report updates the same row")
	assert.Equal(t, "2", rows[0].Version)
	assert.EqualValues(t, 9, rows[0].Pending)
	assert.True(t, rows[0].Online)

	old := time.Now().Add(-time.Hour)
	require.NoError(t, db.Model(&model.TelegramAgentStatus{}).Where("agent_id = ?", "b").Update("seen_at", &old).Error)
	yes, no := true, false
	_, online, _ := dao.List(ctx, 1, 10, TelegramAgentFilter{Online: &yes})
	_, offline, _ := dao.List(ctx, 1, 10, TelegramAgentFilter{Online: &no})
	assert.EqualValues(t, 1, online)
	assert.EqualValues(t, 1, offline)
	_, found, _ := dao.List(ctx, 1, 10, TelegramAgentFilter{Keyword: "b"})
	assert.EqualValues(t, 1, found)

	n, err := dao.Delete(ctx, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = dao.Delete(ctx, []int64{rows[1].ID})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func TestRulesAreMatchedBySourceAndChannelTargetsAreValidated(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardRuleDAO(db)
	channel := directChannel(t, db, "jobs")
	ctx := context.Background()
	mk := func(r model.TelegramForwardRule) error {
		r.Enabled = true
		return dao.Create(ctx, &r)
	}
	require.NoError(t, mk(model.TelegramForwardRule{Name: "q", TargetKind: "channel", ChannelID: channel}))
	require.NoError(t, mk(model.TelegramForwardRule{Name: "t", TargetKind: "channel", ChannelID: channel, SourceType: "private", AccountID: 2}))
	assert.ErrorAs(t, mk(model.TelegramForwardRule{Name: "x", TargetKind: "channel"}), &InvalidInputError{}, "a channel rule names its channel")
	assert.ErrorAs(t, mk(model.TelegramForwardRule{Name: "y", TargetKind: "channel", ChannelID: 999}), &InvalidInputError{}, "and the channel must exist")
	assert.ErrorAs(t, mk(model.TelegramForwardRule{Name: "v", TargetKind: "kafka", TargetRef: "topic"}), &InvalidInputError{}, "the old target kinds are gone")
	assert.ErrorAs(t, mk(model.TelegramForwardRule{Name: "z", TargetKind: "channel", ChannelID: channel, SourceType: "email"}), &InvalidInputError{})
	assert.ErrorAs(t, mk(model.TelegramForwardRule{Name: "w", TargetKind: "channel", ChannelID: channel, AccountID: -1}), &InvalidInputError{})

	names := func(account, sub int64, sourceType string) []string {
		rows, err := dao.ForSource(ctx, account, sub, sourceType)
		require.NoError(t, err)
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	assert.Equal(t, []string{"q"}, names(1, 0, "channel"))
	assert.Equal(t, []string{"q", "t"}, names(2, 0, "private"))
	assert.Equal(t, []string{"q"}, names(2, 0, "channel"))

	_, total, err := dao.List(ctx, 1, 10, TelegramForwardRuleFilter{AccountID: 2, SourceType: "private"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
}
