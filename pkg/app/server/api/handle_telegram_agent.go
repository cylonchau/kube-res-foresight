package api

import (
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"time"
)

// TelegramAgentRouter shows what the TG Agents report. It only reads: the
// agents write their own heartbeat.
type TelegramAgentRouter struct {
	Agents   *rdb.TelegramAgentStatusDAO
	Accounts *rdb.TelegramClientDAO
	Bots     *rdb.TelegramBotDAO
}

func (r *TelegramAgentRouter) RegisterRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin")
	admin.GET("/agents", r.ListAgents)
	admin.POST("/agents/batch-delete", r.DeleteAgents)
}

// agentSummary is what the page shows above the agent list: the message cache
// and how many accounts and bots are up.
type agentSummary struct {
	Cache    map[string]int64 `json:"cache"`
	Accounts map[string]int64 `json:"accounts"`
	Bots     map[string]int64 `json:"bots"`
}

func (r *TelegramAgentRouter) ListAgents(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramAgentListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	rows, total, err := r.Agents.List(ctx, int(req.Page), int(req.PageSize), rdb.TelegramAgentFilter{Keyword: req.Keyword, Online: req.Online})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	summary := agentSummary{Accounts: map[string]int64{"total": 0, "connected": 0, "error": 0, "send_blocked": 0}, Bots: map[string]int64{"total": 0, "running": 0, "error": 0}}
	if summary.Cache, err = r.Accounts.MessageStats(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	accounts, accountTotal, err := r.Accounts.ListAccounts(ctx, &apis.TelegramClientAccountListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 500}})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	summary.Accounts["total"] = accountTotal
	for _, a := range accounts {
		if a.Connected {
			summary.Accounts["connected"]++
		}
		if a.ConnectionStatus == model.TelegramClientError {
			summary.Accounts["error"]++
		}
		if a.SendBlocked(time.Now()) {
			summary.Accounts["send_blocked"]++
		}
	}
	bots, botTotal, err := r.Bots.List(ctx, 1, 200, rdb.TelegramBotFilter{})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	summary.Bots["total"] = botTotal
	for _, b := range bots {
		if b.Connected {
			summary.Bots["running"]++
		}
		if b.Status == model.TelegramBotError {
			summary.Bots["error"]++
		}
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total, "summary": summary, "now": time.Now()})
}

// DeleteAgents removes the heartbeat rows of agents that no longer exist.
func (r *TelegramAgentRouter) DeleteAgents(c *gin.Context) {
	if !requireTelegramClientAdmin(c) {
		return
	}
	var req apis.TelegramIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	n, err := r.Agents.Delete(c.Request.Context(), req.IDs)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"deleted": n})
}
