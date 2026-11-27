package api

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The fixed formula of the resource analysis is the second source of a decision, next to the forecasts of the algorithms. These routes list its calculations apart from the forecasts and say how
// far each decision has gone: the source the module follows, what each source says, what the mode of the module lets it do and what the operator did.

// RuleDecisions serves GET /rule-decisions: every module with its newest calculation of the fixed formula and how far its decision has gone.
// @Summary The calculations of the fixed formula
// @Tags Forecast
// @Router /api/v1/rule-decisions [get]
func (r *ForecastRouter) RuleDecisions(c *gin.Context) {
	rows, err := r.Service.RuleRows(c.Request.Context())
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": len(rows)})
}

// ModuleRuleDecisions serves GET /modules/:id/rule-decisions: the calculations of a module, newest first.
// @Summary The calculations of the fixed formula of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/rule-decisions [get]
func (r *ForecastRouter) ModuleRuleDecisions(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	page, size := pageParams(c)
	list, total, err := r.Service.Rules.List(c.Request.Context(), id, page, size)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// ModuleRuleRun serves POST /modules/:id/rule-decisions/run: it makes the calculation now, from Prometheus, and stores it. It is the same reading as the resource dashboard, held to the
// time the platform gives a request; a calculation that does not end in it ends when the scheduled one runs.
// @Summary Calculate the fixed formula of a module now
// @Tags Forecast
// @Router /api/v1/modules/{id}/rule-decisions/run [post]
func (r *ForecastRouter) ModuleRuleRun(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), r.Service.SyncBudget(c.Request.Context(), timing.PipelineTimeout))
	defer cancel()
	row, err := r.Service.RuleDecide(ctx, id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, row)
}

// ModuleDispatch serves GET /modules/:id/dispatch: the dispatch detail of a module.
// @Summary The dispatch detail of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/dispatch [get]
func (r *ForecastRouter) ModuleDispatch(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	out, err := r.Service.Dispatch(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
