package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// SnapshotRecord is one snapshot of the recommendation history with what became of it: the state (pending, applied, failed,
// expired, advisory), the sentence behind it and the figures of the span it covered.
type SnapshotRecord struct {
	*model.Recommendation
	State       string              `json:"state"`
	StateReason string              `json:"state_reason"`
	Current     bool                `json:"current"`
	Outcome     rdb.SnapshotOutcome `json:"outcome"`
	// SpanEnd is the version of the next snapshot (0 while this one is the current snapshot): the versions from this one
	// up to it are the same answer, kept valid every cycle.
	SpanEnd int `json:"span_end"`
	// Executions are the operator's reports for the span (the detail only).
	Executions []*model.ScalingExecution `json:"executions,omitempty"`
}

// maxSnapshotScan bounds how many snapshots a status filter reads before it pages: the history is capped per module, so this
// is more than any module keeps.
const maxSnapshotScan = 2000

var snapshotStates = map[string]bool{
	forecastsvc.SnapshotPending: true, forecastsvc.SnapshotApplied: true, forecastsvc.SnapshotFailed: true,
	forecastsvc.SnapshotExpired: true, forecastsvc.SnapshotAdvisory: true,
}

func (r *ForecastRouter) snapshotRecord(ctx context.Context, rec *model.Recommendation, now time.Time, withExecutions bool) (SnapshotRecord, error) {
	out := SnapshotRecord{Recommendation: rec}
	next, err := r.Predictions.NextSnapshotVersion(ctx, rec.ModuleID, rec.Version)
	if err != nil {
		return out, err
	}
	out.SpanEnd, out.Current = next, next == 0
	if out.Outcome, err = r.Predictions.OutcomeOfSpan(ctx, rec.ModuleID, rec.Version, next); err != nil {
		return out, err
	}
	out.State, out.StateReason = forecastsvc.SnapshotState(rec, out.Outcome, out.Current, now)
	if withExecutions {
		if out.Executions, err = r.Predictions.SnapshotExecutions(ctx, rec.ModuleID, rec.Version, next, 50); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ListSnapshots serves the history of recommendations: the snapshots (the first, every change and one an hour at least), each
// with its state. The state is read from what the operator and the admission reported, so the filter reads the snapshots first.
// @Summary Recommendation snapshots
// @Tags Forecast
// @Router /api/v1/snapshots [get]
func (r *ForecastRouter) ListSnapshots(c *gin.Context) {
	page, size := pageParams(c)
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	state := c.Query("state")
	if state != "" && !snapshotStates[state] {
		query.BadRequest(c, fmt.Errorf("unknown state %q", state))
		return
	}
	covers, _ := strconv.Atoi(c.Query("covers_version"))
	f := rdb.SnapshotFilter{ModuleID: uint(moduleID), Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order"), WithoutBody: true, CoversVersion: covers}
	ctx := c.Request.Context()
	now := time.Now()
	if state == "" {
		list, total, err := r.Predictions.SearchSnapshots(ctx, f, page, size)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		out := make([]SnapshotRecord, 0, len(list))
		for _, rec := range list {
			item, err := r.snapshotRecord(ctx, rec, now, false)
			if err != nil {
				query.InternalError(c, err)
				return
			}
			out = append(out, item)
		}
		query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
		return
	}
	all, _, err := r.Predictions.SearchSnapshots(ctx, f, 1, maxSnapshotScan)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	matched := make([]SnapshotRecord, 0, len(all))
	for _, rec := range all {
		item, err := r.snapshotRecord(ctx, rec, now, false)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		if item.State == state {
			matched = append(matched, item)
		}
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	start := min((page-1)*size, len(matched))
	end := min(start+size, len(matched))
	query.SuccessResponse(c, nil, gin.H{"list": matched[start:end], "total": len(matched), "page": page, "pageSize": size})
}

// GetSnapshot serves one snapshot with the full stored decision (inputs, reasons, the Pod resource spec) and the executions
// reported for it.
// @Summary One recommendation snapshot
// @Tags Forecast
// @Router /api/v1/snapshots/{id} [get]
func (r *ForecastRouter) GetSnapshot(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	rec, err := r.Predictions.SnapshotByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.NotFound(c, query.ErrRecordNotFound)
			return
		}
		query.InternalError(c, err)
		return
	}
	item, err := r.snapshotRecord(c.Request.Context(), rec, time.Now(), true)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, item)
}
