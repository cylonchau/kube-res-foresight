// Package decision turns validated predictions, administrator overrides and fallbacks into one resource and
// replica recommendation. It is a pure function of its inputs: it never reads a clock, a database or the
// cluster, so every outcome can be reproduced and explained from the recorded inputs.
package decision

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Source says where a field value came from, in the order of precedence of the platform contract.
type Source string

const (
	SourceBypass      Source = "bypass"       // global/module bypass: the user's original value is kept
	SourceFixedPeriod Source = "fixed_period" // administrator override window
	SourceFixedCount  Source = "fixed_count"  // the module keeps a fixed replica count, no algorithm decides it
	SourceAlgorithm   Source = "algorithm"    // an eligible model that passed the safety checks
	SourceFallback    Source = "fallback"     // the module's fixed default
	SourceOriginal    Source = "original"     // nothing usable: keep the user's original value
	// SourceOOMFloor is a memory value that exists only because of the OOM protection: nothing else gave one.
	SourceOOMFloor Source = "oom_floor"
)

// Field is one recommended value. Set is false when the original value must be kept.
type Field struct {
	Set     bool   `json:"set"`
	Value   int64  `json:"value"`
	Source  Source `json:"source"`
	Detail  string `json:"detail,omitempty"` // algorithm id or fixed-period name
	Reason  string `json:"reason"`
	Clamped bool   `json:"clamped,omitempty"`
	// Floored is true when the OOM protection raised the value (or gave it); FloorCapped when the module's
	// maximum then cut it back below the floor.
	Floored     bool `json:"floored,omitempty"`
	FloorCapped bool `json:"floor_capped,omitempty"`
}

// Recommendation is the outcome for one module.
type Recommendation struct {
	CPUMilli    Field     `json:"cpu_milli"`
	MemoryBytes Field     `json:"memory_bytes"`
	Replicas    Field     `json:"replicas"`
	GPU         Field     `json:"gpu"`
	ValidUntil  time.Time `json:"valid_until"`
}

// Bounds are hard limits; zero means unbounded on that side. They apply to every source except bypass.
type Bounds struct {
	MinCPUMilli, MaxCPUMilli       int64
	MinMemoryBytes, MaxMemoryBytes int64
	MinReplicas, MaxReplicas       int
	MinGPU, MaxGPU                 int
}

// Fallback is the module's fixed default; zero means "no default" for that field.
type Fallback struct {
	CPUMilli    int64
	MemoryBytes int64
	Replicas    int
	GPU         int
}

// Policy is the module's static configuration.
type Policy struct {
	Bounds
	Fallback
	// HeadroomRatio multiplies the predicted peak (>= 1; 1 = no headroom).
	HeadroomRatio float64
	// PodCapacityRPS is the verified safe requests-per-second of one Ready pod. Zero disables replica
	// recommendations from the model: total RPS divided by guessed capacity would be invented precision.
	PodCapacityRPS float64
	Location       *time.Location // zone of weekly fixed periods; nil = UTC
	TTL            time.Duration
	// OOMFloorBytes, when positive, is the least memory the container may be given after a recent OOM kill. It
	// applies to every source except bypass: a fixed period or the module fallback is raised to it, and a memory
	// value that would otherwise be kept as the original is replaced by it.
	OOMFloorBytes  int64
	OOMFloorReason string
	// FixedReplicas, when positive, is a replica count the module keeps whatever the forecast says: a period fixing the replicas still wins, and the
	// floors and caps of periods still apply on top (see ApplyReplicaWindows). Metric pressure does not raise it.
	FixedReplicas int
}

// FixedPeriod is an administrator override. A weekly period repeats on Weekdays between StartMinute and
// EndMinute of the local day (EndMinute <= StartMinute crosses midnight into the next day); an absolute period
// uses AbsStart/AbsEnd instants. Only the fields set (> 0) override.
type FixedPeriod struct {
	Name        string
	Priority    int
	Weekdays    uint8 // bit n = time.Weekday(n)
	StartMinute int
	EndMinute   int
	AbsStart    *time.Time
	AbsEnd      *time.Time
	CPUMilli    int64
	MemoryBytes int64
	Replicas    int
	// ReplicaMode says how Replicas acts: "fixed" (or empty) pins the count, "min" is a floor and "max" a cap (see ApplyReplicaWindows).
	ReplicaMode string
}

// Candidate is a model result in metric units: the maximum predicted value over the horizon. Candidates are
// given in priority order and must already be eligible (baseline, or promoted by a backtest).
type Candidate struct {
	Algorithm string
	Peak      float64
}

// Inputs is everything a decision depends on.
type Inputs struct {
	Now          time.Time
	Bypass       bool
	BypassReason string
	Policy       Policy
	Periods      []FixedPeriod
	CPUCores     []Candidate
	MemoryBytes  []Candidate
	RPS          []Candidate
	// GPU candidates are in cards; the recommendation is a whole number of cards.
	GPU []Candidate
	// PressureReplicas, when positive, is the replica count the weighted metric pressure asks for (see
	// multimetric.Result.ReplicasFor). It raises the replica recommendation and never lowers it.
	PressureReplicas int
	PressureReason   string
}

// Active reports whether the period applies at t.
func (p FixedPeriod) Active(t time.Time, loc *time.Location) bool {
	if p.AbsStart != nil || p.AbsEnd != nil {
		if p.AbsStart == nil || p.AbsEnd == nil {
			return false
		}
		return !t.Before(*p.AbsStart) && t.Before(*p.AbsEnd)
	}
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	minute := local.Hour()*60 + local.Minute()
	day := uint(local.Weekday())
	if p.StartMinute < p.EndMinute {
		return p.Weekdays&(1<<day) != 0 && minute >= p.StartMinute && minute < p.EndMinute
	}
	// crosses midnight: the part before midnight belongs to the start day, the part after to the day before
	if minute >= p.StartMinute {
		return p.Weekdays&(1<<day) != 0
	}
	if minute < p.EndMinute {
		return p.Weekdays&(1<<((day+6)%7)) != 0
	}
	return false
}

// ValidatePeriod checks a period's own consistency.
func ValidatePeriod(p FixedPeriod) error {
	if p.Name == "" {
		return fmt.Errorf("period name is required")
	}
	if p.CPUMilli < 0 || p.MemoryBytes < 0 || p.Replicas < 0 {
		return fmt.Errorf("period values must not be negative")
	}
	if p.CPUMilli == 0 && p.MemoryBytes == 0 && p.Replicas == 0 {
		return fmt.Errorf("period must set at least one of cpu, memory or replicas")
	}
	if !ValidReplicaWindow(p.ReplicaMode) {
		return fmt.Errorf("replica_mode of a period must be fixed, min or max")
	}
	if (p.ReplicaMode == ReplicaWindowMin || p.ReplicaMode == ReplicaWindowMax) && p.Replicas == 0 {
		return fmt.Errorf("a period that is a floor or a cap of the replicas needs the replicas")
	}
	if p.AbsStart != nil || p.AbsEnd != nil {
		if p.AbsStart == nil || p.AbsEnd == nil || !p.AbsEnd.After(*p.AbsStart) {
			return fmt.Errorf("absolute period needs a start before its end")
		}
		return nil
	}
	if p.Weekdays == 0 || p.Weekdays >= 1<<7 {
		return fmt.Errorf("weekly period needs at least one weekday")
	}
	if p.StartMinute < 0 || p.StartMinute > 1439 || p.EndMinute < 0 || p.EndMinute > 1439 || p.StartMinute == p.EndMinute {
		return fmt.Errorf("start and end must be different minutes of the day (0-1439)")
	}
	return nil
}

func positive(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 }

const maxValue = 1e15

// Decide applies the precedence bypass > fixed period > algorithm > fallback > original to each field.
func Decide(in Inputs) Recommendation {
	rec := Recommendation{ValidUntil: in.Now.Add(in.Policy.TTL)}
	if in.Bypass {
		reason := "bypass: the original value is kept"
		if in.BypassReason != "" {
			reason += " (" + in.BypassReason + ")"
		}
		keep := Field{Source: SourceBypass, Reason: reason}
		rec.CPUMilli, rec.MemoryBytes, rec.Replicas, rec.GPU = keep, keep, keep, keep
		return rec
	}
	periods := activePeriods(in)
	headroom := in.Policy.HeadroomRatio
	if headroom < 1 {
		headroom = 1
	}
	b := in.Policy.Bounds

	rec.CPUMilli = decideField(fieldSpec{
		name: "cpu", lo: b.MinCPUMilli, hi: b.MaxCPUMilli, fallback: in.Policy.CPUMilli,
		period: func(p FixedPeriod) int64 { return p.CPUMilli },
		convert: func(c Candidate) (int64, string) {
			v := math.Ceil(c.Peak * headroom * 1000)
			return int64(v), fmt.Sprintf("peak %.4g cores x headroom %.3g", c.Peak, headroom)
		},
		candidates: in.CPUCores,
	}, periods)
	rec.MemoryBytes = decideField(fieldSpec{
		name: "memory", lo: b.MinMemoryBytes, hi: b.MaxMemoryBytes, fallback: in.Policy.MemoryBytes,
		period: func(p FixedPeriod) int64 { return p.MemoryBytes },
		convert: func(c Candidate) (int64, string) {
			v := math.Ceil(c.Peak * headroom)
			return int64(v), fmt.Sprintf("peak %.4g bytes x headroom %.3g", c.Peak, headroom)
		},
		candidates: in.MemoryBytes,
	}, periods)
	if f := in.Policy.OOMFloorBytes; f > 0 {
		rec.MemoryBytes = applyFloor(rec.MemoryBytes, f, in.Policy.OOMFloorReason, b.MaxMemoryBytes)
	}
	rec.GPU = decideField(fieldSpec{
		name: "gpu", lo: int64(b.MinGPU), hi: int64(b.MaxGPU), fallback: int64(in.Policy.GPU),
		period: func(FixedPeriod) int64 { return 0 },
		convert: func(c Candidate) (int64, string) {
			v := math.Ceil(c.Peak * headroom)
			if v < 1 {
				v = 1
			}
			return int64(v), fmt.Sprintf("demand %.4g cards x headroom %.3g, rounded up to whole cards", c.Peak, headroom)
		},
		candidates: in.GPU,
	}, periods)
	capacity := in.Policy.PodCapacityRPS
	rec.Replicas = decideField(fieldSpec{
		name: "replicas", lo: int64(b.MinReplicas), hi: int64(b.MaxReplicas), fallback: int64(in.Policy.Replicas),
		period: func(p FixedPeriod) int64 {
			if !p.overridesReplicas() {
				return 0
			}
			return int64(p.Replicas)
		},
		convert: func(c Candidate) (int64, string) {
			if !positive(capacity) {
				return 0, "pod capacity is not configured"
			}
			v := math.Ceil(c.Peak * headroom / capacity)
			if v < 1 {
				v = 1
			}
			return int64(v), fmt.Sprintf("peak %.4g rps x headroom %.3g / capacity %.4g rps per pod", c.Peak, headroom, capacity)
		},
		candidates: in.RPS,
	}, periods)
	if n := in.Policy.FixedReplicas; n > 0 && rec.Replicas.Source != SourceFixedPeriod {
		rec.Replicas = fixedCount(n, int64(b.MinReplicas), int64(b.MaxReplicas))
	}
	if in.PressureReplicas > 0 {
		rec.Replicas = applyPressure(rec.Replicas, in.PressureReplicas, in.PressureReason, b.MinReplicas, b.MaxReplicas)
	}
	return rec
}

// applyPressure raises the replica field to what the metric pressure asks for. A fixed period or a bypass is the
// administrator's word and stays; a lower pressure never lowers a value that came from the throughput formula.
func applyPressure(f Field, want int, why string, lo, hi int) Field {
	if f.Source == SourceBypass || f.Source == SourceFixedPeriod || f.Source == SourceFixedCount {
		return f
	}
	if f.Set && f.Source == SourceAlgorithm && f.Value >= int64(want) {
		return f
	}
	v := int64(want)
	if lo > 0 && v < int64(lo) {
		v = int64(lo)
	}
	if hi > 0 && v > int64(hi) {
		v = int64(hi)
	}
	if f.Set && f.Source == SourceAlgorithm && f.Value >= v {
		return f
	}
	reason := fmt.Sprintf("metric pressure asks for %d replicas (%s)", want, why)
	if f.Source == SourceAlgorithm && f.Set {
		reason = f.Reason + "; raised: " + reason
	}
	out := Field{Set: true, Value: v, Source: SourceAlgorithm, Detail: f.Detail, Reason: reason, Clamped: v != int64(want)}
	if out.Detail == "" {
		out.Detail = "metric-pressure"
	}
	return out
}

func activePeriods(in Inputs) []FixedPeriod {
	var active []FixedPeriod
	for _, p := range in.Periods {
		if ValidatePeriod(p) == nil && p.Active(in.Now, in.Policy.Location) {
			active = append(active, p)
		}
	}
	// highest priority first; equal priorities fall back to name so the outcome is deterministic
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Priority != active[j].Priority {
			return active[i].Priority > active[j].Priority
		}
		return active[i].Name < active[j].Name
	})
	return active
}

type fieldSpec struct {
	name       string
	lo, hi     int64
	fallback   int64
	period     func(FixedPeriod) int64
	convert    func(Candidate) (int64, string)
	candidates []Candidate
}

func decideField(s fieldSpec, periods []FixedPeriod) Field {
	clamp := func(v int64, f Field) Field {
		f.Set, f.Value = true, v
		if s.lo > 0 && v < s.lo {
			f.Value, f.Clamped = s.lo, true
			f.Reason += fmt.Sprintf("; raised to the minimum %d", s.lo)
		}
		if s.hi > 0 && f.Value > s.hi {
			f.Value, f.Clamped = s.hi, true
			f.Reason += fmt.Sprintf("; capped at the maximum %d", s.hi)
		}
		return f
	}

	var withValue []FixedPeriod
	for _, p := range periods {
		if s.period(p) > 0 {
			withValue = append(withValue, p)
		}
	}
	if len(withValue) > 0 {
		win := withValue[0]
		reason := fmt.Sprintf("fixed period %q", win.Name)
		if len(withValue) > 1 {
			if withValue[1].Priority == win.Priority {
				reason += fmt.Sprintf(" (conflicts with %q at equal priority; resolved by name)", withValue[1].Name)
			} else {
				reason += fmt.Sprintf(" (overrides %q by priority)", withValue[1].Name)
			}
		}
		return clamp(s.period(win), Field{Source: SourceFixedPeriod, Detail: win.Name, Reason: reason})
	}

	var skipped []string
	for _, c := range s.candidates {
		if !positive(c.Peak) && !(s.name == "replicas" && c.Peak == 0) {
			skipped = append(skipped, fmt.Sprintf("%s: invalid prediction", c.Algorithm))
			continue
		}
		if c.Peak > maxValue {
			skipped = append(skipped, fmt.Sprintf("%s: prediction out of range", c.Algorithm))
			continue
		}
		v, why := s.convert(c)
		if v <= 0 {
			skipped = append(skipped, fmt.Sprintf("%s: %s", c.Algorithm, why))
			continue
		}
		return clamp(v, Field{Source: SourceAlgorithm, Detail: c.Algorithm, Reason: why})
	}

	skippedNote := ""
	if len(skipped) > 0 {
		skippedNote = " (skipped: " + joinReasons(skipped) + ")"
	}
	if s.fallback > 0 {
		return clamp(s.fallback, Field{Source: SourceFallback, Reason: "module fallback" + skippedNote})
	}
	return Field{Source: SourceOriginal, Reason: "no usable prediction and no fallback: the original value is kept" + skippedNote}
}

func joinReasons(r []string) string {
	out := ""
	for i, s := range r {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// applyFloor raises a memory field to the OOM floor, then lets the module maximum have the last word (a value
// above the maximum could not be applied); that case is flagged so it is visible rather than silent.
func applyFloor(f Field, floor int64, why string, max int64) Field {
	if f.Source == SourceBypass {
		return f
	}
	if f.Set && f.Value >= floor {
		return f
	}
	f.Floored = true
	f.Value, f.Set = floor, true
	if f.Source == SourceOriginal {
		f.Source = SourceOOMFloor
	}
	f.Reason += fmt.Sprintf("; raised to the OOM floor %d bytes (%s)", floor, why)
	if max > 0 && f.Value > max {
		f.Value, f.Clamped, f.FloorCapped = max, true, true
		f.Reason += fmt.Sprintf("; the module maximum %d is below the floor and caps it", max)
	}
	return f
}
