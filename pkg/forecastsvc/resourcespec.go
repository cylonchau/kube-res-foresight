package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The resource spec of a workload (the CPU and memory requests every Pod gets at admission) and its replica count are two
// different things with two different lifecycles. The replica count follows the forecast every cycle: it is what the
// operator scales. The spec is decided once and then stays: a new Pod made by a scale-out or a restart gets the spec in force,
// not whatever the latest forecast says, so the Pods of one workload are alike and a scaling decision never rewrites the
// resources. The spec changes only when the forecast has moved far enough from it to matter (the change ratio), a decrease
// waits out the hold time, and a bypass, a fixed period or the OOM protection act at once. Values are whole, readable
// steps (RoundCPUMilli, RoundMemoryBytes), because 417.3 millicores is not something anyone can set or read.

// Config keys of the resource spec policy, in the system parameters.
const (
	ConfigResourceChangeRatio = "forecast.resource_change_ratio" // percent
	ConfigResourceHoldHours   = "forecast.resource_hold_hours"
)

// Defaults of the resource spec policy.
const (
	DefaultResourceChangePercent = 20
	DefaultResourceHoldHours     = 24
)

const mib = int64(1) << 20

func ceilTo(v, step int64) int64 {
	if step <= 1 || v <= 0 {
		return v
	}
	return (v + step - 1) / step * step
}

// RoundCPUMilli rounds a CPU request up to a readable step: 10 millicores below one core, 50 from one core. Up, never down:
// a request below what was computed is a request below what the workload needs.
func RoundCPUMilli(v int64) int64 {
	if v <= 0 {
		return v
	}
	if v >= 1000 {
		return ceilTo(v, 50)
	}
	return ceilTo(v, 10)
}

// RoundMemoryBytes rounds a memory request up to whole MiB in readable steps: 10 MiB below 1 GiB (320Mi, 420Mi), 64 MiB
// from 1 GiB.
func RoundMemoryBytes(v int64) int64 {
	if v <= 0 {
		return v
	}
	m := (v + mib - 1) / mib
	if m >= 1024 {
		return ceilTo(m, 64) * mib
	}
	return ceilTo(m, 10) * mib
}

// ResourcePair is the CPU and memory of a spec.
type ResourcePair struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// ResourceSpecDetail is what the recommendation says about the spec: its version, whether this cycle kept it, and why.
type ResourceSpecDetail struct {
	// Version counts the changes of the spec of the module; it is the same on every snapshot until the spec changes.
	Version   int        `json:"version"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
	// Held is true when at least one of CPU and memory kept the spec in force although the forecast computed another value.
	Held bool `json:"held"`
	// Changed is true when this snapshot has a spec different from the one before it (always on the first).
	Changed bool `json:"changed"`
	// Computed is what the forecast asked for (rounded), Pinned what the snapshot carries.
	Computed ResourcePair `json:"computed"`
	Pinned   ResourcePair `json:"pinned"`
	// Reasons say, per field, why it was kept or taken.
	Reasons       map[string]string `json:"reasons"`
	ChangePercent int               `json:"change_percent"`
	HoldHours     int               `json:"hold_hours"`
}

// resourcePolicy reads the change ratio (as a fraction) and the hold time from the system parameters.
func (s *Service) resourcePolicy(ctx context.Context) (ratio float64, hold time.Duration) {
	percent, hours := DefaultResourceChangePercent, DefaultResourceHoldHours
	if s.Config != nil {
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigResourceChangeRatio, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 1000 {
				percent = n
			}
		}
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigResourceHoldHours, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 24*365 {
				hours = n
			}
		}
	}
	return float64(percent) / 100, time.Duration(hours) * time.Hour
}

// pinOne decides one field of the spec: take the computed value, or keep the pinned one.
// computed is the field after rounding; prev is what the latest snapshot carried (0 = none); prevFromAlgorithm says that value
// was itself a forecast result and not an override.
func pinOne(f decision.Field, prev int64, prevFromAlgorithm bool, lo, hi int64, now time.Time, changedAt *time.Time, ratio float64, hold time.Duration) (decision.Field, bool, string) {
	if !f.Set {
		return f, false, "not set: the original value is kept"
	}
	if f.Source != decision.SourceAlgorithm || f.Floored {
		return f, false, "taken at once: " + string(f.Source) + " overrides the spec"
	}
	if prev <= 0 || !prevFromAlgorithm {
		return f, false, "taken: no forecast-made spec in force yet"
	}
	if (lo > 0 && prev < lo) || (hi > 0 && prev > hi) {
		return f, false, "taken: the spec in force is outside the module's bounds"
	}
	change := math.Abs(float64(f.Value-prev)) / float64(prev)
	if change <= ratio {
		f.Value = prev
		f.Reason += fmt.Sprintf("; the Pod spec is kept at %d: the forecast moved %.1f%%, within the %.0f%% that changes it", prev, change*100, ratio*100)
		return f, true, fmt.Sprintf("kept: the forecast moved %.1f%%, within %.0f%%", change*100, ratio*100)
	}
	if f.Value < prev && changedAt != nil && now.Sub(*changedAt) < hold {
		f.Value = prev
		f.Reason += fmt.Sprintf("; the Pod spec is kept at %d: a decrease waits until the spec has stood for %s (changed %s ago)", prev, hold, now.Sub(*changedAt).Round(time.Minute))
		return f, true, fmt.Sprintf("kept: a decrease of %.1f%% waits out the %s hold", change*100, hold)
	}
	return f, false, fmt.Sprintf("taken: the forecast moved %.1f%%, more than %.0f%%", change*100, ratio*100)
}

// pinResources rounds the CPU and memory of a decision to readable steps and keeps the spec in force unless it has to
// change. It edits rec in place and returns what to store with the snapshot.
func (s *Service) pinResources(ctx context.Context, m *model.ForecastModule, now time.Time, bounds decision.Bounds, rec *decision.Recommendation) (*ResourceSpecDetail, *model.Recommendation, error) {
	prev, err := s.Predictions.LatestRecommendation(ctx, m.ID) // nil when the module has none yet
	if err != nil {
		return nil, nil, err
	}
	ratio, hold := s.resourcePolicy(ctx)
	detail := &ResourceSpecDetail{Reasons: map[string]string{}, ChangePercent: int(math.Round(ratio * 100)), HoldHours: int(hold / time.Hour)}

	clamp := func(v, lo, hi int64) int64 {
		if hi > 0 && v > hi {
			return hi
		}
		if lo > 0 && v < lo {
			return lo
		}
		return v
	}
	if rec.CPUMilli.Set && rec.CPUMilli.Source == decision.SourceAlgorithm {
		rec.CPUMilli.Value = clamp(RoundCPUMilli(rec.CPUMilli.Value), bounds.MinCPUMilli, bounds.MaxCPUMilli)
	}
	if rec.MemoryBytes.Set && rec.MemoryBytes.Source == decision.SourceAlgorithm {
		rec.MemoryBytes.Value = clamp(RoundMemoryBytes(rec.MemoryBytes.Value), bounds.MinMemoryBytes, bounds.MaxMemoryBytes)
	}
	detail.Computed = ResourcePair{CPUMilli: rec.CPUMilli.Value, MemoryBytes: rec.MemoryBytes.Value}
	if !rec.CPUMilli.Set {
		detail.Computed.CPUMilli = 0
	}
	if !rec.MemoryBytes.Set {
		detail.Computed.MemoryBytes = 0
	}

	var prevCPU, prevMem int64
	var prevCPUAlgo, prevMemAlgo bool
	var changedAt *time.Time
	if prev != nil {
		if prev.CPUSet {
			prevCPU, prevCPUAlgo = prev.CPUMilli, prev.CPUSource == string(decision.SourceAlgorithm)
		}
		if prev.MemorySet {
			prevMem, prevMemAlgo = prev.MemoryBytes, prev.MemorySource == string(decision.SourceAlgorithm)
		}
		changedAt = prev.ResourceChangedAt
	}
	var heldCPU, heldMem bool
	rec.CPUMilli, heldCPU, detail.Reasons["cpu"] = pinOne(rec.CPUMilli, prevCPU, prevCPUAlgo, bounds.MinCPUMilli, bounds.MaxCPUMilli, now, changedAt, ratio, hold)
	rec.MemoryBytes, heldMem, detail.Reasons["memory"] = pinOne(rec.MemoryBytes, prevMem, prevMemAlgo, bounds.MinMemoryBytes, bounds.MaxMemoryBytes, now, changedAt, ratio, hold)
	detail.Held = heldCPU || heldMem
	if rec.CPUMilli.Set {
		detail.Pinned.CPUMilli = rec.CPUMilli.Value
	}
	if rec.MemoryBytes.Set {
		detail.Pinned.MemoryBytes = rec.MemoryBytes.Value
	}

	// the spec has a new version when what the snapshot carries differs from what the one before carried
	switch {
	case prev == nil:
		detail.Version, detail.Changed = 1, true
		detail.ChangedAt = &now
	case prev.CPUMilli != detail.Pinned.CPUMilli || prev.MemoryBytes != detail.Pinned.MemoryBytes || prev.CPUSet != rec.CPUMilli.Set || prev.MemorySet != rec.MemoryBytes.Set:
		detail.Version, detail.Changed = prev.ResourceVersion+1, true
		detail.ChangedAt = &now
	default:
		detail.Version, detail.ChangedAt = prev.ResourceVersion, prev.ResourceChangedAt
		if detail.Version == 0 {
			detail.Version = 1
		}
		if detail.ChangedAt == nil {
			t := prev.CreatedAt
			detail.ChangedAt = &t
		}
	}
	return detail, prev, nil
}
