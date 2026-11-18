package cost

import (
	"math"
	"sort"
)

// Why a diagnosis or a candidate has nothing to say (codes, the page words them).
const (
	ReasonNoRequest      = "no_request"             // the module does not say what a Pod requests, so a request cannot be compared with the use
	ReasonTooFewSamples  = "too_few_samples"        // fewer measured points than the window needs
	ReasonNoSafeValue    = "no_recommendation"      // the platform has made no recommendation to take the safe value from
	ReasonNotAboveSafe   = "current_not_above_safe" // the request is already at or under the safe value: there is nothing to give back
	ReasonBelowSafe      = "current_below_safe"     // the request is under the safe value: that is a risk to raise, not a saving
	ReasonNoUsage        = "no_usage_series"        // the module has no CPU or memory series that can be read per Pod
	ReasonExternalScaler = "external_hpa"           // an HPA or KEDA competes for the replicas: the target is left alone
)

// Usage is how much of a resource one Pod used over a window, in the unit of the resource (cores, bytes).
type Usage struct {
	Points int     `json:"points"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

// UsageOf takes the quantiles of the measured values by the nearest-rank rule; values that are not finite or are negative are dropped.
func UsageOf(values []float64) Usage {
	clean := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return Usage{}
	}
	sort.Float64s(clean)
	return Usage{Points: len(clean), P50: rank(clean, 50), P95: rank(clean, 95), P99: rank(clean, 99), Max: clean[len(clean)-1]}
}

func rank(sorted []float64, p float64) float64 {
	n := len(sorted)
	i := int(math.Ceil(p/100*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return sorted[i]
}

// Waste is the diagnosis of one resource of one workload: what a Pod requests, what it used, and how much of the request is more than
// the platform's own recommendation asks for.
type Waste struct {
	Resource string  `json:"resource"`
	Request  float64 `json:"request"`
	// Safe is the value of the platform's recommendation for a Pod, the lowest value the evidence supports; 0 when there is none.
	Safe  float64 `json:"safe"`
	Usage Usage   `json:"usage"`
	// Utilization is the P95 use over the request.
	Utilization float64 `json:"utilization"`
	// Idle is true when even the P99 use stays under the idle share of the request: the workload takes the resource and hardly uses it.
	Idle bool `json:"idle"`
	// Surplus is, per Pod, how much the request is above Safe (never below zero).
	Surplus float64 `json:"surplus"`
	Enough  bool    `json:"enough"`
	Note    string  `json:"note,omitempty"`
}

// Diagnose compares the request of a Pod with what it used. It says nothing when the request is unknown or the window has fewer than
// minPoints measured points; the use is still reported when there is one.
func Diagnose(resource string, request, safe float64, u Usage, idleRatio float64, minPoints int) Waste {
	w := Waste{Resource: resource, Request: request, Usage: u}
	if request > 0 {
		w.Utilization = u.P95 / request
	}
	switch {
	case !(request > 0):
		w.Note = ReasonNoRequest
		return w
	case u.Points < minPoints:
		w.Note = ReasonTooFewSamples
		return w
	}
	w.Enough = true
	w.Idle = idleRatio > 0 && u.P99 < idleRatio*request
	if !(safe > 0) {
		w.Note = ReasonNoSafeValue
		return w
	}
	w.Safe = safe
	w.Surplus = math.Max(0, request-safe)
	return w
}

// The three candidate levels, from the lowest value to the highest.
const (
	LevelAggressive   = "aggressive"
	LevelBalanced     = "balanced"
	LevelConservative = "conservative"
)

// Levels lists them in that order.
var Levels = []string{LevelAggressive, LevelBalanced, LevelConservative}

// Candidate is a value a request could be lowered to.
type Candidate struct {
	Level string  `json:"level"`
	Value float64 `json:"value"`
	// Saving is how much below the current request the value is, per Pod.
	Saving float64 `json:"saving"`
}

// Candidates gives the values a request could be lowered to, between the safe value and the current request. None is ever below the
// safe value (the recommendation, which already holds the OOM floor, the target utilization and the forecast margin: no second margin
// is put on it and no cut is taken off it) and none is above what runs now:
//
//	aggressive    the safe value itself
//	balanced      the P99 of the measured use plus the margin
//	conservative  the highest measured use plus the margin
//
// each kept between the safe value and the current request. round makes a value one a Pod spec can say (nil leaves it); a value it
// rounds out of that range is brought back into it. It returns nothing, with the reason, when there is nothing to give back or not
// enough evidence to say how much.
func Candidates(current, safe float64, u Usage, margin float64, minPoints int, round func(float64) float64) ([]Candidate, string) {
	switch {
	case !(current > 0):
		return nil, ReasonNoRequest
	case !(safe > 0):
		return nil, ReasonNoSafeValue
	case u.Points < minPoints:
		return nil, ReasonTooFewSamples
	case current < safe:
		return nil, ReasonBelowSafe
	case current == safe:
		return nil, ReasonNotAboveSafe
	}
	if math.IsNaN(margin) || margin < 0 {
		margin = 0
	}
	fit := func(v float64) float64 {
		if round != nil {
			v = round(v)
		}
		return math.Min(current, math.Max(safe, v))
	}
	values := map[string]float64{
		LevelAggressive:   fit(safe),
		LevelBalanced:     fit(u.P99 * (1 + margin)),
		LevelConservative: fit(u.Max * (1 + margin)),
	}
	// the order holds by construction (max is at least P99); the guard keeps it when rounding is odd
	values[LevelBalanced] = math.Max(values[LevelBalanced], values[LevelAggressive])
	values[LevelConservative] = math.Max(values[LevelConservative], values[LevelBalanced])
	out := make([]Candidate, 0, len(Levels))
	for _, level := range Levels {
		out = append(out, Candidate{Level: level, Value: values[level], Saving: current - values[level]})
	}
	if out[0].Saving <= 0 {
		// even the lowest level (the safe value, rounded to a spec) is not below what runs: nothing to give back
		return nil, ReasonNotAboveSafe
	}
	return out, ""
}
