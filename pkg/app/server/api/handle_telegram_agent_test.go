package api

import (
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestAgentStatusPageShowsAgentsCacheAccountsAndBots(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	ctx := t.Context()
	agents := rdb.NewTelegramAgentStatusDAO(db)
	require.NoError(t, agents.Report(ctx, &model.TelegramAgentStatus{AgentID: "host-a", Version: "1.0.0", StartedAt: time.Now(), Accounts: 2, Pending: 3, JobsJSON: `[{"name":"forward"}]`}))
	require.NoError(t, agents.Report(ctx, &model.TelegramAgentStatus{AgentID: "host-b", StartedAt: time.Now()}))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, db.Model(&model.TelegramAgentStatus{}).Where("agent_id = ?", "host-b").Update("seen_at", &old).Error)

	now := time.Now()
	require.NoError(t, db.Create(&model.TelegramClientAccount{Name: "a", PhoneNumber: "+1", APIID: 1, APIHashEncrypted: "x", ConnectionStatus: model.TelegramClientConnected, SeenAt: &now}).Error)
	require.NoError(t, db.Create(&model.TelegramClientAccount{Name: "b", PhoneNumber: "+2", APIID: 1, APIHashEncrypted: "x", ConnectionStatus: model.TelegramClientError}).Error)
	require.NoError(t, db.Create(&model.TelegramBot{Name: "bot", TokenEncrypted: "x", Mode: "polling", Status: model.TelegramBotRunning, SeenAt: &now}).Error)
	require.NoError(t, db.Create(&model.TelegramBot{Name: "bad", TokenEncrypted: "x", Mode: "polling", Status: model.TelegramBotError}).Error)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	require.NoError(t, db.Create(&model.TelegramClientMessage{AccountID: 1, PeerID: 1, TelegramMessageID: 1, SourceType: "channel", Status: model.TelegramMsgPending, MessageAt: now}).Error)

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/agents", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Data struct {
			List    []model.TelegramAgentStatus `json:"list"`
			Total   float64                     `json:"total"`
			Summary agentSummary                `json:"summary"`
		} `json:"data"`
	}
	decode(t, rec.Body.Bytes(), &out)
	require.EqualValues(t, 2, out.Data.Total)
	assert.True(t, out.Data.List[0].Online)
	assert.False(t, out.Data.List[1].Online, "no recent heartbeat means offline")
	assert.EqualValues(t, 1, out.Data.Summary.Cache[model.TelegramMsgPending])
	assert.EqualValues(t, 2, out.Data.Summary.Accounts["total"])
	assert.EqualValues(t, 1, out.Data.Summary.Accounts["connected"])
	assert.EqualValues(t, 1, out.Data.Summary.Accounts["error"])
	assert.EqualValues(t, 1, out.Data.Summary.Bots["running"])
	assert.EqualValues(t, 1, out.Data.Summary.Bots["error"])

	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/agents?online=true&keyword=host", "", nil)
	decode(t, rec.Body.Bytes(), &out)
	assert.EqualValues(t, 1, out.Data.Total)

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/agents/batch-delete", "", map[string]any{"ids": []int64{out.Data.List[0].ID}})
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/agents/batch-delete", "", map[string]any{"ids": []int64{}})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentStatusReportsStorageFailuresAndNeedsAnAdmin(t *testing.T) {
	broken := telegramBrokenRouter(t)
	assert.Equal(t, http.StatusInternalServerError, performJSON(t, broken, http.MethodGet, "/telegram/admin/agents", "", nil).Code)
	assert.Equal(t, http.StatusInternalServerError, performJSON(t, broken, http.MethodPost, "/telegram/admin/agents/batch-delete", "", map[string]any{"ids": []int64{1}}).Code)

	denied, _ := telegramClientAPIRouter(t, []string{"VIEWER"}, time.Second)
	assert.Equal(t, http.StatusForbidden, performJSON(t, denied, http.MethodGet, "/telegram/admin/agents", "", nil).Code)
	assert.Equal(t, http.StatusForbidden, performJSON(t, denied, http.MethodPost, "/telegram/admin/agents/batch-delete", "", map[string]any{"ids": []int64{1}}).Code)
}

func TestForwardRulesCarryTheirSourceAndTheirChannel(t *testing.T) {
	router, _ := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	stream := makeChannel(t, router, map[string]any{"name": "stream", "transport": "redis_stream", "stream": "tg:dm", "consumer_group": "g", "consumer_kind": "external"})
	topic := makeChannel(t, router, map[string]any{"name": "topic", "transport": "kafka", "brokers": "k:9092", "topic": "t", "consumer_group": "g", "consumer_kind": "external"})
	post := func(body map[string]any) int {
		return performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules", "", body).Code
	}
	assert.Equal(t, http.StatusOK, post(map[string]any{"name": "dm to redis", "account_id": 2, "source_type": "private", "target_kind": "channel", "channel_id": stream}))
	assert.Equal(t, http.StatusOK, post(map[string]any{"name": "kafka", "target_kind": "channel", "channel_id": topic, "pattern": "BTC"}))
	assert.Equal(t, http.StatusBadRequest, post(map[string]any{"name": "bad", "target_kind": "channel", "channel_id": topic, "source_type": "email"}))
	assert.Equal(t, http.StatusBadRequest, post(map[string]any{"name": "bad2", "target_kind": "channel"}))

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/forward-rules?account_id=2&source_type=private", "", nil)
	list, total := decodeList[model.TelegramForwardRule](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.Equal(t, "private", list[0].SourceType)
	assert.EqualValues(t, 2, list[0].AccountID)
	assert.Equal(t, "channel", list[0].TargetKind)
	assert.Equal(t, "stream", list[0].ChannelName, "a rule shows the channel it writes to")
}

func TestCachedMessagesCanBeFilteredByStatusAndSource(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	for i, st := range []struct{ status, source string }{{"pending", "channel"}, {"consumed", "private"}, {"failed", "system"}} {
		require.NoError(t, db.Create(&model.TelegramClientMessage{AccountID: 1, PeerID: 1, TelegramMessageID: i + 1, Status: st.status, SourceType: st.source, Text: "t", MessageAt: time.Now()}).Error)
	}
	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages?status=consumed", "", nil)
	list, total := decodeList[model.TelegramClientMessage](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.Equal(t, "private", list[0].SourceType)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages?source_type=system", "", nil)
	_, total = decodeList[model.TelegramClientMessage](t, rec.Body.Bytes())
	assert.EqualValues(t, 1, total)
}

func TestAMessageIsReadInFullOnlyOnRequestAndManagedInBatches(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientMessage{}))
	var ids []int64
	for i := 1; i <= 3; i++ {
		row := model.TelegramClientMessage{AccountID: 1, PeerID: 1, TelegramMessageID: i, SourceType: "channel", Status: model.TelegramMsgFailed, MessageAt: time.Now()}
		row.SetText("full text of message " + string(rune('0'+i)))
		require.NoError(t, db.Create(&row).Error)
		ids = append(ids, row.ID)
	}

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages", "", nil)
	assert.NotContains(t, rec.Body.String(), "full text of message", "the list carries no message content")
	list, _ := decodeList[model.TelegramClientMessage](t, rec.Body.Bytes())
	require.Len(t, list, 3)
	assert.NotZero(t, list[0].Size)

	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages/"+idString(ids[0]), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "full text of message 1")
	assert.GreaterOrEqual(t, performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages/999", "", nil).Code, 400)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages/nope", "", nil).Code)

	batch := func(action string, ids ...int64) int {
		return performJSON(t, router, http.MethodPost, "/telegram/admin/client-messages/batch", "", map[string]any{"ids": ids, "action": action}).Code
	}
	assert.Equal(t, http.StatusOK, batch("retry", ids[0], ids[1]))
	var pending int64
	db.Model(&model.TelegramClientMessage{}).Where("status = ?", model.TelegramMsgPending).Count(&pending)
	assert.EqualValues(t, 2, pending)
	assert.Equal(t, http.StatusOK, batch("consume", ids[0]))
	assert.Equal(t, http.StatusOK, batch("delete", ids[1], ids[2]))
	var left int64
	db.Model(&model.TelegramClientMessage{}).Count(&left)
	assert.EqualValues(t, 1, left)
	assert.Equal(t, http.StatusBadRequest, batch("explode", ids[0]))
	assert.Equal(t, http.StatusBadRequest, performJSON(t, router, http.MethodPost, "/telegram/admin/client-messages/batch", "", map[string]any{"ids": []int64{}, "action": "delete"}).Code)

	broken := telegramBrokenRouter(t)
	for _, action := range []string{"delete", "retry", "consume"} {
		assert.Equal(t, http.StatusInternalServerError, performJSON(t, broken, http.MethodPost, "/telegram/admin/client-messages/batch", "", map[string]any{"ids": []int64{1}, "action": action}).Code, action)
	}

	denied, _ := telegramClientAPIRouter(t, []string{"VIEWER"}, time.Second)
	assert.Equal(t, http.StatusForbidden, performJSON(t, denied, http.MethodGet, "/telegram/admin/client-messages/1", "", nil).Code)
	assert.Equal(t, http.StatusForbidden, performJSON(t, denied, http.MethodPost, "/telegram/admin/client-messages/batch", "", map[string]any{"ids": []int64{1}, "action": "delete"}).Code)
}

func TestSubscriptionsAreSwitchedOnAndOffInBatches(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	var ids []int64
	for i := 1; i <= 3; i++ {
		sub := model.TelegramClientSubscription{AccountID: 1, PeerID: int64(i), PeerType: "channel", Enabled: true}
		require.NoError(t, db.Create(&sub).Error)
		ids = append(ids, sub.ID)
	}
	post := func(enabled bool, ids ...int64) int {
		return performJSON(t, router, http.MethodPost, "/telegram/admin/client-subscriptions/batch-enable", "", map[string]any{"ids": ids, "enabled": enabled}).Code
	}
	assert.Equal(t, http.StatusOK, post(false, ids[0], ids[1]))
	var enabled int64
	db.Model(&model.TelegramClientSubscription{}).Where("enabled = ?", true).Count(&enabled)
	assert.EqualValues(t, 1, enabled)
	assert.Equal(t, http.StatusOK, post(true, ids[0]))
	assert.Equal(t, http.StatusBadRequest, post(true))
	assert.Equal(t, http.StatusInternalServerError, performJSON(t, telegramBrokenRouter(t), http.MethodPost, "/telegram/admin/client-subscriptions/batch-enable", "", map[string]any{"ids": []int64{1}, "enabled": true}).Code)
}

func TestForwardRulesAreListedByClass(t *testing.T) {
	router, _ := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	topic := makeChannel(t, router, map[string]any{"name": "topic", "transport": "kafka", "brokers": "k:9092", "topic": "t", "consumer_group": "g", "consumer_kind": "external"})
	post := func(body map[string]any) {
		require.Equal(t, http.StatusOK, performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules", "", body).Code)
	}
	post(map[string]any{"name": "to topic", "target_kind": "channel", "channel_id": topic})
	post(map[string]any{"name": "to user", "target_kind": "notify", "target_ref": "telegram", "target_recipient": "42"})

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/forward-rules?target_class=user", "", nil)
	list, total := decodeList[model.TelegramForwardRule](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.Equal(t, "to user", list[0].Name)
	assert.Equal(t, "user", list[0].TargetClass)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/forward-rules?target_class=task", "", nil)
	list, total = decodeList[model.TelegramForwardRule](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.Equal(t, "task", list[0].TargetClass)
}
