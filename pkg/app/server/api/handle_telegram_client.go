package api

import (
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

// agentWait is how long an admin request waits for the Telegram Agent, the
// only process that talks to Telegram, to execute a queued action.
const agentWait = 45 * time.Second

// TelegramClientRouter manages client accounts and channel subscriptions.
// Storage is handled here; every action that needs Telegram (login, join,
// leave) is queued for the Telegram Agent through Requests.
type TelegramClientRouter struct {
	DAO      *rdb.TelegramClientDAO
	Requests *rdb.TelegramAgentRequestDAO
	// Wait overrides agentWait (tests).
	Wait time.Duration
}

// callAgent queues a request and waits for the Telegram Agent's answer. On
// failure it has already written the error response.
func (r *TelegramClientRouter) callAgent(c *gin.Context, kind string, targetID int64, payload any) (*model.TelegramAgentRequest, bool) {
	return callTelegramAgent(c, r.Requests, r.Wait, kind, targetID, payload)
}

func callTelegramAgent(c *gin.Context, requests *rdb.TelegramAgentRequestDAO, wait time.Duration, kind string, targetID int64, payload any) (*model.TelegramAgentRequest, bool) {
	if wait <= 0 {
		wait = agentWait
	}
	req, err := requests.Enqueue(c.Request.Context(), kind, targetID, payload)
	if err != nil {
		query.InternalError(c, err)
		return nil, false
	}
	done, err := requests.Wait(c.Request.Context(), req.ID, wait)
	if err != nil {
		query.BadRequest(c, err)
		return nil, false
	}
	return done, true
}

func (r *TelegramClientRouter) RegisterRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin")
	admin.GET("/client-accounts", r.ListAccounts)
	admin.POST("/client-accounts", r.CreateAccount)
	admin.PUT("/client-accounts/:id", r.UpdateAccount)
	admin.DELETE("/client-accounts/:id", r.DeleteAccount)
	admin.POST("/client-accounts/send-code", r.SendCode)
	admin.POST("/client-accounts/verify-code", r.VerifyCode)
	admin.POST("/client-accounts/:id/connect", r.ConnectAccount)
	admin.POST("/client-accounts/:id/logout", r.LogoutAccount)
	admin.GET("/client-subscriptions", r.ListSubscriptions)
	admin.POST("/client-subscriptions", r.CreateSubscription)
	admin.PUT("/client-subscriptions/:id", r.UpdateSubscription)
	admin.POST("/client-subscriptions/:id/leave", r.LeaveSubscription)
	admin.DELETE("/client-subscriptions/:id", r.DeleteSubscription)
	admin.POST("/client-subscriptions/batch-enable", r.EnableSubscriptions)
	admin.GET("/client-messages", r.ListMessages)
	admin.GET("/client-messages/:id", r.GetMessage)
	admin.POST("/client-messages/batch", r.BatchMessages)
}

func requireTelegramClientAdmin(c *gin.Context) bool {
	if hasAnyRole(c, "ADMIN") {
		return true
	}
	query.Forbidden(c, query.ErrNoPermission)
	return false
}

func parseTelegramClientID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		query.BadRequest(c, errors.New("invalid id"))
		return 0, false
	}
	return id, true
}

func (r *TelegramClientRouter) ListAccounts(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientAccountListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.DAO.ListAccounts(c.Request.Context(), &req)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (r *TelegramClientRouter) CreateAccount(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientAccountCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.DAO.CreateAccount(c.Request.Context(), &req)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramClientRouter) UpdateAccount(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	var req apis.TelegramClientAccountUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	// The Telegram Agent reconciles sessions from the stored state: changed
	// credentials clear the session and a disabled account is disconnected.
	row, err := r.DAO.UpdateAccount(c.Request.Context(), id, &req)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramClientRouter) DeleteAccount(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if err := r.DAO.DeleteAccount(c.Request.Context(), id); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *TelegramClientRouter) SendCode(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientSendCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if _, ok := r.callAgent(c, model.AgentReqSendCode, req.AccountID, nil); !ok {
		return
	}
	query.SuccessResponse(c, nil, gin.H{"code_sent": true, "expires_in": 600})
}

func (r *TelegramClientRouter) VerifyCode(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientVerifyCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if _, ok := r.callAgent(c, model.AgentReqVerifyCode, req.AccountID, gin.H{"code": req.Code, "password": req.Password}); !ok {
		return
	}
	query.SuccessResponse(c, nil, gin.H{"authorized": true, "connected": true})
}

func (r *TelegramClientRouter) ConnectAccount(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if _, ok := r.callAgent(c, model.AgentReqConnect, id, nil); !ok {
		return
	}
	query.SuccessResponse(c, nil, gin.H{"connected": true})
}

func (r *TelegramClientRouter) LogoutAccount(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if _, ok := r.callAgent(c, model.AgentReqLogout, id, nil); !ok {
		return
	}
	query.SuccessResponse(c, nil, gin.H{"authorized": false, "connected": false})
}

func (r *TelegramClientRouter) ListSubscriptions(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientSubscriptionListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.DAO.ListSubscriptions(c.Request.Context(), &req)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (r *TelegramClientRouter) CreateSubscription(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramClientSubscriptionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	link := strings.TrimSpace(req.ChannelLink)
	switch {
	case req.AccountID <= 0 && link == "":
		query.BadRequest(c, errors.New("select an account and enter the channel link"))
		return
	case req.AccountID <= 0:
		query.BadRequest(c, errors.New("select an account"))
		return
	case link == "":
		query.BadRequest(c, errors.New("enter the channel link"))
		return
	}
	done, ok := r.callAgent(c, model.AgentReqSubscribe, req.AccountID, gin.H{
		"account_id": req.AccountID, "channel_link": link, "join": req.Join,
	})
	if !ok {
		return
	}
	var row model.TelegramClientSubscription
	if err := json.Unmarshal([]byte(done.ResultJSON), &row); err != nil || row.ID == 0 {
		query.InternalError(c, errors.New("the Telegram Agent returned no subscription"))
		return
	}
	query.SuccessResponse(c, nil, row)
}

// LeaveSubscription makes the subscription's account leave the channel and
// disables the subscription.
func (r *TelegramClientRouter) LeaveSubscription(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if _, ok := r.callAgent(c, model.AgentReqUnsubscribe, id, nil); !ok {
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *TelegramClientRouter) UpdateSubscription(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	var req apis.TelegramClientSubscriptionUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	clean := make([]string, 0, len(req.Keywords))
	for _, keyword := range req.Keywords {
		if keyword = strings.TrimSpace(keyword); keyword != "" {
			clean = append(clean, keyword)
		}
	}
	raw, _ := json.Marshal(clean)
	if err := r.DAO.UpdateSubscription(c.Request.Context(), id, string(raw), req.Enabled); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *TelegramClientRouter) DeleteSubscription(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if err := r.DAO.DeleteSubscription(c.Request.Context(), id); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *TelegramClientRouter) ListMessages(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	var req apis.TelegramClientMessageListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.DAO.ListMessages(c.Request.Context(), &req)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

// EnableSubscriptions switches several subscriptions on or off.
func (r *TelegramClientRouter) EnableSubscriptions(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBatchEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.DAO.SetSubscriptionsEnabled(c.Request.Context(), req.IDs, req.Enabled)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"updated": n})
}

// GetMessage returns one cached message with its full text. Lists do not carry
// the text, so it is read only when someone asks to see it.
func (r *TelegramClientRouter) GetMessage(c *gin.Context) {
	if !hasAnyRole(c, "ADMIN", "OPS") {
		query.Forbidden(c, query.ErrNoPermission)
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	row, err := r.DAO.GetMessage(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

// BatchMessages applies one action to cached messages.
func (r *TelegramClientRouter) BatchMessages(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramMessageBatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	var n int64
	var err error
	switch req.Action {
	case "delete":
		n, err = r.DAO.DeleteMessages(ctx, req.IDs)
	case "retry":
		n, err = r.DAO.RetryMessages(ctx, req.IDs)
	case "consume":
		n, err = r.DAO.ConsumeMessages(ctx, req.IDs, time.Now())
	}
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"affected": n})
}
