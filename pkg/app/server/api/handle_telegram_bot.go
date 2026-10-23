package api

import (
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"time"
)

// TelegramBotRouter manages Telegram bots and forward rules. It only stores
// configuration; the Telegram Agent runs the bots and pushes their commands.
type TelegramBotRouter struct {
	Bots     *rdb.TelegramBotDAO
	Rules    *rdb.TelegramForwardRuleDAO
	Channels *rdb.TelegramForwardChannelDAO
	Requests *rdb.TelegramAgentRequestDAO
	Wait     time.Duration
}

// telegramError answers a rejected value with 400 and anything else like a
// storage error.
func telegramError(c *gin.Context, err error) {
	var invalid rdb.InvalidInputError
	if errors.As(err, &invalid) {
		query.BadRequest(c, err)
		return
	}
	query.HandleDBError(c, err)
}

func (r *TelegramBotRouter) RegisterRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin")
	admin.GET("/bots", r.ListBots)
	admin.POST("/bots", r.CreateBot)
	admin.PUT("/bots/:id", r.UpdateBot)
	admin.POST("/bots/batch-delete", r.DeleteBots)
	admin.POST("/bots/batch-enable", r.EnableBots)
	admin.POST("/bots/:id/sync-commands", r.SyncCommands)
	admin.GET("/forward-rules", r.ListRules)
	admin.POST("/forward-rules", r.CreateRule)
	admin.PUT("/forward-rules/:id", r.UpdateRule)
	admin.POST("/forward-rules/batch-delete", r.DeleteRules)
	admin.POST("/forward-rules/batch-enable", r.EnableRules)
	admin.GET("/forward-channels", r.ListChannels)
	admin.POST("/forward-channels", r.CreateChannel)
	admin.PUT("/forward-channels/:id", r.UpdateChannel)
	admin.POST("/forward-channels/batch-delete", r.DeleteChannels)
	admin.POST("/forward-channels/batch-enable", r.EnableChannels)
	admin.POST("/forward-channels/:id/test", r.TestChannel)
}

func (r *TelegramBotRouter) ListBots(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBotListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.Bots.List(c.Request.Context(), int(req.Page), int(req.PageSize), rdb.TelegramBotFilter{Keyword: req.Keyword, Enabled: req.Enabled, Status: req.Status})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	counts := r.Bots.CommandCounts(c.Request.Context(), rows)
	for i := range rows {
		rows[i].CommandCount = counts[rows[i].ID]
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (r *TelegramBotRouter) CreateBot(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBotCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Bots.Create(c.Request.Context(), rdb.TelegramBotInput{
		Name: &req.Name, Token: &req.Token, Username: req.Username, Mode: req.Mode,
		APIBaseURL: req.APIBaseURL, MiniAppURL: req.MiniAppURL, Enabled: req.Enabled,
	})
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) UpdateBot(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	var req apis.TelegramBotUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Bots.Update(c.Request.Context(), id, rdb.TelegramBotInput{
		Name: req.Name, Token: req.Token, Username: req.Username, Mode: req.Mode,
		APIBaseURL: req.APIBaseURL, MiniAppURL: req.MiniAppURL, Enabled: req.Enabled,
	})
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) DeleteBots(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Bots.Delete(c.Request.Context(), req.IDs)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": n})
}

func (r *TelegramBotRouter) EnableBots(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBatchEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Bots.SetEnabled(c.Request.Context(), req.IDs, req.Enabled)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"updated": n})
}

// SyncCommands asks the Telegram Agent to push the command menu of a bot to
// Telegram now instead of at its next reconcile.
func (r *TelegramBotRouter) SyncCommands(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	if r.Requests == nil {
		query.InternalError(c, errors.New("the Telegram Agent queue is not available"))
		return
	}
	if _, ok := callTelegramAgent(c, r.Requests, r.Wait, model.AgentReqSyncBotCommand, id, nil); !ok {
		return
	}
	query.SuccessResponse(c, nil, gin.H{"synced": true})
}

func (r *TelegramBotRouter) ListRules(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramForwardRuleListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rows, total, err := r.Rules.List(c.Request.Context(), int(req.Page), int(req.PageSize), rdb.TelegramForwardRuleFilter{
		Keyword: req.Keyword, AccountID: req.AccountID, SubscriptionID: req.SubscriptionID, SourceType: req.SourceType,
		TargetClass: req.TargetClass, TargetKind: req.TargetKind, Enabled: req.Enabled,
	})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if r.Channels != nil {
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			if row.ChannelID > 0 {
				ids = append(ids, row.ChannelID)
			}
		}
		if names, err := r.Channels.Many(c.Request.Context(), ids); err == nil {
			for i := range rows {
				rows[i].ChannelName = names[rows[i].ChannelID].Name
			}
		}
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func ruleFrom(req apis.TelegramForwardRuleReq) model.TelegramForwardRule {
	row := model.TelegramForwardRule{
		Name: req.Name, AccountID: req.AccountID, SourceType: req.SourceType, SubscriptionID: req.SubscriptionID, Pattern: req.Pattern, TargetKind: req.TargetKind,
		TargetRef: req.TargetRef, TargetRecipient: req.TargetRecipient, ChannelID: req.ChannelID, SendAccountID: req.SendAccountID, Enabled: true, Remark: req.Remark,
	}
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	return row
}

func (r *TelegramBotRouter) CreateRule(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramForwardRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row := ruleFrom(req)
	if err := r.Rules.Create(c.Request.Context(), &row); err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) UpdateRule(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	id, ok := parseTelegramClientID(c)
	if !ok {
		return
	}
	var req apis.TelegramForwardRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	row, err := r.Rules.Update(c.Request.Context(), id, ruleFrom(req))
	if err != nil {
		telegramError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

func (r *TelegramBotRouter) DeleteRules(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Rules.Delete(c.Request.Context(), req.IDs)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": n})
}

func (r *TelegramBotRouter) EnableRules(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramBatchEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Rules.SetEnabled(c.Request.Context(), req.IDs, req.Enabled)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"updated": n})
}
