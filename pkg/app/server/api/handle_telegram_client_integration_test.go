package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"testing"
	"time"
)

func telegramTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.TelegramClientAccount{}, &model.TelegramClientSubscription{}, &model.TelegramClientMessage{},
		&model.TelegramBot{}, &model.TelegramAgentRequest{}, &model.TelegramForwardRule{}, &model.TelegramForwardChannel{}, &model.WorkflowDef{}, &model.BotCommandDef{}, &model.TelegramAgentStatus{},
	))
	return db
}

// fakeAgent answers queued Telegram Agent requests the way the real agent
// would, so the API can be tested without Telegram.
func fakeAgent(t *testing.T, db *gorm.DB, handle func(req *model.TelegramAgentRequest, dao *rdb.TelegramAgentRequestDAO) (any, error)) {
	t.Helper()
	dao := rdb.NewTelegramAgentRequestDAO(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			req, err := dao.Claim(ctx)
			if err != nil || req == nil {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			result, runErr := handle(req, dao)
			_ = dao.Finish(ctx, req.ID, result, runErr)
		}
	}()
}

func telegramClientAPIRouter(t *testing.T, roles []string, wait time.Duration) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := telegramTestDB(t)
	router := gin.New()
	group := router.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", roles); c.Next() })
	requests := rdb.NewTelegramAgentRequestDAO(db)
	(&TelegramClientRouter{DAO: rdb.NewTelegramClientDAO(db), Requests: requests, Wait: wait}).RegisterRoutes(group)
	(&TelegramBotRouter{Bots: rdb.NewTelegramBotDAO(db), Rules: rdb.NewTelegramForwardRuleDAO(db), Channels: rdb.NewTelegramForwardChannelDAO(db), Requests: requests, Wait: wait}).RegisterRoutes(group)
	(&TelegramAgentRouter{Agents: rdb.NewTelegramAgentStatusDAO(db), Accounts: rdb.NewTelegramClientDAO(db), Bots: rdb.NewTelegramBotDAO(db)}).RegisterRoutes(group)
	return router, db
}

func TestTelegramClientAdminAPIStorageFlow(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, 2*time.Second)
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/client-accounts", "", map[string]any{"name": "reader", "phone_number": "+12025550100", "api_id": 12345, "api_hash": "hash", "enabled": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var account model.TelegramClientAccount
	require.NoError(t, db.Where("name = ?", "reader").First(&account).Error)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/client-accounts?page=1&page_size=10&keyword=read", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/client-accounts/%d", account.ID), "", map[string]any{"name": "reader-updated", "enabled": false})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sub := model.TelegramClientSubscription{AccountID: account.ID, PeerID: 7, PeerType: "channel", Username: "news", Title: "News", Enabled: true}
	require.NoError(t, db.Create(&sub).Error)
	msg := model.TelegramClientMessage{AccountID: account.ID, SubscriptionID: sub.ID, PeerID: 7, TelegramMessageID: 1, SenderName: "Alice", Text: "BTC", MessageAt: time.Now()}
	require.NoError(t, db.Create(&msg).Error)
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/client-subscriptions?page=1&page_size=10&account_id="+fmt.Sprint(account.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/telegram/admin/client-subscriptions/%d", sub.ID), "", map[string]any{"keywords": []string{" BTC ", "", "ETH"}, "enabled": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/client-messages?page=1&page_size=10&keyword=BTC", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/telegram/admin/client-subscriptions/%d", sub.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/telegram/admin/client-accounts/%d", account.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestTelegramClientActionsAreExecutedByTheAgent(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, 3*time.Second)
	seen := make(chan string, 16)
	fakeAgent(t, db, func(req *model.TelegramAgentRequest, dao *rdb.TelegramAgentRequestDAO) (any, error) {
		seen <- req.Kind
		switch req.Kind {
		case model.AgentReqVerifyCode:
			var payload struct{ Code, Password string }
			require.NoError(t, dao.Payload(req, &payload))
			if payload.Password == "" {
				return nil, errors.New("telegram two-step verification password is required")
			}
			return nil, nil
		case model.AgentReqSubscribe:
			row := model.TelegramClientSubscription{AccountID: req.TargetID, PeerID: 9, PeerType: "channel", Username: "news", Enabled: true, State: model.TelegramSubJoined}
			require.NoError(t, db.Create(&row).Error)
			return row, nil
		}
		return nil, nil
	})

	for _, step := range []struct{ path, kind string }{
		{"/telegram/admin/client-accounts/send-code", model.AgentReqSendCode},
		{"/telegram/admin/client-accounts/7/connect", model.AgentReqConnect},
		{"/telegram/admin/client-accounts/7/logout", model.AgentReqLogout},
	} {
		body := map[string]any{"account_id": 7}
		rec := performJSON(t, router, http.MethodPost, step.path, "", body)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, step.kind, <-seen)
	}

	// The two-step password is carried to the agent and its refusal reaches the caller.
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/client-accounts/verify-code", "", map[string]any{"account_id": 7, "code": "12345"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "two-step verification password is required")
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/client-accounts/verify-code", "", map[string]any{"account_id": 7, "code": "12345", "password": "pw"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/client-subscriptions", "", map[string]any{"account_id": 7, "channel_link": "https://t.me/news", "join": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Data model.TelegramClientSubscription
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, model.TelegramSubJoined, created.Data.State)

	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/telegram/admin/client-subscriptions/%d/leave", created.Data.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Nothing the agent was given stays in the database.
	var leftover int64
	db.Model(&model.TelegramAgentRequest{}).Where("payload_encrypted <> ''").Count(&leftover)
	assert.Zero(t, leftover)
}

func TestTelegramClientActionsFailClearlyWithoutAnAgent(t *testing.T) {
	router, db := telegramClientAPIRouter(t, []string{"ADMIN"}, 300*time.Millisecond)
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/client-accounts/send-code", "", map[string]any{"account_id": 1})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Telegram Agent")
	var pending int64
	db.Model(&model.TelegramAgentRequest{}).Where("status = ?", model.AgentReqPending).Count(&pending)
	assert.Zero(t, pending, "an unanswered request is cancelled so it cannot run later")
}

func TestTelegramClientAdminAPIErrorsAndRoles(t *testing.T) {
	router, _ := telegramClientAPIRouter(t, []string{"ADMIN"}, 200*time.Millisecond)
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/telegram/admin/client-accounts", map[string]any{}},
		{http.MethodPut, "/telegram/admin/client-accounts/nope", map[string]any{}},
		{http.MethodPost, "/telegram/admin/client-accounts/send-code", map[string]any{"account_id": 999}},
		{http.MethodPost, "/telegram/admin/client-accounts/verify-code", map[string]any{"account_id": 999, "code": "1"}},
		{http.MethodPost, "/telegram/admin/client-accounts/999/connect", nil},
		{http.MethodPost, "/telegram/admin/client-accounts/999/logout", nil},
		{http.MethodPost, "/telegram/admin/client-accounts/nope/connect", nil},
		{http.MethodPost, "/telegram/admin/client-subscriptions", map[string]any{"account_id": 999, "channel_link": "news"}},
		{http.MethodPost, "/telegram/admin/client-subscriptions", map[string]any{}},
		{http.MethodPost, "/telegram/admin/client-subscriptions/nope/leave", nil},
		{http.MethodPut, "/telegram/admin/client-subscriptions/nope", map[string]any{}},
	}
	for _, tc := range cases {
		rec := performJSON(t, router, tc.method, tc.path, "", tc.body)
		assert.GreaterOrEqual(t, rec.Code, 400, "%s %s: %s", tc.method, tc.path, rec.Body.String())
	}

	denied, _ := telegramClientAPIRouter(t, []string{"VIEWER"}, time.Second)
	for _, path := range []string{"/telegram/admin/client-accounts", "/telegram/admin/client-messages", "/telegram/admin/bots", "/telegram/admin/forward-rules"} {
		rec := performJSON(t, denied, http.MethodGet, path, "", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, path)
	}
}

// telegramBrokenRouter serves the Telegram admin API over a database that has
// none of its tables, so every storage call fails.
func telegramBrokenRouter(t *testing.T) *gin.Engine {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-broken?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	router := gin.New()
	group := router.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", []string{"ADMIN"}); c.Next() })
	requests := rdb.NewTelegramAgentRequestDAO(db)
	(&TelegramClientRouter{DAO: rdb.NewTelegramClientDAO(db), Requests: requests, Wait: 100 * time.Millisecond}).RegisterRoutes(group)
	(&TelegramBotRouter{Bots: rdb.NewTelegramBotDAO(db), Rules: rdb.NewTelegramForwardRuleDAO(db), Channels: rdb.NewTelegramForwardChannelDAO(db), Requests: requests, Wait: 100 * time.Millisecond}).RegisterRoutes(group)
	(&TelegramAgentRouter{Agents: rdb.NewTelegramAgentStatusDAO(db), Accounts: rdb.NewTelegramClientDAO(db), Bots: rdb.NewTelegramBotDAO(db)}).RegisterRoutes(group)
	return router
}

func telegramRouterWithoutQueue(t *testing.T) *gin.Engine {
	t.Helper()
	db := telegramTestDB(t)
	router := gin.New()
	group := router.Group("/telegram")
	group.Use(func(c *gin.Context) { c.Set("roles", []string{"ADMIN"}); c.Next() })
	(&TelegramBotRouter{Bots: rdb.NewTelegramBotDAO(db), Rules: rdb.NewTelegramForwardRuleDAO(db), Channels: rdb.NewTelegramForwardChannelDAO(db)}).RegisterRoutes(group)
	return router
}

func TestJoiningAChannelNeedsAnAccountAndAChannelLink(t *testing.T) {
	router, _ := telegramClientAPIRouter(t, []string{"ADMIN"}, 200*time.Millisecond)
	for body, want := range map[string]string{
		`{}`:                                  "select an account and enter the channel link",
		`{"channel_link":"https://t.me/abc"}`: "select an account",
		`{"account_id":3}`:                    "enter the channel link",
		`{"account_id":3,"channel_link":" "}`: "enter the channel link",
	} {
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &payload))
		rec := performJSON(t, router, http.MethodPost, "/telegram/admin/client-subscriptions", "", payload)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
		assert.Contains(t, rec.Body.String(), want, body)
	}
}

// makeChannel creates a forward channel through the API and returns its id.
func makeChannel(t *testing.T, router http.Handler, body map[string]any) int64 {
	t.Helper()
	rec := performJSON(t, router, http.MethodPost, "/telegram/admin/forward-channels", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Data model.TelegramForwardChannel `json:"data"`
	}
	decode(t, rec.Body.Bytes(), &out)
	return out.Data.ID
}

func jobChannel(t *testing.T, router http.Handler, name string) int64 {
	return makeChannel(t, router, map[string]any{"name": name, "transport": "direct", "consumer_kind": "job", "consumer_ref": "llm_rating"})
}
