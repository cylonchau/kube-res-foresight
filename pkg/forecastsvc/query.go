package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const (
	// DefaultQueryPoints and MaxQueryPoints bound the points of one series in a query response.
	DefaultQueryPoints = 1000
	MaxQueryPoints     = 5000
	// maxQueryRange bounds how far back a query may look.
	maxQueryRange = 365 * 24 * time.Hour
)

// ErrBadQuery is returned for a query that cannot be answered as asked.
var ErrBadQuery = errors.New("invalid metric query")

// QueryParams selects a metric of a module and a time range.
type QueryParams struct {
	Name      string
	ModuleID  uint
	Metric    string // the source key (required for the prediction metrics)
	Algorithm string
	From, To  time.Time
	// Step is the width of one bucket; zero picks the smallest step that keeps a series within MaxPoints.
	Step      time.Duration
	MaxPoints int
}

// Point is one bucket of a series.
type Point struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

// Series is one label combination of a result.
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []Point           `json:"points"`
}

// QueryResult is the answer to a metric query.
type QueryResult struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Unit        string   `json:"unit"`
	Agg         string   `json:"agg"`
	From        int64    `json:"from"`
	To          int64    `json:"to"`
	StepSeconds int      `json:"step_seconds"`
	Series      []Series `json:"series"`
	// Truncated is true when the source rows exceeded the read limit; the buckets then cover only part of the range.
	Truncated bool `json:"truncated"`
}

// Query returns a metric of the catalog as time series aligned to a step grid. Several series of the same
// metric share the grid, so algorithms can be compared bucket by bucket.
func (s *Service) Query(ctx context.Context, p QueryParams) (*QueryResult, error) {
	def, ok := LookupMetric(p.Name)
	if !ok {
		return nil, fmt.Errorf("%w: unknown metric %q", ErrBadQuery, p.Name)
	}
	if p.ModuleID == 0 {
		return nil, fmt.Errorf("%w: module_id is required", ErrBadQuery)
	}
	unit := ""
	if p.Metric != "" {
		if def.Group == GroupExecution {
			return nil, fmt.Errorf("%w: %s has no metric filter", ErrBadQuery, def.Name)
		}
		mod, err := s.Modules.Get(ctx, p.ModuleID)
		if err != nil {
			return nil, err
		}
		src := dataOf(mod).Source(p.Metric)
		if src == nil {
			return nil, fmt.Errorf("%w: metric must be the key of one of the module's metric sources %v", ErrBadQuery, dataOf(mod).ConfiguredMetrics())
		}
		unit = model.MetricKinds[src.Kind]
	}
	if def.Name == "prediction_value" || def.Name == "prediction_peak" || def.Name == "usage_source" {
		if p.Metric == "" {
			return nil, fmt.Errorf("%w: metric is required for %s, its unit depends on it", ErrBadQuery, def.Name)
		}
	}
	to := p.To
	if to.IsZero() {
		to = s.now()
	}
	from := p.From
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !to.After(from) {
		return nil, fmt.Errorf("%w: to must be after from", ErrBadQuery)
	}
	if to.Sub(from) > maxQueryRange {
		return nil, fmt.Errorf("%w: the range must not exceed %d days", ErrBadQuery, int(maxQueryRange/(24*time.Hour)))
	}
	maxPoints := p.MaxPoints
	if maxPoints <= 0 {
		maxPoints = DefaultQueryPoints
	}
	if maxPoints > MaxQueryPoints {
		return nil, fmt.Errorf("%w: max_points must be at most %d", ErrBadQuery, MaxQueryPoints)
	}
	step := p.Step
	// an epoch-aligned grid over a range can have one bucket more than range/step, hence maxPoints-1
	minStep := time.Duration(math.Ceil(float64(to.Sub(from))/float64(max(maxPoints-1, 1))/float64(time.Second))) * time.Second
	if step <= 0 {
		step = minStep
	}
	if step < minStep {
		return nil, fmt.Errorf("%w: step %s would return more than %d points; use at least %s", ErrBadQuery, step, maxPoints, minStep)
	}
	if step < time.Second {
		step = time.Second
	}

	points, truncated, err := def.load(ctx, s, loadParams{ownerID: p.ModuleID, metric: p.Metric, algorithm: p.Algorithm, from: from, to: to})
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Name: def.Name, Group: def.Group, Unit: def.Unit, Agg: def.Agg, From: from.Unix(), To: to.Unix(),
		StepSeconds: int(step / time.Second), Truncated: truncated, Series: []Series{}}
	if def.Unit == "metric" || def.Unit == "source" {
		res.Unit = unit
	}
	res.Series = bucketize(points, def.Agg, int64(step/time.Second))
	return res, nil
}

// bucketize groups points by label set and combines the points of each epoch-aligned step.
func bucketize(points []rawPoint, agg string, stepSec int64) []Series {
	type acc struct {
		sum, max, last float64
		lastTS         int64
		n              int
	}
	type seriesAcc struct {
		labels  map[string]string
		buckets map[int64]*acc
	}
	all := map[string]*seriesAcc{}
	for _, pt := range points {
		if math.IsNaN(pt.value) || math.IsInf(pt.value, 0) {
			continue
		}
		key := labelKey(pt.labels)
		sa := all[key]
		if sa == nil {
			sa = &seriesAcc{labels: pt.labels, buckets: map[int64]*acc{}}
			all[key] = sa
		}
		ts := pt.ts.Unix()
		bucket := ts - ((ts%stepSec)+stepSec)%stepSec
		a := sa.buckets[bucket]
		if a == nil {
			a = &acc{max: math.Inf(-1)}
			sa.buckets[bucket] = a
		}
		a.sum += pt.value
		a.n++
		a.max = math.Max(a.max, pt.value)
		if ts >= a.lastTS {
			a.last, a.lastTS = pt.value, ts
		}
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Series, 0, len(keys))
	for _, k := range keys {
		sa := all[k]
		starts := make([]int64, 0, len(sa.buckets))
		for b := range sa.buckets {
			starts = append(starts, b)
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
		series := Series{Labels: sa.labels, Points: make([]Point, 0, len(starts))}
		for _, b := range starts {
			a := sa.buckets[b]
			v := a.sum / float64(a.n)
			switch agg {
			case AggMax:
				v = a.max
			case AggLast:
				v = a.last
			}
			series.Points = append(series.Points, Point{TS: b, Value: v})
		}
		out = append(out, series)
	}
	return out
}
