package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"strconv"
	"testing"
	"time"
)

const testBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"

func decode(t *testing.T, body []byte, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(body, out))
}

func decodeList[T any](t *testing.T, body []byte) ([]T, float64) {
	t.Helper()
	var out struct {
		Data struct {
			List  []T     `json:"list"`
			Total float64 `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	return out.Data.List, out.Data.Total
}

func TestTelegramBotsAreManagedWithoutExposingTheToken(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)

	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/bots", "", map[string]any{"name": "alpha", "token": testBotToken, "username": "@alpha_bot"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), testBotToken, "the token is never returned")
	assert.Contains(t, rec.Body.String(), `"has_token":true`)

	var bot model.TelegramBot
	require.NoError(t, db.First(&bot).Error)
	assert.NotEqual(t, testBotToken, bot.TokenEncrypted, "stored encrypted")
	assert.Equal(t, "alpha_bot", bot.Username)
	assert.Equal(t, model.TelegramModePolling, bot.Mode)

	for name, body := range map[string]map[string]any{
		"duplicate name": {"name": "alpha", "token": testBotToken},
		"bad token":      {"name": "beta", "token": "nope"},
		"webhook mode":   {"name": "beta", "token": testBotToken, "mode": "webhook"},
		"unknown mode":   {"name": "beta", "token": testBotToken, "mode": "carrier-pigeon"},
		"missing token":  {"name": "beta"},
	} {
		rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bots", "", body)
		assert.GreaterOrEqual(t, rec.Code, 400, name)
	}

	// Two commands: one bound to the bot, one to every bot.
	require.NoError(t, db.Create(&model.BotCommandDef{BotID: bot.ID, Command: "/status", Name: "s", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.BotCommandDef{BotID: 0, Command: "/help", Name: "h", Enabled: true}).Error)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/bots?keyword=alp&enabled=true", "", nil)
	bots, total := decodeList[model.TelegramBot](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.EqualValues(t, 2, bots[0].CommandCount)

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/bots/%d", bot.ID), "", map[string]any{"name": "alpha2", "token": "", "enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, db.First(&bot, bot.ID).Error)
	assert.Equal(t, "alpha2", bot.Name)
	assert.False(t, bot.Enabled)
	assert.NotEmpty(t, bot.TokenEncrypted, "an empty token keeps the stored one")
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/bots/%d", bot.ID), "", map[string]any{"token": "bad"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/bots/999", "", map[string]any{"name": "x"})
	assert.GreaterOrEqual(t, rec.Code, 400)

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bots/batch-enable", "", map[string]any{"ids": []int64{bot.ID}, "enabled": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, db.First(&bot, bot.ID).Error)
	assert.True(t, bot.Enabled)

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bots/batch-delete", "", map[string]any{"ids": []int64{bot.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var remaining int64
	db.Model(&model.BotCommandDef{}).Where("bot_id = ?", bot.ID).Count(&remaining)
	assert.Zero(t, remaining, "a deleted bot takes its commands with it")
	db.Model(&model.BotCommandDef{}).Where("bot_id = 0").Count(&remaining)
	assert.EqualValues(t, 1, remaining, "commands of every bot stay")

	for _, path := range []string{"/telegram/admin/bots/batch-delete", "/telegram/admin/bots/batch-enable"} {
		rec = performJSON(t, router, http.MethodPost, path, "", map[string]any{"ids": []int64{}})
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

func TestTelegramBotCommandSyncIsAnAgentAction(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, 2*time.Second)
	got := make(chan model.TelegramAgentRequest, 1)
	fakeAgent(t, db, func(req *model.TelegramAgentRequest, _ *rdb.TelegramAgentRequestDAO) (any, error) {
		got <- *req
		return nil, nil
	})
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/bots/5/sync-commands", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	req := <-got
	assert.Equal(t, model.AgentReqSyncBotCommand, req.Kind)
	assert.EqualValues(t, 5, req.TargetID)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bots/nope/sync-commands", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTelegramForwardRulesAreValidatedAndManaged(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, time.Second)
	require.NoError(t, db.Create(&model.WorkflowDef{WorkflowKey: "review", Name: "Review", InitialState: "new", PipelineJSON: "{}", Enabled: true}).Error)
	jobs := jobChannel(t, router, "jobs")
	flow := makeChannel(t, router, map[string]any{"name": "flow", "transport": "direct", "consumer_kind": "workflow", "consumer_ref": "review"})
	create := func(body map[string]any) int {
		return performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules", "", body).Code
	}
	assert.Equal(t, http.StatusOK, create(map[string]any{"name": "to ops", "target_kind": "notify", "target_ref": "telegram", "target_recipient": "42"}))
	assert.Equal(t, http.StatusOK, create(map[string]any{"name": "rate", "target_kind": "channel", "channel_id": jobs, "pattern": "BTC|ETH", "subscription_id": 3}))
	assert.Equal(t, http.StatusOK, create(map[string]any{"name": "flow", "target_kind": "channel", "channel_id": flow, "enabled": false}))
	for name, body := range map[string]map[string]any{
		"no name":            {"target_kind": "channel", "channel_id": jobs},
		"unknown kind":       {"name": "a", "target_kind": "pigeon", "target_ref": "x"},
		"notify no recip":    {"name": "a", "target_kind": "notify", "target_ref": "telegram"},
		"channel not chosen": {"name": "a", "target_kind": "channel"},
		"channel missing":    {"name": "a", "target_kind": "channel", "channel_id": 999},
		"old job kind":       {"name": "a", "target_kind": "job", "target_ref": "x"},
		"bad regex":          {"name": "a", "target_kind": "channel", "channel_id": jobs, "pattern": "("},
		"duplicate name":     {"name": "rate", "target_kind": "channel", "channel_id": jobs},
		"negative sub":       {"name": "a", "target_kind": "channel", "channel_id": jobs, "subscription_id": -1},
		"kind needs a value": {"name": "a", "target_ref": "x"},
	} {
		assert.GreaterOrEqual(t, create(body), 400, name)
	}

	rec := performJSON(t, router, http.MethodGet, "/telegram/admin/forward-rules?target_kind=channel&subscription_id=3&enabled=true&keyword=rat", "", nil)
	rules, total := decodeList[model.TelegramForwardRule](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	id := rules[0].ID

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/forward-rules/%d", id), "", map[string]any{"pattern": "SOL", "target_kind": "channel", "channel_id": jobs, "name": "rate"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/forward-rules/%d", id), "", map[string]any{"pattern": "("})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/forward-rules/999", "", map[string]any{"name": "x"})
	assert.GreaterOrEqual(t, rec.Code, 400)
	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/forward-rules/nope", "", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules/batch-enable", "", map[string]any{"ids": []int64{id}, "enabled": false})
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/forward-rules?enabled=false", "", nil)
	_, total = decodeList[model.TelegramForwardRule](t, rec.Body.Bytes())
	assert.EqualValues(t, 2, total)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/forward-rules/batch-delete", "", map[string]any{"ids": []int64{id}})
	assert.Equal(t, http.StatusOK, rec.Code)
	for _, path := range []string{"/telegram/admin/forward-rules/batch-delete", "/telegram/admin/forward-rules/batch-enable"} {
		rec = performJSON(t, router, http.MethodPost, path, "", map[string]any{})
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

func TestTelegramAdminReportsStorageFailuresAsErrors(t *testing.T) {
	gin := telegramBrokenRouter(t)
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/telegram/admin/bots", nil},
		{http.MethodPost, "/telegram/admin/bots", map[string]any{"name": "a", "token": testBotToken}},
		{http.MethodPut, "/telegram/admin/bots/1", map[string]any{"name": "a"}},
		{http.MethodPost, "/telegram/admin/bots/batch-delete", map[string]any{"ids": []int64{1}}},
		{http.MethodPost, "/telegram/admin/bots/batch-enable", map[string]any{"ids": []int64{1}, "enabled": true}},
		{http.MethodGet, "/telegram/admin/forward-rules", nil},
		{http.MethodPost, "/telegram/admin/forward-rules", map[string]any{"name": "a", "target_kind": "notify", "target_ref": "telegram", "target_recipient": "42"}},
		{http.MethodGet, "/telegram/admin/forward-channels", nil},
		{http.MethodPost, "/telegram/admin/forward-channels", map[string]any{"name": "a", "transport": "direct", "consumer_kind": "job", "consumer_ref": "x"}},
		{http.MethodPost, "/telegram/admin/forward-channels/batch-enable", map[string]any{"ids": []int64{1}, "enabled": true}},
		{http.MethodPost, "/telegram/admin/forward-channels/batch-delete", map[string]any{"ids": []int64{1}}},
		{http.MethodPost, "/telegram/admin/forward-rules/batch-delete", map[string]any{"ids": []int64{1}}},
		{http.MethodPost, "/telegram/admin/forward-rules/batch-enable", map[string]any{"ids": []int64{1}, "enabled": true}},
		{http.MethodPost, "/telegram/admin/bots/1/sync-commands", nil},
		{http.MethodGet, "/telegram/admin/client-accounts", nil},
		{http.MethodGet, "/telegram/admin/client-subscriptions", nil},
		{http.MethodGet, "/telegram/admin/client-messages", nil},
		{http.MethodPost, "/telegram/admin/client-accounts", map[string]any{"name": "a", "phone_number": "+1", "api_id": 1, "api_hash": "h"}},
		{http.MethodDelete, "/telegram/admin/client-accounts/1", nil},
		{http.MethodPut, "/telegram/admin/client-subscriptions/1", map[string]any{"keywords": []string{"x"}}},
		{http.MethodDelete, "/telegram/admin/client-subscriptions/1", nil},
	} {
		rec := performJSON(t, gin, call.method, call.path, "", call.body)
		assert.Equal(t, http.StatusInternalServerError, rec.Code, "%s %s: %s", call.method, call.path, rec.Body.String())
	}
}

func TestTelegramBotRoutesRefuseToQueueWithoutAnAgentQueue(t *testing.T) {
	router := telegramRouterWithoutQueue(t)
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/bots/1/sync-commands", "", nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCommandsAreBoundToABotAndFilteredByIt(t *testing.T) {
	db := telegramTestDB(t)
	bots := rdb.NewTelegramBotDAO(db)
	name, token := "alpha", testBotToken
	bot, err := bots.Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token})
	require.NoError(t, err)

	router := gin.New()
	group := router.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", []string{"ADMIN"}); c.Next() })
	(&WorkflowRouter{CommandDAO: rdb.NewBotCommandDAO(db), BotDAO: bots}).RegisterRoutes(group)

	create := func(body map[string]any) int {
		body["name"], body["action_type"], body["enabled"] = "n", "builtin", true
		return performJSON(t, router, http.MethodPost, "/telegram/admin/commands", "", body).Code
	}
	assert.Equal(t, http.StatusOK, create(map[string]any{"command": "/help", "bot_id": 0}))
	assert.Equal(t, http.StatusOK, create(map[string]any{"command": "/help", "bot_id": bot.ID}), "the same command on another bot")
	assert.Equal(t, http.StatusBadRequest, create(map[string]any{"command": "/ghost", "bot_id": 404}), "the bot must exist")
	assert.GreaterOrEqual(t, create(map[string]any{"command": "/help", "bot_id": bot.ID}), 400, "but only once per bot")

	rec := performJSON(t, router, http.MethodGet, fmt.Sprintf("/telegram/admin/commands?bot_id=%d", bot.ID), "", nil)
	list, total := decodeList[model.BotCommandDef](t, rec.Body.Bytes())
	require.EqualValues(t, 1, total)
	assert.Equal(t, bot.ID, list[0].BotID)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/commands?bot_id=0", "", nil)
	_, total = decodeList[model.BotCommandDef](t, rec.Body.Bytes())
	assert.EqualValues(t, 1, total, "bot_id=0 lists the shared commands")
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/commands", "", nil)
	_, total = decodeList[model.BotCommandDef](t, rec.Body.Bytes())
	assert.EqualValues(t, 2, total)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/commands?bot_id=x", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	id := list[0].ID
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/commands/%d", id), "", map[string]any{"command": "/help", "name": "n", "action_type": "builtin", "bot_id": 404})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/commands/%d", id), "", map[string]any{"command": "/help2", "name": "n", "action_type": "builtin", "bot_id": bot.ID, "enabled": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }
