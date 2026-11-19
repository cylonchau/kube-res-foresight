package api

import (
	"context"
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
	task, err := r.Async.SubmitSpec(c.Request.Context(), asyncrun.Spec{Owner: owner.ID, OwnerName: owner.Name, Kind: steprun.Kind, Title: step + ": " + m.Name, Dedup: dedup,
		TraceID: c.GetString("requestId"), MaxAttempts: steprun.Attempts[step], Input: steprun.Input{ModuleID: id, Step: step, Body: body}})
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
	out, kept, err := r.Service.ResourceAnalysisCached(c.Request.Context(), uintQuery(c, "module_id"), days)
	if err != nil {
		r.writeError(c, err)
		return
	}
	cacheHeaders(c, kept)
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

// ModuleChangePreviewAsync serves POST /modules/:id/change-preview/async: the preview of a change, queued as a background task of the signed-in user. It
// answers at once with the task, which holds the report the synchronous preview would have answered and is held only to forecast.pipeline_timeout_seconds,
// not to the room under the gateway timeout, so a long replay no longer ends in a 504. The same preview asked while one is open is the same task.
func (r *ForecastRouter) ModuleChangePreviewAsync(c *gin.Context) {
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
	var req ChangePreviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	m, err := r.Modules.Get(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	in := steprun.PreviewInput{ModuleID: id, SinceHours: req.SinceHours, Change: req.PreviewChange}
	raw, _ := json.Marshal(in)
	task, err := r.Async.SubmitSpec(c.Request.Context(), asyncrun.Spec{Owner: owner.ID, OwnerName: owner.Name, Kind: steprun.PreviewKind, Title: "preview: " + m.Name,
		Dedup: "preview-" + asyncrun.Hash("p", raw)[:24], TraceID: c.GetString("requestId"), MaxAttempts: steprun.PreviewAttempts, Input: in,
		// the same replay that was made lately, from the same data, is not queued: the task is the answer that is kept
		Cached: func(ctx context.Context) (any, bool) {
			if rep, ok := r.Service.PreviewKept(ctx, id, req.SinceHours, req.PreviewChange); ok {
				return rep, true
			}
			return nil, false
		}})
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
