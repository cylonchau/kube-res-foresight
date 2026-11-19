package api

import (
	"context"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/gin-gonic/gin"
)

// windowQuery reads the window of a request: window_minutes, or window as one of the labels (15m, 30m, 1h, 1d, 7d, 15d, 30d); 0 is the baseline window.
func windowQuery(c *gin.Context) (int, bool) {
	if raw := c.Query("window_minutes"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			query.BadRequest(c, err)
			return 0, false
		}
		return n, true
	}
	if label := c.Query("window"); label != "" {
		for _, w := range overprovision.Windows {
			if w.Label == label {
				return w.Minutes, true
			}
		}
		query.BadRequest(c, forecastsvc.ErrOverprovisionWindow)
		return 0, false
	}
	return 0, true
}

// OverprovisionBoard serves GET /overprovision?window=: the dashboard of the over-provisioning analysis, from the newest stored results of the window.
// @Summary Over-provisioning dashboard
// @Tags Forecast
// @Router /api/v1/overprovision [get]
func (r *ForecastRouter) OverprovisionBoard(c *gin.Context) {
	minutes, ok := windowQuery(c)
	if !ok {
		return
	}
	out, err := r.Service.OverprovisionBoard(c.Request.Context(), minutes)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// OverprovisionRun serves POST /overprovision/run?module_id=: analyse a module (every enabled module without module_id) now and store the results. It
// holds to the budget of a manual step; the modules it did not reach are left to the scheduled task.
// @Summary Analyse the modules now
// @Tags Forecast
// @Router /api/v1/overprovision/run [post]
func (r *ForecastRouter) OverprovisionRun(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), r.Service.SyncBudget(c.Request.Context(), timing.PipelineTimeout))
	defer cancel()
	done, err := r.Service.RunOverprovision(ctx, uintQuery(c, "module_id"))
	out := gin.H{"analyzed": done, "at": time.Now().UTC()}
	if err != nil {
		if done == 0 {
			r.writeError(c, err)
			return
		}
		out["failed"] = err.Error()
	}
	query.SuccessResponse(c, nil, out)
}

// ModuleOverprovision serves GET /modules/:id/overprovision?window=&from=&to=: one module analysed live over a window of the list or a custom range
// (RFC 3339), with the series of its chart, its JVM metrics and the stored history of the window.
// @Summary Over-provisioning of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/overprovision [get]
func (r *ForecastRouter) ModuleOverprovision(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	minutes, ok := windowQuery(c)
	if !ok {
		return
	}
	var from, to time.Time
	if f := timeQuery(c, "from"); f != nil {
		from = *f
	}
	if t := timeQuery(c, "to"); t != nil {
		to = *t
	}
	out, err := r.Service.OverprovisionDetail(c.Request.Context(), id, minutes, from, to)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
