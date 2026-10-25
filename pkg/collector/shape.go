package collector

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Shaping describes how the points a query returned become the values of a dataset: the grid step of the dataset,
// how the points of one dataset step are combined, the unit scale and the decimals kept.
type Shaping struct {
	// Step is the grid of the dataset; it has to be a whole multiple of the step the query was run with.
	Step time.Duration
	// QueryStep is the step of the query.
	QueryStep time.Duration
	// Aggregation is avg, max, min, last or sum (see model.SourceAggregations).
	Aggregation string
	Scale       float64
	// Precision is the number of decimals kept.
	Precision int
}

// Check refuses a shaping that cannot be applied: a dataset step finer than the query, or not a whole multiple of it.
func (s Shaping) Check() error {
	if s.QueryStep <= 0 || s.Step <= 0 {
		return fmt.Errorf("the step must be positive")
	}
	if s.Step < s.QueryStep {
		return fmt.Errorf("the dataset step (%s) is finer than the step of the query (%s): a finer grid cannot be made from coarser samples", s.Step, s.QueryStep)
	}
	if s.Step%s.QueryStep != 0 {
		return fmt.Errorf("the dataset step (%s) is not a whole multiple of the step of the query (%s)", s.Step, s.QueryStep)
	}
	return nil
}

// FetchStart is how far before the first dataset step the query has to start so the first bucket is complete.
func (s Shaping) FetchStart(first time.Time) time.Time {
	return first.Add(-(s.Step - s.QueryStep))
}

// Shape turns query points into dataset points on the grid of Step. A dataset point labelled t summarises the query
// samples in (t - Step, t], so its value never contains anything after t. Only complete buckets are produced: a bucket
// is complete when it has the sample at its label time (the newest bucket of a query is cut at the end of the
// window). The result is scaled and rounded to Precision decimals; NaN and infinite samples are skipped.
func Shape(points []Point, s Shaping) ([]Point, error) {
	if err := s.Check(); err != nil {
		return nil, err
	}
	scale := s.Scale
	if scale == 0 {
		scale = 1
	}
	pow := math.Pow(10, float64(s.Precision))
	finish := func(v float64) float64 {
		v *= scale
		return math.Round(v*pow) / pow
	}
	if s.Step == s.QueryStep {
		out := make([]Point, 0, len(points))
		for _, p := range points {
			if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
				continue
			}
			out = append(out, Point{Time: p.Time, Value: finish(p.Value)})
		}
		return out, nil
	}
	type bucket struct {
		values []float64
		hasEnd bool
	}
	buckets := map[int64]*bucket{}
	stepSec := int64(s.Step / time.Second)
	for _, p := range points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		ts := p.Time.Unix()
		// the label of the bucket the sample belongs to: the next multiple of the step at or after it
		label := ts
		if r := ((ts % stepSec) + stepSec) % stepSec; r != 0 {
			label = ts + stepSec - r
		}
		b := buckets[label]
		if b == nil {
			b = &bucket{}
			buckets[label] = b
		}
		b.values = append(b.values, p.Value)
		if ts == label {
			b.hasEnd = true
		}
	}
	labels := make([]int64, 0, len(buckets))
	for l, b := range buckets {
		if b.hasEnd {
			labels = append(labels, l)
		}
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i] < labels[j] })
	out := make([]Point, 0, len(labels))
	for _, l := range labels {
		out = append(out, Point{Time: time.Unix(l, 0).UTC(), Value: finish(aggregate(buckets[l].values, s.Aggregation))})
	}
	return out, nil
}

func aggregate(values []float64, how string) float64 {
	switch how {
	case "max":
		m := values[0]
		for _, v := range values {
			m = math.Max(m, v)
		}
		return m
	case "min":
		m := values[0]
		for _, v := range values {
			m = math.Min(m, v)
		}
		return m
	case "last":
		return values[len(values)-1]
	case "sum":
		var t float64
		for _, v := range values {
			t += v
		}
		return t
	}
	var t float64
	for _, v := range values {
		t += v
	}
	return t / float64(len(values))
}
