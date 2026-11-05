package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Advice of a NeedField about a bound of the module.
const (
	// AdviceLowerMin says the minimum of the module is above what the forecast needs: the module keeps more than it uses.
	AdviceLowerMin = "lower_min"
	// AdviceRaiseMax says the forecast needs more than the maximum allows: the module is held below what it needs.
	AdviceRaiseMax = "raise_max"
)

// needRangeHours is the stretch of forecasts a suggested minimum and maximum of the replicas are taken from.
const needRangeHours = 24

// NeedField is one quantity (replicas, or the CPU or memory of one Pod) as the forecast needs it, as it was recommended, and as it runs now.
type NeedField struct {
	// Needed is what the forecast asks for before the minimum and maximum of the module are applied; 0 when no forecast gave one.
	Needed int64 `json:"needed"`
	// Recommended is the recommendation, after the bounds, the periods and the protections.
	Recommended int64 `json:"recommended"`
	// Current is what runs or is configured now; 0 when it is not known.
	Current int64 `json:"current"`
	// Min and Max are the bounds of the module in force (0 = none).
	Min int64 `json:"min,omitempty"`
	Max int64 `json:"max,omitempty"`
	// Bound is "min" or "max" when that bound is what moved Needed to Recommended.
	Bound string `json:"bound,omitempty"`
	// Advice is lower_min or raise_max when a bound of the module is out of step with the need; SuggestMin and SuggestMax are the bounds the
	// need of the last day suggests (replicas only).
	Advice     string `json:"advice,omitempty"`
	SuggestMin int64  `json:"suggest_min,omitempty"`
	SuggestMax int64  `json:"suggest_max,omitempty"`
	// Held is "ratio" or "hold" when the Pod spec in force was kept instead of the value computed (CPU and memory only): "ratio" = the computed
	// value is within the change ratio of it, "hold" = it is a decrease that waits out the hold time. HeldLimit is that ratio (percent) or
	// hold (hours). Then Recommended is what was kept, which can be far from Needed.
	Held      string `json:"held,omitempty"`
	HeldLimit int    `json:"held_limit,omitempty"`
}

// NeedTotals is the whole workload: the replicas times the CPU and the memory of one Pod, for the need, the recommendation and what runs.
type NeedTotals struct {
	NeededCPUMilli         int64 `json:"needed_cpu_milli"`
	RecommendedCPUMilli    int64 `json:"recommended_cpu_milli"`
	CurrentCPUMilli        int64 `json:"current_cpu_milli"`
	NeededMemoryBytes      int64 `json:"needed_memory_bytes"`
	RecommendedMemoryBytes int64 `json:"recommended_memory_bytes"`
	CurrentMemoryBytes     int64 `json:"current_memory_bytes"`
}

// NeedDetail is what a module needs, read against what it is set to: stored with every recommendation and answered by Estimate. It is the
// answer to "how many replicas, and how much CPU and memory, does this module need, and does its configuration get in the way".
type NeedDetail struct {
	// Algorithm is the one the need was read from; empty when no forecast decided.
	Algorithm string `json:"algorithm,omitempty"`
	// Basis says where the figures come from: forecast, or none when no algorithm had a usable forecast (then Needed is 0 everywhere).
	Basis    string     `json:"basis"`
	Replicas NeedField  `json:"replicas"`
	CPU      NeedField  `json:"cpu_milli"`
	Memory   NeedField  `json:"memory_bytes"`
	Total    NeedTotals `json:"total"`
	// RangeNote says how the suggested bounds were found, or why there are none.
	RangeNote string `json:"range_note,omitempty"`
	// Parts takes the replica need apart into the load measured now, the load forecast and the margin put on it, which are three different
	// things and are never shown as one number. Nil when the replicas were not solved from a forecast.
	Parts *NeedParts `json:"parts,omitempty"`
}

// NeedPart is a load figure: what it is in (Unit), its value and the replicas it alone would need.
type NeedPart struct {
	Resource string     `json:"resource"`
	Unit     string     `json:"unit"`
	Value    float64    `json:"value"`
	Replicas int        `json:"replicas"`
	At       *time.Time `json:"at,omitempty"`
	Note     string     `json:"note,omitempty"`
}

// NeedForecast is the forecast part: the peak of the forecast and what the figure is. Conservatism is "" for an expected load and, for an
// algorithm whose forecast is deliberately high ("P90", "peak envelope"), what that is: it is a level that is enough most of the time, not
// "what will be used next", and a high percentile does not make a memory limit safe either.
type NeedForecast struct {
	NeedPart
	Algorithm    string `json:"algorithm"`
	Conservatism string `json:"conservatism,omitempty"`
}

// NeedMargin is what is added on top of the forecast: the factor (1 + the expected shortfall of the forecast at its peak) and where that
// came from, and the replicas it adds.
type NeedMargin struct {
	Factor   float64 `json:"factor"`
	Source   string  `json:"source"`
	Replicas int     `json:"replicas"`
}

// NeedParts is the replica need in three parts. PerPod is what one Pod carries of the resource, at the target utilization.
type NeedParts struct {
	// Current is the load measured now (the newest samples), nil when there is none recent enough.
	Current  *NeedPart    `json:"current,omitempty"`
	Forecast NeedForecast `json:"forecast"`
	Margin   NeedMargin   `json:"margin"`
	PerPod   float64      `json:"per_pod"`
}

// unitOfResource is what the load of a resource is measured in.
func unitOfResource(resource string) string {
	switch resource {
	case "cpu":
		return "cores"
	case "memory":
		return "bytes"
	}
	return "rps"
}

// partsOf takes the replica plan apart: the constraint that decided gives the peak of the forecast, the capacity of one Pod and the margin;
// the live load is the measured one. The margin is what the factor adds over the forecast peak alone.
func partsOf(plan *decision.ReplicaPlan, live *LiveDetail, algorithm, conservatism string) *NeedParts {
	if plan == nil || len(plan.Constraints) == 0 {
		return nil
	}
	best := plan.Constraints[0]
	for _, c := range plan.Constraints[1:] {
		if c.Needed > best.Needed {
			best = c
		}
	}
	if best.UnitCapacity <= 0 || best.PeakDemand <= 0 {
		return nil // the pressure and heap constraints are not a forecast load over a capacity
	}
	alone := int(math.Max(1, math.Ceil(best.PeakDemand/best.UnitCapacity)))
	parts := &NeedParts{PerPod: best.UnitCapacity}
	parts.Forecast = NeedForecast{NeedPart: NeedPart{Resource: best.Metric, Unit: unitOfResource(best.Metric), Value: best.PeakDemand, Replicas: alone},
		Algorithm: algorithm, Conservatism: conservatism}
	parts.Margin = NeedMargin{Factor: 1 + best.Uncertainty, Source: best.UncertaintySource, Replicas: max(0, best.Needed-alone)}
	if live != nil && live.OK {
		// The load now is read for the resource the forecast part is about: a memory figure beside a CPU forecast says nothing about either.
		// Without a measurement of that resource, the one that needs the most replicas is shown and the note says so.
		cur := &NeedPart{Resource: live.Resource, Unit: unitOfResource(live.Resource), Value: live.Value, Replicas: live.Need, At: live.At, Note: live.Note}
		for _, r := range live.Resources {
			if r.Resource == best.Metric {
				cur = &NeedPart{Resource: r.Resource, Unit: unitOfResource(r.Resource), Value: r.Value, Replicas: r.Need, At: r.At}
				break
			}
		}
		if cur.Resource != best.Metric {
			cur.Note = "other_resource"
		}
		parts.Current = cur
	}
	return parts
}

// heldOf tells whether the Pod spec of a field was kept (see pinOne) and by what: the change ratio, or the hold of a decrease.
func heldOf(spec *ResourceSpecDetail, field string) (string, int) {
	if spec == nil {
		return "", 0
	}
	switch reason := spec.Reasons[field]; {
	case strings.HasPrefix(reason, "kept: a decrease"):
		return "hold", spec.HoldHours
	case strings.HasPrefix(reason, "kept:"):
		return "ratio", spec.ChangePercent
	}
	return "", 0
}

// bounded tells which bound moved a needed value to the recommended one.
func bounded(needed, recommended, lo, hi int64) string {
	switch {
	case needed <= 0:
		return ""
	case lo > 0 && needed < lo && recommended == lo:
		return "min"
	case hi > 0 && needed > hi && recommended == hi:
		return "max"
	}
	return ""
}

// forecastNeed is the value of a field when it came from an algorithm (not from a period, a fallback or the bypass), else 0.
func forecastNeed(f decision.Field) int64 {
	if f.Set && f.Source == decision.SourceAlgorithm {
		return f.Value
	}
	return 0
}

// needOf reads what the forecast needs against what was recommended and what runs. free is the same decision made without the minimum and
// maximum of the module; plan is the replica plan of the algorithm that decided (nil when the replicas were not solved); current is the
// replica count that runs now (0 = unknown) and maxReplicas the ceiling in force.
func (s *Service) needOf(ctx context.Context, m *model.ForecastModule, bs boundSet, detail *RecommendDetail, rec, free decision.Recommendation,
	plan *decision.ReplicaPlan, current, maxReplicas int, conservatism string, now time.Time) *NeedDetail {
	n := &NeedDetail{Algorithm: detail.Algorithm, Basis: "forecast"}
	var live *LiveDetail
	if detail.Ahead != nil {
		live = &detail.Ahead.Live
	}
	n.Parts = partsOf(plan, live, detail.Algorithm, conservatism)
	n.CPU = NeedField{Needed: forecastNeed(free.CPUMilli), Recommended: rec.CPUMilli.Value, Current: m.PodCPURequestMilli,
		Min: m.MinCPUMilli, Max: m.MaxCPUMilli}
	n.Memory = NeedField{Needed: forecastNeed(free.MemoryBytes), Recommended: rec.MemoryBytes.Value, Current: m.PodMemoryRequestBytes,
		Min: m.MinMemoryBytes, Max: m.MaxMemoryBytes}
	n.CPU.Held, n.CPU.HeldLimit = heldOf(detail.ResourceSpec, "cpu")
	n.Memory.Held, n.Memory.HeldLimit = heldOf(detail.ResourceSpec, "memory")
	n.CPU.Bound = bounded(n.CPU.Needed, n.CPU.Recommended, n.CPU.Min, n.CPU.Max)
	n.Memory.Bound = bounded(n.Memory.Needed, n.Memory.Recommended, n.Memory.Min, n.Memory.Max)

	n.Replicas = NeedField{Recommended: rec.Replicas.Value, Current: int64(current), Min: int64(m.MinReplicas), Max: int64(maxReplicas)}
	switch {
	case plan != nil && plan.Needed > 0:
		n.Replicas.Needed = int64(plan.Needed)
	default:
		n.Replicas.Needed = forecastNeed(free.Replicas)
	}
	n.Replicas.Bound = bounded(n.Replicas.Needed, n.Replicas.Recommended, n.Replicas.Min, n.Replicas.Max)
	switch n.Replicas.Bound {
	case "min":
		n.Replicas.Advice = AdviceLowerMin
	case "max":
		n.Replicas.Advice = AdviceRaiseMax
	}
	if n.Replicas.Needed == 0 && n.CPU.Needed == 0 && n.Memory.Needed == 0 {
		n.Basis = "none"
	}
	if lo, hi, note := s.replicaRange(ctx, m, bs, detail.Algorithm, now); hi > 0 {
		n.Replicas.SuggestMin, n.Replicas.SuggestMax = lo, hi
		n.RangeNote = note
	} else {
		n.RangeNote = note
	}

	n.Total = NeedTotals{
		NeededCPUMilli: n.Replicas.Needed * n.CPU.Needed, RecommendedCPUMilli: n.Replicas.Recommended * n.CPU.Recommended,
		CurrentCPUMilli:   n.Replicas.Current * n.CPU.Current,
		NeededMemoryBytes: n.Replicas.Needed * n.Memory.Needed, RecommendedMemoryBytes: n.Replicas.Recommended * n.Memory.Recommended,
		CurrentMemoryBytes: n.Replicas.Current * n.Memory.Current,
	}
	return n
}

// unboundedReplicas turns a forecast of the CPU the whole workload uses (cores) into the Pods it needs, from the Pod CPU request and the
// target utilization of the module, with the module's minimum and maximum left out. ok is false when the module does not state the capacity
// of one Pod, so there is nothing to divide by.
func unboundedReplicas(m *model.ForecastModule) (func(float64) int, bool) {
	if m.ReplicaMode != model.ReplicaModeDynamic || m.PodCPURequestMilli <= 0 {
		return nil, false
	}
	target := m.TargetUtilization
	if target <= 0 || target > 1 {
		target = 0.7
	}
	headroom := m.HeadroomRatio
	if headroom <= 0 {
		headroom = 1
	}
	perPod := float64(m.PodCPURequestMilli) / 1000 * target
	return func(v float64) int { return int(math.Max(1, math.Ceil(v*headroom/perPod))) }, true
}

// replicaRange is the fewest and the most Pods the forecasts of the last day needed, from the CPU forecast of the algorithm that decided and
// the capacity of one Pod: the bounds that day suggests for the module. 0, 0 and a note when it cannot be found.
func (s *Service) replicaRange(ctx context.Context, m *model.ForecastModule, bs boundSet, algorithm string, now time.Time) (int64, int64, string) {
	capacity, ok := unboundedReplicas(m)
	if !ok {
		return 0, 0, "the module does not state the CPU capacity of one Pod (dynamic replicas with a Pod CPU request), so there is no range to suggest"
	}
	if algorithm == "" {
		return 0, 0, "no algorithm decided, so there is no range to suggest"
	}
	refs := bs.forecastOfResource(model.ResourceCPU)
	if len(refs) == 0 {
		return 0, 0, "the module has no forecast CPU metric, so there is no range to suggest"
	}
	from := now.Add(-needRangeHours * time.Hour)
	rows, err := s.Predictions.ForCompare(ctx, m.ID, refs[0].Src.Key, from, now.Add(time.Duration(m.HorizonMinutes)*time.Minute), []string{algorithm})
	if err != nil || len(rows) == 0 {
		return 0, 0, "the forecasts of the last day are not stored (yet), so there is no range to suggest"
	}
	lo, hi := math.MaxInt, 0
	for _, r := range rows {
		n := capacity(r.Value)
		lo, hi = min(lo, n), max(hi, n)
	}
	return int64(lo), int64(hi), "the fewest and the most Pods the CPU forecasts of " + algorithm + " needed over the last 24 hours, at the Pod CPU request and the target utilization of the module"
}

// seasonCoverage is "" when the history holds enough repeats of the cycle the algorithm learns (or it learns none), else why it may not
// decide yet: how much history there is, in repeats, against what is asked (timing.MinSeasons). The history is the stored span of the
// source, cut to the training window of the module when it sets one, because the algorithm sees no more than that.
func (s *Service) seasonCoverage(ctx context.Context, inst *model.AlgorithmRun, bm *boundModel, key string, minSeasons int) string {
	if minSeasons <= 0 {
		return ""
	}
	series, err := s.Samples.Series(ctx, inst.ID, key)
	if err != nil {
		return ""
	}
	step := time.Duration(series.StepSeconds) * time.Second
	span := forecast.SeasonSpanOf(bm.model, step)
	if span <= 0 {
		return ""
	}
	first, ok1, err1 := s.Samples.Earliest(ctx, series.ID)
	last, ok2, err2 := s.Samples.Latest(ctx, series.ID)
	if err1 != nil || err2 != nil || !ok1 || !ok2 {
		return ""
	}
	have := last.Sub(first)
	if inst.WindowSteps > 0 && time.Duration(inst.WindowSteps)*step < have {
		have = time.Duration(inst.WindowSteps) * step
	}
	need := time.Duration(minSeasons) * span
	if have >= need {
		return ""
	}
	return fmt.Sprintf("not eligible: its cycle is %s and the history holds %.1f of them (%s), %d are needed (%s) before it may decide",
		span.Round(time.Minute), float64(have)/float64(span), have.Round(time.Minute), minSeasons, need.Round(time.Minute))
}
