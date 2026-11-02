package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Candle is the actual value of a metric over one bucket of the chart: the first, highest, lowest and last value and the
// mean. A bucket is one or more whole steps of the dataset, so a candle is a real summary of what Prometheus returned
// for the window, not an interpolation.
type Candle struct {
	Time  time.Time `json:"time"`
	Open  float64   `json:"open"`
	High  float64   `json:"high"`
	Low   float64   `json:"low"`
	Close float64   `json:"close"`
	Mean  float64   `json:"mean"`
	Count int       `json:"count"`
}

// LiveSeries is a metric read from Prometheus for a window, as the dataset of the module shapes it. It is read at the
// moment of the request, so it holds the history from before the module existed; nothing is read from the collected
// samples. Missing is true only when Prometheus returned no sample in the whole window.
type LiveSeries struct {
	Metric        string            `json:"metric"`
	Unit          string            `json:"unit"`
	StepSeconds   int               `json:"step_seconds"`
	BucketSeconds int               `json:"bucket_seconds"`
	Candles       []Candle          `json:"candles"`
	Quality       collector.Quality `json:"quality"`
	Missing       bool              `json:"missing"`
	Query         string            `json:"query"`
}

// maxLiveWindowSteps bounds the steps one request may ask Prometheus for.
const maxLiveWindowSteps = 100000

// LiveHistory reads the metric of a module from Prometheus for [from, to].
func (s *Service) LiveHistory(ctx context.Context, moduleID uint, metric string, from, to time.Time, maxPoints int) (*LiveSeries, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	m := dataOf(mod)
	setup, err := s.setup(ctx, m, metric)
	if err != nil {
		return nil, err
	}
	step := setup.step
	from, to = floorToStep(from, step), floorToStep(to, step)
	if to.Before(from) {
		return nil, fmt.Errorf("from must not be after to")
	}
	expected := int(to.Sub(from)/step) + 1
	if expected > maxLiveWindowSteps {
		return nil, fmt.Errorf("the window holds more than %d steps; choose a shorter range", maxLiveWindowSteps)
	}
	query, err := collector.RenderMapping(setup.mapping, target(m))
	if err != nil {
		return nil, err
	}
	querier, err := s.NewQuerier(setup.ds)
	if err != nil {
		return nil, err
	}
	src := m.Source(metric)
	shaping := collector.Shaping{Step: step, QueryStep: time.Duration(setup.mapping.StepSeconds) * time.Second,
		Aggregation: src.Aggregation, Scale: src.Scale, Precision: src.Precision}
	if err := shaping.Check(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNotConfigured, metric, err)
	}
	opt := s.FetchOptions
	if opt.Concurrency == 0 {
		opt.Concurrency = setup.ds.MaxConcurrency
	}
	fetched, err := collector.Fetch(ctx, querier, query, shaping.FetchStart(from), to, shaping.QueryStep, opt)
	if err != nil {
		return nil, err
	}
	points, err := collector.Shape(fetched, shaping)
	if err != nil {
		return nil, err
	}
	kept := points[:0:0]
	for _, p := range points {
		if p.Time.Before(from) || p.Time.After(to) || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		kept = append(kept, p)
	}
	out := &LiveSeries{Metric: metric, Unit: unitOf(m, metric, setup.mapping), StepSeconds: int(step / time.Second), Query: query,
		Quality: collector.Analyze(kept, from, to, step, s.now()), Missing: len(kept) == 0}
	out.Candles, out.BucketSeconds = candles(kept, from, step, expected, maxPoints)
	return out, nil
}

// unitOf is the unit of a source: the unit of its kind, or the one its mapping declares for a custom metric.
func unitOf(m *model.AlgorithmRun, metric string, mapping *model.MetricMapping) string {
	if u := MetricUnit(m.KindOf(metric)); u != "" {
		return u
	}
	return mapping.Unit
}

// candles groups the points into buckets of whole steps so that at most maxPoints remain.
func candles(points []collector.Point, from time.Time, step time.Duration, expected, maxPoints int) ([]Candle, int) {
	if maxPoints <= 0 {
		maxPoints = DefaultHistoryPoints
	}
	if maxPoints > maxHistoryPoints {
		maxPoints = maxHistoryPoints
	}
	bucketSteps := (expected + maxPoints - 1) / maxPoints
	if bucketSteps < 1 {
		bucketSteps = 1
	}
	bucket := time.Duration(bucketSteps) * step
	out := []Candle{}
	var cur *Candle
	var sum float64
	flush := func() {
		if cur != nil {
			cur.Mean = sum / float64(cur.Count)
			out = append(out, *cur)
		}
	}
	index := int64(-1)
	for _, p := range points {
		idx := int64(p.Time.Sub(from) / bucket)
		if idx != index || cur == nil {
			flush()
			index = idx
			cur = &Candle{Time: from.Add(time.Duration(idx) * bucket), Open: p.Value, High: p.Value, Low: p.Value}
			sum = 0
		}
		cur.Close = p.Value
		cur.High = math.Max(cur.High, p.Value)
		cur.Low = math.Min(cur.Low, p.Value)
		cur.Count++
		sum += p.Value
	}
	flush()
	return out, int(bucket / time.Second)
}

// ComparePoint is what one algorithm forecast for one bucket of the chart.
type ComparePoint struct {
	Time time.Time `json:"time"`
	// Value is the mean of the forecasts of the bucket. PredictedAt is the newest prediction time among them: the
	// forecast that applies to a target time is the latest one made before it.
	Value       float64   `json:"value"`
	PredictedAt time.Time `json:"predicted_at"`
	// Replicas is what the forecast needs in Pods, from the capacity of one Pod; nil when the module does not state it.
	Replicas *int `json:"replicas"`
}

// CompareAlgorithm is the forecasts of one algorithm.
type CompareAlgorithm struct {
	AlgorithmID string         `json:"algorithm_id"`
	Points      []ComparePoint `json:"points"`
}

// Compare puts the actual values of a metric next to the forecasts of every algorithm, on one time grid.
type Compare struct {
	Metric string `json:"metric"`
	Unit   string `json:"unit"`
	// Actual and ActualReplicas are read from Prometheus through the mappings of the same module (the same namespace and
	// workload), so the metric and the replica count describe the same group of Pods. ActualReplicas is nil when the module
	// has no replica source.
	Actual         *LiveSeries        `json:"actual"`
	ActualReplicas *LiveSeries        `json:"actual_replicas"`
	Algorithms     []CompareAlgorithm `json:"algorithms"`
	// Statuses has every enabled algorithm, with the reason for the ones that have no forecast in the window.
	Statuses []AlgorithmStatus `json:"statuses"`
	// ReplicaBasis says how a predicted replica count is derived, or why there is none.
	ReplicaBasis  string    `json:"replica_basis"`
	BucketSeconds int       `json:"bucket_seconds"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	// ActualError is why the actual values are missing when Prometheus could not be read; the forecasts are still served.
	ActualError string `json:"actual_error,omitempty"`
}

// CompareOptions selects what Compare returns.
type CompareOptions struct {
	Metric     string
	From, To   time.Time
	Algorithms []string // empty = every algorithm with forecasts in the window
	MaxPoints  int
	// WithPredictions false returns the actual values only (the metric curve page).
	WithPredictions bool
}

// Compare builds the comparison of a module metric for a window.
func (s *Service) Compare(ctx context.Context, moduleID uint, o CompareOptions) (*Compare, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	m := dataOf(mod)
	if m.MappingID(o.Metric) == 0 {
		return nil, fmt.Errorf("metric must be the key of one of the module's metric sources")
	}
	out := &Compare{Metric: o.Metric, Unit: MetricUnit(m.KindOf(o.Metric)), Algorithms: []CompareAlgorithm{}, Statuses: []AlgorithmStatus{}, From: o.From, To: o.To}

	actual, aerr := s.LiveHistory(ctx, moduleID, o.Metric, o.From, o.To, o.MaxPoints)
	switch {
	case aerr == nil:
		out.Actual, out.Unit, out.BucketSeconds = actual, actual.Unit, actual.BucketSeconds
	default:
		out.ActualError = aerr.Error()
	}
	if rs := m.FirstOfKind("replicas"); rs != nil && rs.Key != o.Metric {
		if live, err := s.LiveHistory(ctx, moduleID, rs.Key, o.From, o.To, o.MaxPoints); err == nil {
			out.ActualReplicas = live
			if out.BucketSeconds == 0 {
				out.BucketSeconds = live.BucketSeconds
			}
		}
	}
	if !o.WithPredictions {
		return out, nil
	}

	step := time.Duration(out.BucketSeconds) * time.Second
	if step <= 0 {
		// the actual values could not be read: bucket by the step of the dataset
		step = time.Minute
		if m.StepSeconds > 0 {
			step = time.Duration(m.StepSeconds) * time.Second
		}
		out.BucketSeconds = int(step / time.Second)
	}
	rows, err := s.Predictions.ForCompare(ctx, moduleID, o.Metric, o.From, o.To, o.Algorithms)
	if err != nil {
		return nil, err
	}
	capacity, basis := replicaCapacity(mod, o.Metric)
	out.ReplicaBasis = basis
	type key struct {
		algorithm string
		target    time.Time
	}
	latest := map[key]model.Prediction{}
	for _, p := range rows {
		if p.QuantilePermille != 0 && p.QuantilePermille != 500 {
			continue
		}
		k := key{p.AlgorithmID, p.TargetTime.UTC()}
		// the forecast of a target time is the newest one made at or before it; one made later would be hindsight
		if p.PredictionTime.After(p.TargetTime) {
			continue
		}
		if cur, ok := latest[k]; !ok || p.PredictionTime.After(cur.PredictionTime) {
			latest[k] = p
		}
	}
	type acc struct {
		sum  float64
		n    int
		last time.Time
	}
	by := map[string]map[int64]*acc{}
	anchor := o.From.UTC().Truncate(step)
	for k, p := range latest {
		idx := int64(k.target.Sub(anchor) / step)
		if by[k.algorithm] == nil {
			by[k.algorithm] = map[int64]*acc{}
		}
		a := by[k.algorithm][idx]
		if a == nil {
			a = &acc{}
			by[k.algorithm][idx] = a
		}
		a.sum += p.Value
		a.n++
		if p.PredictionTime.After(a.last) {
			a.last = p.PredictionTime
		}
	}
	// The curves are the algorithms that run for the module now. One that was disabled or removed has forecasts in the stored history, but
	// it is no longer an algorithm of the module and does not belong on the chart. When the algorithms cannot be read the stored ones show.
	runs, runsErr := s.runsOf(ctx, mod)
	current := map[string]bool{}
	for _, inst := range runs {
		current[inst.Key()] = true
	}
	ids := make([]string, 0, len(by))
	for id := range by {
		if runsErr == nil && !current[id] {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		idxs := make([]int64, 0, len(by[id]))
		for i := range by[id] {
			idxs = append(idxs, i)
		}
		sort.Slice(idxs, func(a, b int) bool { return idxs[a] < idxs[b] })
		alg := CompareAlgorithm{AlgorithmID: id, Points: make([]ComparePoint, 0, len(idxs))}
		for _, i := range idxs {
			a := by[id][i]
			v := a.sum / float64(a.n)
			pt := ComparePoint{Time: anchor.Add(time.Duration(i) * step), Value: v, PredictedAt: a.last.UTC()}
			if capacity != nil {
				r := capacity(v)
				pt.Replicas = &r
			}
			alg.Points = append(alg.Points, pt)
		}
		out.Algorithms = append(out.Algorithms, alg)
	}
	if runsErr == nil {
		for _, inst := range runs {
			_, has := by[inst.Key()]
			out.Statuses = append(out.Statuses, s.AlgorithmStatus(ctx, moduleID, o.Metric, inst.Key(), has, noForecastsInRange(o.From, o.To)))
		}
	}
	return out, nil
}

// replicaCapacity returns how many Pods a forecast of the metric needs, from the module's own capacity statement: only
// a CPU forecast of a module with a stated Pod CPU request and target utilization has one. The text says how it is
// derived, or why it is not.
func replicaCapacity(m *model.ForecastModule, metric string) (func(float64) int, string) {
	if !strings.HasPrefix(metric, "cpu") {
		return nil, "only a CPU forecast has a replica count"
	}
	if m.ReplicaMode != model.ReplicaModeDynamic || m.PodCPURequestMilli <= 0 {
		return nil, "the module does not state the CPU capacity of one Pod (dynamic replicas with a Pod CPU request)"
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
	return func(v float64) int {
			n := int(math.Ceil(v * headroom / perPod))
			if n < m.MinReplicas {
				n = m.MinReplicas
			}
			if m.MaxReplicas > 0 && n > m.MaxReplicas {
				n = m.MaxReplicas
			}
			if n < 1 {
				n = 1
			}
			return n
		}, fmt.Sprintf("ceil(forecast x headroom %.2f / (Pod CPU request %dm x target utilization %.0f%%)), kept within the module replica bounds",
			headroom, m.PodCPURequestMilli, target*100)
}
