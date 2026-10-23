package api

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/gin-gonic/gin"
	"time"
)

type OperationLogRouter struct {
	DAO *rdb.OperationLogDAO
}

// operationLogListReq mirrors the "系统管理/操作日志" page's filter bar:
// source (client_type), a free-text keyword (via ListOptions), success/
// failure, and a created-at range.
type operationLogListReq struct {
	apis.ListOptions
	ClientType string `form:"client_type"`
	Status     string `form:"status"` // "success" | "failed"
	StartTime  string `form:"start_time"`
	EndTime    string `form:"end_time"`
}

// List returns paginated operation log entries, filterable by source
// (page/tma/api), keyword (username/path/client IP), success/failure, and a
// created-at range.
// @Summary List operation logs
// @Description Retrieve a filterable, paginated history of mutating admin/API requests.
// @Tags System
// @Accept json
// @Produce json
// @Param query query operationLogListReq true "Query parameters"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/operation-logs [get]
func (h *OperationLogRouter) List(c *gin.Context) {
	var req operationLogListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	offset := (req.Page - 1) * req.PageSize
	filter := rdb.OperationLogListFilter{
		ClientType: req.ClientType,
		Keyword:    req.Keyword,
		Offset:     int(offset),
		Limit:      int(req.PageSize),
	}
	switch req.Status {
	case "success":
		success := true
		filter.Success = &success
	case "failed":
		failed := false
		filter.Success = &failed
	}
	if t, err := time.Parse("2006-01-02 15:04:05", req.StartTime); err == nil {
		filter.Since = &t
	} else if t, err := time.Parse(time.RFC3339, req.StartTime); err == nil {
		filter.Since = &t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", req.EndTime); err == nil {
		filter.Until = &t
	} else if t, err := time.Parse(time.RFC3339, req.EndTime); err == nil {
		filter.Until = &t
	}

	logs, total, err := h.DAO.List(context.Background(), filter)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{
		"list":     logs,
		"total":    total,
		"page":     req.Page,
		"pageSize": req.PageSize,
	})
}

// Clear deletes every operation log row.
// @Summary Clear operation logs
// @Description Permanently deletes all operation log entries.
// @Tags System
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/operation-logs [delete]
func (h *OperationLogRouter) Clear(c *gin.Context) {
	if err := h.DAO.DeleteAll(context.Background()); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}
