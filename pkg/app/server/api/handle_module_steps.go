package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/steprun"
	"github.com/gin-gonic/gin"
)

// ModuleStepAsync serves POST /modules/:id/steps/:step (step: collect, predict, run, backtest, evaluate or score): the step of the synchronous
// endpoint, queued as a background task of the signed-in user. It answers at once with the task, which is read at /assistant/async/:id (the
// same task calls serve every kind of task) and holds the result the synchronous endpoint would have answered. A step of the same module that is
// already open is the same task.
func (r *ForecastRouter) ModuleStepAsync(c *gin.Context) {
	if r.Async == nil {
		query.InternalError(c, errors.New("background tasks are not set up"))
		return
	}
	owner, ok := ownerOfRequest(c)
	if !ok {
		query.BadRequest(c, errors.New("background tasks are for a signed-in user only: use the synchronous endpoint"))
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	step := c.Param("step")
	if !steprun.Steps[step] {
		query.BadRequest(c, fmt.Errorf("step %q cannot be run in the background", step))
		return
	}
	m, err := r.Modules.Get(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	var body json.RawMessage
	if raw, _ := io.ReadAll(io.LimitReader(c.Request.Body, 4096)); len(raw) > 0 && json.Valid(raw) {
		body = raw
	}
	if step == "backtest" {
		var req apis.BacktestReq
		if err := json.Unmarshal(body, &req); err != nil || req.Metric == "" {
			query.BadRequest(c, errors.New("the backtest needs a metric"))
			return
		}
	}
	dedup := fmt.Sprintf("step-%d-%s-%s", id, step, string(body))
	if len(dedup) > 100 {
		dedup = dedup[:100]
	}
	task, err := r.Async.Submit(c.Request.Context(), owner.ID, owner.Name, steprun.Kind, step+": "+m.Name, dedup, steprun.Input{ModuleID: id, Step: step, Body: body})
	if err != nil {
		if errors.Is(err, asyncrun.ErrTooMany) {
			query.ErrorResponse(c, http.StatusTooManyRequests, err)
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, asyncDTO(task))
}

// ResourceAnalysis serves GET /resource-analysis?module_id=&days=: for each module, what a Pod requests against what it used over the window, and
// what it could be lowered to. It reads what the platform stored; nothing is read from the cluster and nothing is changed.
// @Summary Resource analysis of the modules
// @Tags Forecast
// @Router /api/v1/resource-analysis [get]
func (r *ForecastRouter) ResourceAnalysis(c *gin.Context) {
	days, _ := strconv.Atoi(c.Query("days"))
	out, err := r.Service.ResourceAnalysis(c.Request.Context(), uintQuery(c, "module_id"), days)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// ModuleMetricCatalog serves GET /modules/:id/metric-catalog: the metrics of the module, with their units and the span of what is stored.
// @Summary Metrics of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/metric-catalog [get]
func (r *ForecastRouter) ModuleMetricCatalog(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	out, err := r.Service.MetricCatalog(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
