package api

import (
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/gin-gonic/gin"
	"strconv"
)

type NotifyRouter struct {
	DAO *rdb.NotificationDAO
}

func (router *NotifyRouter) ListDeliveries(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	channel := c.Query("channel")
	severity := c.Query("severity")
	status := c.Query("status")
	keyword := c.Query("keyword")

	if router.DAO == nil {
		query.SuccessResponse(c, nil, gin.H{"list": []interface{}{}, "total": 0})
		return
	}

	rows, total, err := router.DAO.ListDeliveriesPaginated(c.Request.Context(), page, pageSize, channel, severity, status, keyword)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (router *NotifyRouter) AckDelivery(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		query.BadRequest(c, errors.New("notification id is required"))
		return
	}
	engine := notify.GetEngine()
	if engine != nil {
		if err := engine.Acknowledge(c.Request.Context(), id); err != nil {
			query.InternalError(c, err)
			return
		}
	} else if router.DAO != nil {
		if err := router.DAO.AckNotification(c.Request.Context(), id); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	query.SuccessResponse(c, nil, gin.H{"acknowledged": true, "notification_id": id})
}

// BatchAck acknowledges several deliveries at once: the listed notification ids, or with "all" every
// unacknowledged delivery matching the channel, severity and keyword given, on every page.
func (router *NotifyRouter) BatchAck(c *gin.Context) {
	var req struct {
		NotificationIDs []string `json:"notification_ids"`
		All             bool     `json:"all"`
		Channel         string   `json:"channel"`
		Severity        string   `json:"severity"`
		Keyword         string   `json:"keyword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || router.DAO == nil || (!req.All && (len(req.NotificationIDs) == 0 || len(req.NotificationIDs) > 500)) {
		query.BadRequest(c, errors.New("between 1 and 500 notification ids, or all=true, are required"))
		return
	}
	var n int64
	var err error
	if req.All {
		n, err = router.DAO.AckMatching(c.Request.Context(), req.Channel, req.Severity, req.Keyword)
	} else {
		n, err = router.DAO.AckMany(c.Request.Context(), req.NotificationIDs)
	}
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"acknowledged": n})
}

func (router *NotifyRouter) GetStats(c *gin.Context) {
	if router.DAO == nil {
		query.SuccessResponse(c, nil, &rdb.NotificationStats{})
		return
	}
	stats, err := router.DAO.GetStats(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, stats)
}
