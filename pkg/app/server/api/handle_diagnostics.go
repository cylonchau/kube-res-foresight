package api

import (
	"errors"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/cylonchau/kube-res-foresight/pkg/resultcache"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/gin-gonic/gin"
)

// DiagnosticsRouter serves what an administrator needs to tell why a page is slow or a request ends in a 502 or 504: the pool of database
// connections of this process, the size of the tables that grow, the use of their indexes, the statements the database is running and which module
// writes the most (Database); and the baseline of the time of every route, where that time goes, the runs of the process and what the numbers say
// (Requests). It reads the statistics the database keeps, never the rows, and holds nothing secret.
type DiagnosticsRouter struct {
	Diagnostics *rdb.DiagnosticsDAO
	Config      *rdb.SysConfigDAO
}

// Database serves GET /diagnostics/database.
// @Summary Database diagnostics
// @Tags Diagnostics
// @Router /api/v1/diagnostics/database [get]
func (r *DiagnosticsRouter) Database(c *gin.Context) {
	report, err := r.Diagnostics.Report(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, report)
}

// Requests serves GET /diagnostics/requests?hours=24: the baseline of the time of each route over the hours (as every process of the platform saw
// it), the layer the time is in (database, Prometheus or the platform's own work), the runs of the process with how each ended, and what the numbers
// say about the 502 and 504 of the gateway.
// @Summary Request baseline and diagnosis
// @Tags Diagnostics
// @Router /api/v1/diagnostics/requests [get]
func (r *DiagnosticsRouter) Requests(c *gin.Context) {
	svc := reqstat.Default()
	if svc == nil {
		query.InternalError(c, errors.New("the request statistics are not running in this process"))
		return
	}
	hours, _ := strconv.Atoi(c.Query("hours"))
	ctx := c.Request.Context()
	seconds := func(p timing.Param) time.Duration {
		raw := ""
		if r.Config != nil {
			raw, _ = r.Config.GetValueOrDefault(ctx, p.Key, "")
		}
		return time.Duration(p.Parse(raw)) * time.Second
	}
	lim := reqstat.Limits{Gateway: seconds(timing.HTTPGatewayTimeout), ListBudget: timing.Under(seconds(timing.HTTPGatewayTimeout), seconds(timing.ListQuery))}
	if r.Diagnostics != nil {
		pool := r.Diagnostics.Pool()
		lim.PoolWaitCount, lim.PoolWaitMs, lim.PoolMaxOpen = pool.WaitCount, pool.WaitMillis, pool.MaxOpen
	}
	report, err := svc.Read(ctx, hours, lim)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, RequestDiagnostics{Report: report, Cache: resultcache.Default().Stats(), Reuse: forecastsvc.Reuse()})
}

// RequestDiagnostics is the baseline of the routes with what the platform saved by not working things out again (P11 B3): the hit rate of the cache of the heavy
// reports, and how many forecasts were made and how many were not made again because the last one stood for them. The counters are those of this process since it started.
type RequestDiagnostics struct {
	*reqstat.Report
	Cache resultcache.Stats      `json:"cache"`
	Reuse forecastsvc.ReuseStats `json:"reuse"`
}
