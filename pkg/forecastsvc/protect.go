package forecastsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// Reason codes of a degraded field. They are stable strings: the audit page, the exports and the alert rules
// read them.
const (
	DegradeNoData          = "no_data"              // the metric has no stored history
	DegradeStaleData       = "stale_data"           // the newest sample is too old
	DegradeInsufficient    = "insufficient_history" // too short, too many gaps, or too little contiguous history
	DegradePredictorFailed = "predictor_failed"     // the algorithms ran and failed (or kept failing)
	DegradeNoPrediction    = "no_prediction"        // no forecast has been made yet
	DegradeStalePrediction = "stale_prediction"     // the newest forecast is older than the module TTL
	DegradeNotEligible     = "no_eligible_model"    // every model is unpromoted or changed since its promotion
	DegradeRecovering      = "recovering"           // healthy again, held back until RecoverCycles good cycles in a row
	DegradeLowScore        = "low_score"            // every usable algorithm scored below the module minimum (or has no score yet)
	DegradePredictTimeout  = "predict_timeout"      // the forecast did not finish in time
)

// OOMFloor is what the OOM protection decided for a module.
type OOMFloor struct {
	Active     bool      `json:"active"`
	Events     int       `json:"events"`
	LastEvent  time.Time `json:"last_event,omitempty"`
	WorkingSet int64     `json:"working_set_bytes,omitempty"`
	Limit      int64     `json:"limit_bytes,omitempty"`
	Uplift     float64   `json:"uplift"`
	FloorBytes int64     `json:"floor_bytes,omitempty"`
	Reason     string    `json:"reason"`
}

// HeapPressure is the JVM heap ratio seen recently.
type HeapPressure struct {
	Used      float64 `json:"used_bytes,omitempty"`
	Max       float64 `json:"max_bytes,omitempty"`
	Ratio     float64 `json:"ratio,omitempty"`
	Threshold float64 `json:"threshold"`
	Over      bool    `json:"over"`
	Reason    string  `json:"reason"`
}

// auxRange reads the stored samples of every enabled source of a kind across the bound instances; no source or no
// series means no samples, not an error.
func (s *Service) auxRange(ctx context.Context, bs boundSet, kind string, from, to time.Time) ([]model.MetricSample, error) {
	var out []model.MetricSample
	for _, ref := range bs.sourcesOfKind(kind) {
		series, err := s.Samples.Series(ctx, ref.Inst.ID, ref.Src.Key)
		if err != nil {
			continue
		}
		rows, err := s.Samples.Range(ctx, series.ID, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func maxValue(samples []model.MetricSample) float64 {
	v := 0.0
	for _, sm := range samples {
		v = math.Max(v, sm.Value)
	}
	return v
}

// oomFloor computes the least memory a module may be given: when an OOM kill was seen within OOMLookbackDays
// the memory is never below max(working set before the kill, memory limit) x OOMMemoryUplift. Without an OOM
// mapping or without events the floor is inactive. An OOM with no usable baseline is reported but gives no floor
// (inventing a number would be worse than saying so).
func (s *Service) oomFloor(ctx context.Context, m *model.ForecastModule, bs boundSet, now time.Time) (OOMFloor, error) {
	out := OOMFloor{Uplift: m.OOMMemoryUplift}
	if len(bs.sourcesOfKind("oom")) == 0 {
		out.Reason = "no OOM metric source is configured"
		return out, nil
	}
	from := now.Add(-time.Duration(m.OOMLookbackDays) * 24 * time.Hour)
	events, err := s.auxRange(ctx, bs, "oom", from, now)
	if err != nil {
		return out, err
	}
	var last time.Time
	for _, e := range events {
		if e.Value > 0 {
			out.Events++
			if t := time.Unix(e.TS, 0).UTC(); t.After(last) {
				last = t
			}
		}
	}
	if out.Events == 0 {
		out.Reason = fmt.Sprintf("no OOM kill in the last %d days", m.OOMLookbackDays)
		return out, nil
	}
	out.LastEvent = last
	// the working set in the hours before the newest kill; the limit over the whole lookback (it can change)
	window := 6 * time.Hour
	if ws, err := s.auxRange(ctx, bs, "oom_working_set", last.Add(-window), last); err != nil {
		return out, err
	} else {
		out.WorkingSet = int64(maxValue(ws))
	}
	if lim, err := s.auxRange(ctx, bs, "k8s_mem_limits", from, now); err != nil {
		return out, err
	} else {
		out.Limit = int64(maxValue(lim))
	}
	base := out.WorkingSet
	if out.Limit > base {
		base = out.Limit
	}
	if base == 0 { // no OOM detail: the memory the workload itself used before the kill is the best evidence left
		if mem, err := s.auxRange(ctx, bs, model.MetricMemory, last.Add(-window), last); err != nil {
			return out, err
		} else {
			base = int64(maxValue(mem))
		}
	}
	if base == 0 {
		out.Reason = fmt.Sprintf("%d OOM kill(s), latest %s, but no working set, limit or memory history to size a floor from", out.Events, last.Format(time.RFC3339))
		return out, nil
	}
	out.Active, out.FloorBytes = true, int64(math.Ceil(float64(base)*m.OOMMemoryUplift))
	out.Reason = fmt.Sprintf("%d OOM kill(s) in %d days, latest %s; max(working set %d, limit %d) x %.2g",
		out.Events, m.OOMLookbackDays, last.Format(time.RFC3339), out.WorkingSet, out.Limit, m.OOMMemoryUplift)
	return out, nil
}

// heapPressure is the newest JVM heap used / max ratio; the max of the last few samples of used over the latest max,
// so one low sample after a GC does not hide a heap that is mostly full.
func (s *Service) heapPressure(ctx context.Context, m *model.ForecastModule, bs boundSet, now time.Time) (HeapPressure, error) {
	out := HeapPressure{Threshold: m.JVMHeapPressure}
	if m.JVMHeapPressure <= 0 {
		out.Reason = "heap pressure rule is disabled"
		return out, nil
	}
	if len(bs.sourcesOfKind("jvm_heap_used")) == 0 || len(bs.sourcesOfKind("jvm_heap_max")) == 0 {
		out.Reason = "jvm_heap_used and jvm_heap_max are not both configured as metric sources"
		return out, nil
	}
	from := now.Add(-30 * time.Minute)
	used, err := s.auxRange(ctx, bs, "jvm_heap_used", from, now)
	if err != nil {
		return out, err
	}
	max, err := s.auxRange(ctx, bs, "jvm_heap_max", from, now)
	if err != nil {
		return out, err
	}
	if len(used) == 0 || len(max) == 0 {
		out.Reason = "no JVM heap samples in the last 30 minutes"
		return out, nil
	}
	out.Used, out.Max = maxValue(used), maxValue(max)
	if out.Max <= 0 {
		out.Reason = "the heap maximum is not positive"
		return out, nil
	}
	out.Ratio = out.Used / out.Max
	out.Over = out.Ratio > m.JVMHeapPressure
	out.Reason = fmt.Sprintf("heap %.0f%% of its maximum over the last 30 minutes (threshold %.0f%%)", out.Ratio*100, m.JVMHeapPressure*100)
	return out, nil
}

// fieldDegradeCode explains why a recommendation field has no forecast behind it. The history of the sources
// feeding the field is probed first (a forecast cannot be better than its data), then what the algorithms did.
func (s *Service) fieldDegradeCode(ctx context.Context, bs boundSet, field string, detail *RecommendDetail, failed map[string]string,
	st *model.ModuleRunState, states []*instanceState) (string, string) {
	refs := bs.forecastOfResource(resourceOfField[field])
	for _, ref := range refs {
		if _, _, err := s.loadTarget(ctx, ref.Inst, ref.Src.Key); err != nil {
			switch {
			case errors.Is(err, ErrStale):
				return DegradeStaleData, err.Error()
			case errors.Is(err, ErrInsufficientData):
				return DegradeInsufficient, err.Error()
			default:
				return DegradeNoData, err.Error()
			}
		}
	}
	for _, ref := range refs {
		if why, ok := failed[pairKey(ref.Inst.Key(), ref.Src.Key)]; ok {
			if strings.Contains(why, ErrTimeout.Error()) {
				return DegradePredictTimeout, why
			}
			return DegradePredictorFailed, why
		}
	}
	// algorithms that could forecast but were held back by their score
	scoreHeld := false
	for _, state := range states {
		if !state.eligible && state.hasUsableForecast() && (strings.HasPrefix(state.reason, "score ") || strings.HasPrefix(state.reason, "not scored")) {
			scoreHeld = true
		}
	}
	var stale, none, unfit bool
	for _, c := range detail.Considered {
		inField := false
		for _, ref := range refs {
			inField = inField || (c.Metric == ref.Src.Key && c.AlgorithmID == ref.Inst.Key())
		}
		if !inField {
			continue
		}
		switch {
		case c.Eligible:
		case strings.HasPrefix(c.Reason, "stale prediction"):
			stale = true
		case strings.HasPrefix(c.Reason, "no prediction"):
			none = true
		default:
			unfit = true
		}
	}
	switch {
	case scoreHeld:
		return DegradeLowScore, "every algorithm with a forecast scored below the module minimum, or has not been scored yet"
	case stale && st != nil && st.ConsecutiveFailures > 0:
		return DegradePredictorFailed, fmt.Sprintf("the forecast is stale and the last %d cycle(s) failed: %s", st.ConsecutiveFailures, st.LastError)
	case stale:
		return DegradeStalePrediction, "the newest forecast is older than the module TTL"
	case none:
		return DegradeNoPrediction, "no forecast has been made yet"
	case unfit:
		return DegradeNotEligible, "no algorithm is baseline or promoted for its current parameters"
	}
	return DegradeNoPrediction, "no usable forecast"
}

// heldFields are the fields of the previous degraded recommendation that must keep using the fallback chain
// because the forecast has not been healthy for RecoverCycles cycles in a row yet.
func heldFields(st *model.ModuleRunState, m *model.ForecastModule) map[string]bool {
	held := map[string]bool{}
	if st == nil || strings.TrimSpace(st.DegradedFields) == "" || st.HealthyCycles+1 >= m.RecoverCycles {
		return held
	}
	var prev map[string]string
	if json.Unmarshal([]byte(st.DegradedFields), &prev) != nil {
		return held
	}
	for field := range prev {
		held[field] = true
	}
	return held
}

// cycleHealth is the outcome of one scheduled cycle fed back into the run state.
type cycleHealth struct {
	Errors   []string
	Degraded map[string]string // field -> code of the newest recommendation
}

// updateRunState folds one pipeline cycle into the module's remembered state.
func (s *Service) updateRunState(ctx context.Context, moduleID uint, now time.Time, h cycleHealth) {
	if s.RunStates == nil {
		return
	}
	st, err := s.RunStates.RunState(ctx, moduleID)
	if err != nil {
		return
	}
	st.LastRunAt = &now
	real := map[string]string{} // degradation that is a fault, not the hold-back of a recovery
	for field, code := range h.Degraded {
		if code != DegradeRecovering {
			real[field] = code
		}
	}
	switch {
	case len(h.Errors) > 0:
		st.ConsecutiveFailures++
		st.HealthyCycles = 0
		st.LastError = truncate(strings.Join(h.Errors, "; "), 500)
	default:
		st.ConsecutiveFailures, st.LastError = 0, ""
		st.LastSuccessAt = &now
		if len(real) > 0 {
			st.HealthyCycles = 0
		} else {
			st.HealthyCycles++
		}
	}
	switch {
	case len(real) > 0:
		data, _ := json.Marshal(real)
		st.DegradedFields = string(data)
	case len(h.Degraded) == 0:
		st.DegradedFields = ""
	}
	_ = s.RunStates.SaveRunState(ctx, st)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
