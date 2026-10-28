package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// Two different things happen when the system cannot do what it planned, and the records keep them apart.
//
//	degrade:  the forecast, the data or the decision service is not usable, so a safe preset runs instead: the module's fallback,
//	          the original value, or the memory the OOM protection demands. The configuration in force before is the "original".
//	rollback: a change that was decided was not applied (the operator got an error, or another autoscaler holds the target), so
//	          the workload stays on the configuration that was valid before it. The change that was wanted is the "original".
const (
	FallbackDegrade  = "degrade"
	FallbackRollback = "rollback"
)

// reasonDecisionUnavailable is the degradation reported by the operator: it had no usable decision and held or used its fallback.
const reasonDecisionUnavailable = "decision_unavailable"

// fallbackReasons are the reason codes the filter accepts.
var fallbackReasons = map[string]bool{
	forecastsvc.DegradeNoData: true, forecastsvc.DegradeStaleData: true, forecastsvc.DegradeInsufficient: true,
	forecastsvc.DegradePredictorFailed: true, forecastsvc.DegradeNoPrediction: true, forecastsvc.DegradeStalePrediction: true,
	forecastsvc.DegradeNotEligible: true, forecastsvc.DegradeRecovering: true, "oom_floor": true, reasonDecisionUnavailable: true,
}

// ConfigPoint is one configuration value of a record: the value, and where it came from.
type ConfigPoint struct {
	Set    bool   `json:"set"`
	Value  int64  `json:"value"`
	Source string `json:"source,omitempty"`
}

// FallbackChange is what happened to one field: why, from which configuration to which.
type FallbackChange struct {
	Field    string      `json:"field"` // cpu, memory, replicas, gpu or memory_floor
	Trigger  string      `json:"trigger"`
	Note     string      `json:"note,omitempty"`
	Original ConfigPoint `json:"original"`
	Target   ConfigPoint `json:"target"`
}

// FallbackRecord is one episode of a degradation, or one rollback of a change that was not applied.
type FallbackRecord struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	At       time.Time        `json:"at"`
	ModuleID uint             `json:"module_id"`
	Changes  []FallbackChange `json:"changes"`
	// Result is what became of the configuration the record switched to, as the state (see the snapshot states, or not_applied for a
	// rollback) and the sentence behind it.
	ResultState string `json:"result_state"`
	Result      string `json:"result"`
	// SnapshotID and Version name the snapshot of a degradation; ExecutionID the operator report of a rollback.
	SnapshotID  uint `json:"snapshot_id,omitempty"`
	Version     int  `json:"version,omitempty"`
	ExecutionID uint `json:"execution_id,omitempty"`
	// RecoveredAt is when the episode ended (the first later healthy snapshot, or the first later successful execution).
	RecoveredAt     *time.Time `json:"recovered_at"`
	DurationSeconds int64      `json:"duration_seconds"`
	Ongoing         bool       `json:"ongoing"`
	// Reasons maps a degraded field to its reason code, Notes to the sentence behind it.
	Reasons map[string]string `json:"reasons"`
	Notes   map[string]string `json:"notes"`
}

type fallbackCandidate struct {
	at   time.Time
	rec  *model.Recommendation
	exec *model.ScalingExecution
}

// maxFallbackScan bounds what one request reads of each kind before it merges and pages.
const maxFallbackScan = 1000

func pointOf(rec *model.Recommendation, field string) ConfigPoint {
	if rec == nil {
		return ConfigPoint{}
	}
	switch field {
	case "cpu":
		return ConfigPoint{Set: rec.CPUSet, Value: rec.CPUMilli, Source: rec.CPUSource}
	case "memory", "memory_floor":
		return ConfigPoint{Set: rec.MemorySet, Value: rec.MemoryBytes, Source: rec.MemorySource}
	case "replicas":
		return ConfigPoint{Set: rec.ReplicasSet, Value: int64(rec.Replicas), Source: rec.ReplicasSource}
	case "gpu":
		return ConfigPoint{Set: rec.GPUSet, Value: int64(rec.GPU), Source: rec.GPUSource}
	}
	return ConfigPoint{}
}

// ListFallbacks serves the degradation and rollback records. A degradation is an episode: it starts at the first snapshot in which
// a field fell back (or the OOM protection acted) after a healthy one and ends at the next healthy snapshot. A rollback is an
// operator report that did not go as decided.
// @Summary Degradation and rollback records
// @Tags Forecast
// @Router /api/v1/fallbacks [get]
func (r *ForecastRouter) ListFallbacks(c *gin.Context) {
	page, size := pageParams(c)
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	reason, kind := c.Query("reason"), c.Query("kind")
	if reason != "" && !fallbackReasons[reason] {
		query.BadRequest(c, fmt.Errorf("unknown reason %q", reason))
		return
	}
	if kind != "" && kind != FallbackDegrade && kind != FallbackRollback {
		query.BadRequest(c, fmt.Errorf("kind must be %s or %s", FallbackDegrade, FallbackRollback))
		return
	}
	ctx := c.Request.Context()
	since, until := timeQuery(c, "from"), timeQuery(c, "to")

	var cands []fallbackCandidate
	if (kind == "" || kind == FallbackDegrade) && reason != reasonDecisionUnavailable {
		recs, err := r.Predictions.DegradedSnapshots(ctx, rdb.FallbackFilter{ModuleID: uint(moduleID), Reason: reason, Since: since, Until: until}, maxFallbackScan)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		for _, rec := range recs {
			prev, err := r.Predictions.PreviousRecommendation(ctx, rec.ModuleID, rec.Version)
			if err != nil {
				query.InternalError(c, err)
				return
			}
			if prev != nil && (prev.Degraded || prev.OOMFloor) {
				continue // the same episode, already listed at its start
			}
			cands = append(cands, fallbackCandidate{at: rec.CreatedAt, rec: rec})
		}
	}
	if reason == "" || reason == reasonDecisionUnavailable {
		execs, err := r.Predictions.ProblemExecutions(ctx, uint(moduleID), since, until, maxFallbackScan)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		for _, e := range execs {
			operatorDegrade := e.Result == model.ExecHeld
			if (operatorDegrade && kind == FallbackRollback) || (!operatorDegrade && kind == FallbackDegrade) {
				continue
			}
			if reason == reasonDecisionUnavailable && !operatorDegrade {
				continue
			}
			cands = append(cands, fallbackCandidate{at: e.CreatedAt, exec: e})
		}
	}
	asc := c.Query("sort_order") == "ascend" || c.Query("sort_order") == "asc"
	sort.SliceStable(cands, func(i, j int) bool {
		if asc {
			return cands[i].at.Before(cands[j].at)
		}
		return cands[i].at.After(cands[j].at)
	})
	total := len(cands)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	start := min((page-1)*size, total)
	end := min(start+size, total)
	now := time.Now()
	out := make([]FallbackRecord, 0, end-start)
	for _, cand := range cands[start:end] {
		var rec FallbackRecord
		var err error
		if cand.rec != nil {
			rec, err = r.degradeRecord(ctx, cand.rec, now)
		} else {
			rec, err = r.executionRecord(ctx, cand.exec, now)
		}
		if err != nil {
			query.InternalError(c, err)
			return
		}
		out = append(out, rec)
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// degradeRecord builds the record of the degradation episode that starts at a snapshot.
func (r *ForecastRouter) degradeRecord(ctx context.Context, light *model.Recommendation, now time.Time) (FallbackRecord, error) {
	rec, err := r.Predictions.SnapshotByID(ctx, light.ID)
	if err != nil {
		return FallbackRecord{}, err
	}
	out := FallbackRecord{ID: fmt.Sprintf("d-%d", rec.ID), Kind: FallbackDegrade, At: rec.CreatedAt, ModuleID: rec.ModuleID, SnapshotID: rec.ID, Version: rec.Version,
		Changes: []FallbackChange{}, Reasons: map[string]string{}, Notes: map[string]string{}}
	if rec.DegradeReasons != "" {
		_ = json.Unmarshal([]byte(rec.DegradeReasons), &out.Reasons)
	}
	if rec.OOMFloor {
		out.Reasons["memory_floor"] = "oom_floor"
	}
	var detail forecastsvc.RecommendDetail
	if json.Unmarshal([]byte(rec.Detail), &detail) == nil {
		for field, note := range detail.DegradeNotes {
			out.Notes[field] = note
		}
		if rec.OOMFloor && detail.OOM != nil {
			out.Notes["memory_floor"] = detail.OOM.Reason
		}
	}
	before, err := r.Predictions.PreviousHealthy(ctx, rec.ModuleID, rec.Version)
	if err != nil {
		return out, err
	}
	for _, field := range []string{"cpu", "memory", "replicas", "gpu", "memory_floor"} {
		code, ok := out.Reasons[field]
		if !ok {
			continue
		}
		out.Changes = append(out.Changes, FallbackChange{Field: field, Trigger: code, Note: out.Notes[field], Original: pointOf(before, field), Target: pointOf(rec, field)})
	}

	// what became of the configuration it switched to: the state of the snapshot, as the operator and the admission reported
	snap, err := r.snapshotRecord(ctx, rec, now, false)
	if err != nil {
		return out, err
	}
	out.ResultState, out.Result = snap.State, snap.StateReason

	next, err := r.Predictions.NextHealthy(ctx, rec.ModuleID, rec.Version)
	if err != nil {
		return out, err
	}
	end := now
	if next != nil {
		out.RecoveredAt, end = &next.CreatedAt, next.CreatedAt
	} else {
		out.Ongoing = true
	}
	if d := end.Sub(rec.CreatedAt); d > 0 {
		out.DurationSeconds = int64(d / time.Second)
	}
	return out, nil
}

// executionRecord builds the record of an operator report that did not go as decided: a rollback when a change was not applied,
// a degradation when the operator had no usable decision.
func (r *ForecastRouter) executionRecord(ctx context.Context, e *model.ScalingExecution, now time.Time) (FallbackRecord, error) {
	out := FallbackRecord{ID: fmt.Sprintf("e-%d", e.ID), At: e.CreatedAt, ModuleID: e.ModuleID, ExecutionID: e.ID, Version: int(e.DecisionVersion),
		Reasons: map[string]string{}, Notes: map[string]string{}}
	change := FallbackChange{Field: "replicas", Note: e.Reason, Original: ConfigPoint{Set: true, Value: int64(e.Desired), Source: "decision"},
		Target: ConfigPoint{Set: true, Value: int64(e.Current), Source: "previous"}}
	switch e.Result {
	case model.ExecHeld:
		out.Kind = FallbackDegrade
		change.Trigger = reasonDecisionUnavailable
		out.ResultState = "held"
		out.Result = fmt.Sprintf("the operator had no usable decision and kept %d replica(s) (%s)", e.Current, e.Reason)
	default:
		out.Kind = FallbackRollback
		change.Trigger = e.Result
		out.ResultState = "not_applied"
		out.Result = fmt.Sprintf("the change to %d replica(s) was not applied: the workload stays on %d, the last valid configuration (%s)", e.Desired, e.Current, e.Reason)
	}
	out.Changes = []FallbackChange{change}
	out.Reasons["replicas"], out.Notes["replicas"] = change.Trigger, e.Reason
	next, err := r.Predictions.NextSuccessfulExecution(ctx, e.ModuleID, e.ID)
	if err != nil {
		return out, err
	}
	end := now
	if next != nil {
		out.RecoveredAt, end = &next.CreatedAt, next.CreatedAt
	} else {
		out.Ongoing = true
	}
	if d := end.Sub(e.CreatedAt); d > 0 {
		out.DurationSeconds = int64(d / time.Second)
	}
	return out, nil
}
