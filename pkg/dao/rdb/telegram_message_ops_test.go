package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestCachedMessageTextIsStoredCompressedAndOnlyLoadedOnRequest(t *testing.T) {
	db := agentTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	long := strings.Repeat("BTC is breaking out with heavy volume right now. ", 50)
	row := cacheRow(1, model.TelegramMsgPending, model.TelegramSourceChannel)
	row.Text = long
	require.NoError(t, dao.SaveMessage(ctx, row))

	var stored model.TelegramClientMessage
	require.NoError(t, db.First(&stored, row.ID).Error)
	assert.Less(t, len(stored.Body), len(long)/4, "what is stored is far smaller than the text")
	assert.EqualValues(t, len(long), stored.Size)

	list := &apis.TelegramClientMessageListReq{}
	list.Page, list.PageSize = 1, 10
	rows, _, err := dao.ListMessages(ctx, list)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].Body, "lists never load the bodies")
	assert.Empty(t, rows[0].Text)
	assert.EqualValues(t, len(long), rows[0].Size, "but they know how big the message is")

	full, err := dao.GetMessage(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, long, full.Text)
	_, err = dao.GetMessage(ctx, 404)
	assert.Error(t, err)
	require.NoError(t, db.Model(&model.TelegramClientMessage{}).Where("id = ?", row.ID).Update("body", []byte{9}).Error)
	_, err = dao.GetMessage(ctx, row.ID)
	assert.ErrorIs(t, err, model.ErrBadBody)

	// The text is not searchable (it is compressed); the sender and the source are.
	list.Keyword = "Ann"
	_, n, _ := dao.ListMessages(ctx, list)
	assert.EqualValues(t, 1, n)
	list.Keyword = "breaking out"
	_, n, _ = dao.ListMessages(ctx, list)
	assert.Zero(t, n)
}

func TestMessagesCanBeDiscardedRetriedOrConsumedInBatches(t *testing.T) {
	db := agentTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	now := time.Now()
	for i := 1; i <= 4; i++ {
		require.NoError(t, dao.SaveMessage(ctx, cacheRow(i, model.TelegramMsgFailed, model.TelegramSourceChannel)))
	}
	require.NoError(t, db.Model(&model.TelegramClientMessage{}).Where("telegram_message_id = ?", 1).
		Updates(map[string]any{"attempts": 5, "done_rules": "[1,2]", "last_error": "boom", "consumed_at": &now}).Error)
	ids := func(n ...int) []int64 {
		out := []int64{}
		for _, i := range n {
			var m model.TelegramClientMessage
			require.NoError(t, db.Where("telegram_message_id = ?", i).First(&m).Error)
			out = append(out, m.ID)
		}
		return out
	}

	n, err := dao.RetryMessages(ctx, ids(1, 2))
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	var m model.TelegramClientMessage
	require.NoError(t, db.Where("telegram_message_id = ?", 1).First(&m).Error)
	assert.Equal(t, model.TelegramMsgPending, m.Status)
	assert.Zero(t, m.Attempts)
	assert.Empty(t, m.DoneRules, "a retry delivers to every target again")
	assert.Empty(t, m.LastError)
	assert.Nil(t, m.ConsumedAt)

	n, err = dao.ConsumeMessages(ctx, ids(3), now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	var consumed model.TelegramClientMessage
	require.NoError(t, db.Where("telegram_message_id = ?", 3).First(&consumed).Error)
	assert.Equal(t, model.TelegramMsgConsumed, consumed.Status)
	assert.NotNil(t, consumed.ConsumedAt)

	require.NoError(t, dao.FailMessage(ctx, ids(2)[0], "unreadable"))
	var failed model.TelegramClientMessage
	require.NoError(t, db.Where("telegram_message_id = ?", 2).First(&failed).Error)
	assert.Equal(t, model.TelegramMsgFailed, failed.Status)
	assert.Equal(t, "unreadable", failed.LastError)

	n, err = dao.DeleteMessages(ctx, ids(3, 4))
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	for _, f := range []func(context.Context, []int64) (int64, error){dao.DeleteMessages, dao.RetryMessages} {
		n, err = f(ctx, nil)
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	n, err = dao.ConsumeMessages(ctx, nil, now)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestSubscriptionsAreEnabledAndDisabledInBatches(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		require.NoError(t, dao.CreateSubscription(ctx, &model.TelegramClientSubscription{AccountID: 1, PeerID: int64(i), PeerType: "channel", Enabled: true}))
	}
	n, err := dao.SetSubscriptionsEnabled(ctx, []int64{1, 2}, false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	enabled := func() int64 {
		req := &apis.TelegramClientSubscriptionListReq{Enabled: boolPtr(true)}
		req.Page, req.PageSize = 1, 10
		_, total, err := dao.ListSubscriptions(ctx, req)
		require.NoError(t, err)
		return total
	}
	assert.EqualValues(t, 1, enabled())
	_, err = dao.SetSubscriptionsEnabled(ctx, []int64{1, 2}, true)
	require.NoError(t, err)
	assert.EqualValues(t, 3, enabled())
	n, err = dao.SetSubscriptionsEnabled(ctx, nil, true)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRulesKnowWhetherTheyForwardToTasksOrToUsers(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardRuleDAO(db)
	channel := directChannel(t, db, "jobs")
	ctx := context.Background()
	mk := func(name, kind, ref, recipient string) model.TelegramForwardRule {
		r := model.TelegramForwardRule{Name: name, TargetKind: kind, TargetRef: ref, TargetRecipient: recipient, Enabled: true}
		if kind == "channel" {
			r.ChannelID = channel
		}
		require.NoError(t, dao.Create(ctx, &r))
		return r
	}
	task := mk("to topic", "channel", "", "")
	user := mk("to user", "notify", "telegram", "42")
	assert.Equal(t, model.ForwardClassTask, task.TargetClass, "a rule just created already carries its class")
	assert.Equal(t, model.ForwardClassUser, user.TargetClass)
	assert.Equal(t, "jobs", task.TargetRef, "a channel rule records the channel it writes to")
	mk("to job", "channel", "", "")

	got, err := dao.Get(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, model.ForwardClassUser, got.TargetClass, "and so does one read back")
	count := func(class string) int64 {
		_, n, err := dao.List(ctx, 1, 10, TelegramForwardRuleFilter{TargetClass: class})
		require.NoError(t, err)
		return n
	}
	assert.EqualValues(t, 2, count(model.ForwardClassTask))
	assert.EqualValues(t, 1, count(model.ForwardClassUser))
	assert.EqualValues(t, 3, count(""))

	updated, err := dao.Update(ctx, task.ID, model.TelegramForwardRule{TargetKind: "notify", TargetRef: "telegram", TargetRecipient: "7"})
	require.NoError(t, err)
	assert.Equal(t, model.ForwardClassUser, updated.TargetClass)
}
