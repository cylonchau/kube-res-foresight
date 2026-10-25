package api

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
)

// LoginLogRouter serves the login history page.
type LoginLogRouter struct {
	DAO *rdb.LoginAuditLogDAO
}

// loginLogListReq mirrors the login-log page's filter bar. Paging, ordering and filtering are done in the
// database; sort_by is one of created_at, username, client_ip, success, reason, method.
type loginLogListReq struct {
	apis.ListOptions
	Username  string `form:"username"`
	ClientIP  string `form:"client_ip"`
	Reason    string `form:"reason"`
	Method    string `form:"method"`
	Result    string `form:"result"` // "success" | "failed"
	StartTime string `form:"start_time"`
	EndTime   string `form:"end_time"`
	SortBy    string `form:"sort_by"`
	SortOrder string `form:"sort_order"` // "asc" | "desc"
}

func parseLogTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return &t
		}
	}
	return nil
}

// List returns the login history, newest first unless a sort column is given.
// @Summary List login logs
// @Tags System
// @Produce json
// @Param query query loginLogListReq true "Query parameters"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/login-logs [get]
func (h *LoginLogRouter) List(c *gin.Context) {
	var req loginLogListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	filter := rdb.LoginAuditListFilter{
		Username: req.Username, ClientIP: req.ClientIP, Reason: req.Reason, Method: req.Method,
		Since: parseLogTime(req.StartTime), Until: parseLogTime(req.EndTime),
		SortBy: req.SortBy, SortDesc: strings.EqualFold(req.SortOrder, "desc"),
		Offset: int((req.Page - 1) * req.PageSize), Limit: int(req.PageSize),
	}
	switch req.Result {
	case "success":
		ok := true
		filter.Success = &ok
	case "failed":
		failed := false
		filter.Success = &failed
	}
	logs, total, err := h.DAO.List(c.Request.Context(), filter)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": logs, "total": total, "page": req.Page, "pageSize": req.PageSize})
}
