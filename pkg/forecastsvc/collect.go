package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// CollectResult is the outcome of collecting one metric.
type CollectResult struct {
	Metric  string            `json:"metric"`
	From    time.Time         `json:"from"`
	To      time.Time         `json:"to"`
	Fetched int               `json:"fetched"`
	Stored  int               `json:"stored"`
	Pruned  int64             `json:"pruned"`
	Quality collector.Quality `json:"quality"`
	Error   string            `json:"error,omitempty"`
}

// Collect pulls the history of every metric source of a module that is missing from storage. The first run
// backfills LookbackHours; later runs continue from the newest stored sample. Re-collecting a window is idempotent.
// A failing metric does not stop the others; its error is reported in its result.
func (s *Service) Collect(ctx context.Context, moduleID uint) ([]CollectResult, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	m := dataOf(mod)
	metrics := configuredMetrics(m)
	if len(metrics) == 0 {
		return nil, fmt.Errorf("%w: the module has no metric source", ErrNotConfigured)
	}
	results := make([]CollectResult, 0, len(metrics))
	runs := s.runsForCollect(ctx, mod)
	for _, metric := range metrics {
		res := CollectResult{Metric: metric}
		started := time.Now()
		err := s.collectMetric(ctx, m, runs, metric, &res)
		s.Telemetry.recordCollect(metric, time.Since(started), err)
		if err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
	}
	return results, nil
}

func (s *Service) collectMetric(ctx context.Context, m *model.AlgorithmRun, runs []*model.AlgorithmRun, metric string, res *CollectResult) error {
	setup, err := s.setup(ctx, m, metric)
	if err != nil {
		return err
	}
	query, err := collector.RenderMapping(setup.mapping, target(m))
	if err != nil {
		return err
	}
	querier, err := s.NewQuerier(setup.ds)
	if err != nil {
		return err
	}
	src := m.Source(metric)
	shaping := collector.Shaping{Step: setup.step, QueryStep: time.Duration(setup.mapping.StepSeconds) * time.Second,
		Aggregation: src.Aggregation, Scale: src.Scale, Precision: src.Precision}
	if err := shaping.Check(); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotConfigured, metric, err)
	}
	series, err := s.Samples.EnsureSeries(ctx, m.ID, metric, setup.mapping.ID, int(setup.step/time.Second), src.Shape())
	if err != nil {
		return err
	}

	now := s.now()
	to := floorToStep(now, setup.step)
	// the range is the module's lookback, or longer when an enabled algorithm needs more steps than that holds
	hours := collectLookback(m, runs, setup.step)
	from := floorToStep(to.Add(-time.Duration(hours)*time.Hour), setup.step)
	if latest, ok, err := s.Samples.Latest(ctx, series.ID); err != nil {
		return err
	} else if ok && latest.After(from) {
		from = latest // one step of overlap: the newest sample may have been partial
		// A range that was just extended is not stored yet before its oldest sample: fetch it too, once the older part
		// exists the continuing collection is enough again (what Prometheus never had is asked for again, cheaply, each run).
		if hours > m.LookbackHours {
			if earliest, ok, err := s.Samples.Earliest(ctx, series.ID); err != nil {
				return err
			} else if ok && earliest.After(floorToStep(to.Add(-time.Duration(hours)*time.Hour), setup.step).Add(2*setup.step)) {
				from = floorToStep(to.Add(-time.Duration(hours)*time.Hour), setup.step)
			}
		}
	}
	res.From, res.To = from, to

	opt := s.FetchOptions
	if opt.Concurrency == 0 {
		opt.Concurrency = setup.ds.MaxConcurrency
	}
	// the query runs at its own step and starts early enough for the first bucket of the dataset to be complete
	fetched, err := collector.Fetch(ctx, querier, query, shaping.FetchStart(from), to, shaping.QueryStep, opt)
	if err != nil {
		return err
	}
	res.Fetched = len(fetched)
	points, err := collector.Shape(fetched, shaping)
	if err != nil {
		return err
	}
	samples := make([]model.MetricSample, 0, len(points))
	for _, p := range points {
		if p.Time.Before(from) || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		samples = append(samples, model.MetricSample{TS: p.Time.Unix(), Value: p.Value})
	}
	if err := s.Samples.Upsert(ctx, series.ID, samples); err != nil {
		return err
	}
	res.Stored = len(samples)
	res.Quality = collector.Analyze(points, from, to, setup.step, now)
	pruned, err := s.Samples.PruneBefore(ctx, series.ID, now.Add(-time.Duration(hours)*time.Hour))
	res.Pruned = pruned
	return err
}

// History is the stored window of one metric with its quality report. Quality is computed on every stored
// sample; Points may be averaged into buckets of BucketSeconds so a long window stays drawable.
type History struct {
	Metric        string            `json:"metric"`
	StepSeconds   int               `json:"step_seconds"`
	BucketSeconds int               `json:"bucket_seconds"`
	Unit          string            `json:"unit"`
	Points        []HistoryPoint    `json:"points"`
	Quality       collector.Quality `json:"quality"`
	// NotCollected is true when nothing has been collected for the metric yet: an empty history, not an error.
	NotCollected bool `json:"not_collected,omitempty"`
}

// HistoryPoint is one stored sample.
type HistoryPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// History limits: a window may span up to maxHistoryWindowPoints stored steps (about 140 days at one minute),
// and a response carries at most maxHistoryPoints points, averaged into buckets when the window has more.
const (
	maxHistoryWindowPoints = 200000
	DefaultHistoryPoints   = 2000
	maxHistoryPoints       = 10000
)

// downsample averages points into buckets of whole steps so that at most maxPoints remain. A bucket without any
// stored sample produces no point, so a gap in the data stays a gap.
func downsample(points []HistoryPoint, from time.Time, step time.Duration, expected, maxPoints int) ([]HistoryPoint, time.Duration) {
	bucketSteps := (expected + maxPoints - 1) / maxPoints
	if bucketSteps <= 1 {
		return points, step
	}
	bucket := time.Duration(bucketSteps) * step
	var out []HistoryPoint
	var sum float64
	count, current := 0, int64(-1)
	flush := func() {
		if count > 0 {
			out = append(out, HistoryPoint{Time: from.Add(time.Duration(current) * bucket), Value: sum / float64(count)})
		}
	}
	for _, p := range points {
		idx := int64(p.Time.Sub(from) / bucket)
		if idx != current {
			flush()
			current, sum, count = idx, 0, 0
		}
		sum += p.Value
		count++
	}
	flush()
	return out, bucket
}

// History returns the stored samples of a metric in [from, to] and the quality of that window. Missing
// steps are not filled: the gaps in the quality report are the visualization of what is absent.
func (s *Service) History(ctx context.Context, moduleID uint, metric string, from, to time.Time, maxPoints int) (*History, error) {
	if maxPoints <= 0 {
		maxPoints = DefaultHistoryPoints
	}
	if maxPoints > maxHistoryPoints {
		maxPoints = maxHistoryPoints
	}
	series, err := s.Samples.Series(ctx, moduleID, metric)
	if err != nil {
		return nil, err
	}
	step := time.Duration(series.StepSeconds) * time.Second
	from, to = floorToStep(from, step), floorToStep(to, step)
	if to.Before(from) {
		return nil, fmt.Errorf("from must not be after to")
	}
	expected := int(to.Sub(from)/step) + 1
	if expected > maxHistoryWindowPoints {
		return nil, fmt.Errorf("window exceeds %d steps; request a shorter range", maxHistoryWindowPoints)
	}
	samples, err := s.Samples.Range(ctx, series.ID, from, to)
	if err != nil {
		return nil, err
	}
	points := make([]collector.Point, len(samples))
	out := &History{Metric: metric, StepSeconds: series.StepSeconds, Unit: MetricUnit(metric)}
	raw := make([]HistoryPoint, len(samples))
	for i, sm := range samples {
		t := time.Unix(sm.TS, 0).UTC()
		points[i] = collector.Point{Time: t, Value: sm.Value}
		raw[i] = HistoryPoint{Time: t, Value: sm.Value}
	}
	out.Quality = collector.Analyze(points, from, to, step, s.now())
	points2, bucket := downsample(raw, from, step, expected, maxPoints)
	out.Points, out.BucketSeconds = points2, int(bucket.Seconds())
	return out, nil
}
