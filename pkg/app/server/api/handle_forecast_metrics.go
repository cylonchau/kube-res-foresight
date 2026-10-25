package api

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/forecastsvc"
)

// MetricCatalog lists every queryable forecasting metric with its unit, labels, source table and retention.
// @Summary Forecast metric catalog
// @Tags Forecast
// @Router /api/v1/forecast-metrics/catalog [get]
func (r *ForecastRouter) MetricCatalog(c *gin.Context) {
	query.SuccessResponse(c, nil, gin.H{"list": forecastsvc.Catalog()})
}

// QueryMetrics returns one catalog metric of a module as time series on a step grid.
// @Summary Query a forecast metric
// @Tags Forecast
// @Param name query string true "metric name from the catalog"
// @Param module_id query int true "module"
// @Param metric query string false "forecast metric kind: cpu, memory or rps"
// @Param algorithm query string false "algorithm filter"
// @Param from query string false "RFC3339 start"
// @Param to query string false "RFC3339 end"
// @Param step query int false "bucket width in seconds"
// @Param max_points query int false "points per series, at most 5000"
// @Router /api/v1/forecast-metrics/query [get]
func (r *ForecastRouter) QueryMetrics(c *gin.Context) {
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	p := forecastsvc.QueryParams{Name: c.Query("name"), ModuleID: uint(moduleID), Metric: c.Query("metric"), Algorithm: c.Query("algorithm")}
	if t := timeQuery(c, "from"); t != nil {
		p.From = *t
	}
	if t := timeQuery(c, "to"); t != nil {
		p.To = *t
	}
	if raw := c.Query("step"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 {
			query.BadRequest(c, fmt.Errorf("step must be a number of seconds"))
			return
		}
		p.Step = time.Duration(seconds) * time.Second
	}
	if raw := c.Query("max_points"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 {
			query.BadRequest(c, fmt.Errorf("max_points must be at least 2"))
			return
		}
		p.MaxPoints = n
	}
	// the module named by the request must exist
	if p.ModuleID != 0 {
		if _, err := r.Modules.Get(c.Request.Context(), p.ModuleID); err != nil {
			r.writeError(c, err)
			return
		}
	}
	res, err := r.Service.Query(c.Request.Context(), p)
	if err != nil {
		if errors.Is(err, forecastsvc.ErrBadQuery) {
			query.BadRequest(c, err)
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, res)
}
