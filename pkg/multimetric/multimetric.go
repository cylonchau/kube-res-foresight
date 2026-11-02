// Package multimetric turns the values of several metric sources into one recommendation: each source is reduced to
// a demand with its own statistic, divided by its trigger threshold, and the sources of one resource are combined
// by weight (or by maximum, sum, or the highest-priority one). It is a pure function, so a live recommendation and
// a backtest that replays predicted and measured values run exactly the same calculation, and every figure it
// produces is also returned as a row that explains it.
package multimetric

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Resources a source can feed.
const (
	ResourceCPU      = "cpu"
	ResourceMemory   = "memory"
	ResourceGPU      = "gpu"
	ResourceReplicas = "replicas" // a throughput signal such as requests per second
	ResourceSignal   = "signal"   // pressure only (OOM events, network, custom metrics)
)

// Statistics a source can be reduced with.
const (
	StatAvg  = "avg"
	StatP50  = "p50"
	StatP90  = "p90"
	StatP95  = "p95"
	StatP99  = "p99"
	StatMax  = "max"
	StatSum  = "sum"
	StatLast = "last"
)

// Combine modes of the sources of one resource.
const (
	CombineWeighted = "weighted" // weighted mean of the usable sources (the default)
	CombineMax      = "max"
	CombineSum      = "sum"
	CombinePrimary  = "primary" // the usable source with the lowest priority number only
)

// Level is the quantile a statistic of the form "p<number>" asks for, as a level in (0, 1): "p40" is 0.4, "p99.9" is 0.999. Any number
// above 0 and below 100 is a statistic (p41 and p42 as well as p95): none is fixed in the program.
func Level(stat string) (float64, bool) {
	rest, ok := strings.CutPrefix(stat, "p")
	if !ok || rest == "" || strings.ContainsAny(rest, "+-eE xX_") {
		return 0, false
	}
	n, err := strconv.ParseFloat(rest, 64)
	if err != nil || math.IsNaN(n) || n <= 0 || n >= 100 {
		return 0, false
	}
	return n / 100, true
}

// ValidStat reports whether the statistic is known: avg, max, sum, last, or p<number> for a percentile.
func ValidStat(s string) bool {
	switch s {
	case StatAvg, StatMax, StatSum, StatLast:
		return true
	}
	_, ok := Level(s)
	return ok && len(s) <= MaxStatLength
}

// MaxStatLength is the longest statistic that is stored (the column is 8 characters): "p99.999" fits, "p99.99999" does not.
const MaxStatLength = 8

// ValidCombine reports whether the combine mode is known.
func ValidCombine(s string) bool {
	switch s {
	case CombineWeighted, CombineMax, CombineSum, CombinePrimary:
		return true
	}
	return false
}

// Source is one metric source as the calculation sees it.
type Source struct {
	Key      string
	Name     string
	Kind     string
	Resource string
	Priority int // lower number = preferred; only decides CombinePrimary and ties
	Weight   float64
	Stat     string
	// Threshold is the demand at which the source asks for more capacity; 0 = the source has no trigger and only
	// feeds its resource.
	Threshold float64
}

// Input is a source with the values the calculation reduces: the forecast over the horizon, or the measured window.
type Input struct {
	Source
	Values []float64
	// Missing marks a source that has no usable values (no data, forecast failed); Reason says why.
	Missing bool
	Reason  string
}

// Config is the module-level part of the calculation.
type Config struct {
	// Combine maps a resource to its combine mode; absent = weighted.
	Combine map[string]string
}

// Row explains one source in the result.
type Row struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Resource  string  `json:"resource"`
	Stat      string  `json:"stat"`
	Priority  int     `json:"priority"`
	Weight    float64 `json:"weight"`
	Demand    float64 `json:"demand"`
	Threshold float64 `json:"threshold,omitempty"`
	Ratio     float64 `json:"ratio,omitempty"`
	// Share is the weight of the source among those combined into its resource (0 when it did not take part).
	Share     float64 `json:"share"`
	Used      bool    `json:"used"`
	Triggered bool    `json:"triggered,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// ResourceValue is the combined demand of one resource.
type ResourceValue struct {
	Value   float64  `json:"value"`
	Combine string   `json:"combine"`
	Sources []string `json:"sources"`
}

// Result is the outcome of one calculation.
type Result struct {
	Rows      []Row                    `json:"rows"`
	Resources map[string]ResourceValue `json:"resources"`
	// Pressure is the weighted mean of ratio over the sources that have a threshold; MaxRatio is the largest ratio.
	Pressure float64 `json:"pressure"`
	MaxRatio float64 `json:"max_ratio"`
	// Triggered is true when any source is at or over its threshold.
	Triggered bool `json:"triggered"`
	// Notes are sentences about sources that could not be used.
	Notes []string `json:"notes,omitempty"`
}

// Reduce applies a statistic to values. An empty slice has no statistic.
func Reduce(stat string, values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	switch stat {
	case StatSum:
		var s float64
		for _, v := range values {
			s += v
		}
		return s, true
	case StatLast:
		return values[len(values)-1], true
	case StatMax:
		m := values[0]
		for _, v := range values {
			m = math.Max(m, v)
		}
		return m, true
	case StatAvg, "":
		var s float64
		for _, v := range values {
			s += v
		}
		return s / float64(len(values)), true
	}
	q, ok := Level(stat)
	if !ok {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	pos := q * float64(len(sorted)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo], true
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac, true
}

// Compute reduces every input, combines them per resource and computes the pressure.
func Compute(cfg Config, in []Input) Result {
	res := Result{Resources: map[string]ResourceValue{}, Rows: make([]Row, 0, len(in))}
	type usable struct {
		idx    int
		demand float64
	}
	byResource := map[string][]usable{}
	for i, src := range in {
		row := Row{Key: src.Key, Name: src.Name, Kind: src.Kind, Resource: src.Resource, Stat: src.Stat, Priority: src.Priority,
			Weight: src.Weight, Threshold: src.Threshold}
		if src.Missing {
			row.Note = src.Reason
			if row.Note == "" {
				row.Note = "no usable values"
			}
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %s", src.Name, row.Note))
		} else if demand, ok := Reduce(src.Stat, src.Values); !ok {
			row.Note = "no values to reduce"
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %s", src.Name, row.Note))
		} else {
			row.Demand, row.Used = demand, true
			if src.Threshold > 0 {
				row.Ratio = demand / src.Threshold
				row.Triggered = row.Ratio >= 1
			}
			byResource[src.Resource] = append(byResource[src.Resource], usable{i, demand})
		}
		res.Rows = append(res.Rows, row)
	}

	for resource, list := range byResource {
		mode := cfg.Combine[resource]
		if !ValidCombine(mode) {
			mode = CombineWeighted
		}
		value := 0.0
		switch mode {
		case CombineMax:
			for k, u := range list {
				if k == 0 || u.demand > value {
					value = u.demand
				}
				res.Rows[u.idx].Share = 1
			}
		case CombineSum:
			for _, u := range list {
				value += u.demand
				res.Rows[u.idx].Share = 1
			}
		case CombinePrimary:
			best := list[0]
			for _, u := range list[1:] {
				if in[u.idx].Priority < in[best.idx].Priority {
					best = u
				}
			}
			value = best.demand
			res.Rows[best.idx].Share = 1
		default:
			var wsum float64
			for _, u := range list {
				wsum += math.Max(in[u.idx].Weight, 0)
			}
			for _, u := range list {
				share := 1 / float64(len(list))
				if wsum > 0 {
					share = math.Max(in[u.idx].Weight, 0) / wsum
				}
				value += share * u.demand
				res.Rows[u.idx].Share = share
			}
		}
		rv := ResourceValue{Value: value, Combine: mode}
		for _, u := range list {
			if res.Rows[u.idx].Share > 0 {
				rv.Sources = append(rv.Sources, in[u.idx].Key)
			}
		}
		res.Resources[resource] = rv
	}

	// the pressure: every source that has a threshold, by weight
	var wsum, psum float64
	var counted int
	for i := range res.Rows {
		row := &res.Rows[i]
		if !row.Used || row.Threshold <= 0 {
			continue
		}
		counted++
		w := math.Max(row.Weight, 0)
		wsum += w
		psum += w * row.Ratio
		res.MaxRatio = math.Max(res.MaxRatio, row.Ratio)
		if row.Triggered {
			res.Triggered = true
		}
	}
	switch {
	case counted == 0:
	case wsum > 0:
		res.Pressure = psum / wsum
	default: // all weights are zero: the sources count equally
		var s float64
		for _, row := range res.Rows {
			if row.Used && row.Threshold > 0 {
				s += row.Ratio
			}
		}
		res.Pressure = s / float64(counted)
	}
	return res
}

// ReplicasFor turns the pressure into a replica count for the current one. A source at or over its threshold asks
// for capacity in proportion to its ratio whatever its weight (a breach is not averaged away); below every
// threshold the weighted pressure decides, which is what lets the count come down. At least one replica is returned
// when current is positive.
func (r Result) ReplicasFor(current int) (replicas int, why string) {
	if current <= 0 {
		return 0, "the current replica count is unknown"
	}
	if r.Pressure == 0 && r.MaxRatio == 0 {
		return current, "no source has a trigger threshold"
	}
	factor, why := r.Pressure, fmt.Sprintf("weighted pressure %.3g", r.Pressure)
	if r.Triggered {
		factor, why = r.MaxRatio, fmt.Sprintf("a source is over its threshold (ratio %.3g)", r.MaxRatio)
	}
	n := int(math.Ceil(float64(current) * factor))
	if n < 1 {
		n = 1
	}
	return n, fmt.Sprintf("%s: ceil(%d x %.3g) = %d", why, current, factor, n)
}

// Describe is a one-line summary of a result for logs.
func (r Result) Describe() string {
	parts := make([]string, 0, len(r.Resources))
	keys := make([]string, 0, len(r.Resources))
	for k := range r.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.4g", k, r.Resources[k].Value))
	}
	return fmt.Sprintf("%s pressure=%.3g max=%.3g", strings.Join(parts, " "), r.Pressure, r.MaxRatio)
}
