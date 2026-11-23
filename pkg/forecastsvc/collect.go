package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// CollectResult is the outcome of collecting one metric.
type CollectResult struct {
	Metric  string    `json:"metric"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Fetched int       `json:"fetched"`
	Stored  int       `json:"stored"`
	Pruned  int64     `json:"pruned"`
	// Repaired is how many samples of holes in the stored history were fetched again from the data source.
	Repaired int               `json:"repaired,omitempty"`
	Quality  collector.Quality `json:"quality"`
	Error    string            `json:"error,omitempty"`
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
	m.AllSources = true // the history keeps the display metrics too: the history data and the charts show them
	metrics := configuredMetrics(m)
	if len(metrics) == 0 {
		return nil, fmt.Errorf("%w: the module has no metric source", ErrNotConfigured)
	}
	results := make([]CollectResult, 0, len(metrics))
	runs := s.runsForCollect(ctx, mod)
	for i, metric := range metrics {
		res := CollectResult{Metric: metric}
		started := time.Now()
		err := s.collectMetric(ctx, m, runs, metric, &res)
		s.Telemetry.recordCollect(metric, time.Since(started), err)
		if err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
		stepReport(ctx, "collect: "+metric, i+1, len(metrics), results)
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
	querier, err := s.querierOf(ctx, setup.ds)
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
	fetched, points, stored, err := s.storeRange(ctx, querier, query, shaping, series.ID, from, to, opt)
	if err != nil {
		return err
	}
	res.Fetched, res.Stored = fetched, stored
	res.Quality = collector.Analyze(points, from, to, setup.step, now)
	histFrom := now.Add(-time.Duration(hours) * time.Hour)
	// the holes the collection left behind (the source was down, a request failed, the platform was stopped for longer than a range holds) are
	// asked for again from the data source, in segments like the first backfill; what it never had stays a hole
	repaired, rerr := s.repairGaps(ctx, querier, query, shaping, series, floorToStep(histFrom, setup.step), to, opt)
	res.Repaired = repaired
	if rerr != nil {
		res.Error = "repairing the gaps: " + rerr.Error()
	}
	pruned, err := s.Samples.PruneBefore(ctx, series.ID, histFrom)
	res.Pruned = pruned
	return err
}

// storeRange fetches [from, to] of a query in segments, shapes it into the dataset step and stores the samples from "from" on (idempotent).
func (s *Service) storeRange(ctx context.Context, querier collector.RangeQuerier, query string, shaping collector.Shaping, seriesID uint,
	from, to time.Time, opt collector.Options) (fetched int, points []collector.Point, stored int, err error) {
	raw, err := collector.Fetch(ctx, querier, query, shaping.FetchStart(from), to, shaping.QueryStep, opt)
	if err != nil {
		return 0, nil, 0, err
	}
	points, err = collector.Shape(raw, shaping)
	if err != nil {
		return len(raw), nil, 0, err
	}
	samples := make([]model.MetricSample, 0, len(points))
	for _, p := range points {
		if p.Time.Before(from) || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		samples = append(samples, model.MetricSample{TS: p.Time.Unix(), Value: p.Value})
	}
	if err := s.Samples.Upsert(ctx, seriesID, samples); err != nil {
		return len(raw), points, 0, err
	}
	return len(raw), points, len(samples), nil
}

const (
	// gapScanEvery is how often a series is looked at for holes (reading its stored range is not free), gapRetryAfter how long a hole that the data
	// source could not fill is left alone, and maxGapsPerScan how many holes one look asks for: the largest first.
	gapScanEvery   = time.Hour
	gapRetryAfter  = 24 * time.Hour
	maxGapsPerScan = 3
)

type gapRange struct {
	from, to time.Time
	steps    int
}

// findGaps lists the holes inside the stored samples: runs of steps with no sample between two that exist, the largest first.
func findGaps(samples []model.MetricSample, step time.Duration) []gapRange {
	var out []gapRange
	sec := int64(step / time.Second)
	if sec <= 0 {
		return nil
	}
	for i := 1; i < len(samples); i++ {
		if missing := int((samples[i].TS-samples[i-1].TS)/sec) - 1; missing > 0 {
			out = append(out, gapRange{from: time.Unix(samples[i-1].TS+sec, 0).UTC(), to: time.Unix(samples[i].TS-sec, 0).UTC(), steps: missing})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].steps > out[j].steps })
	return out
}

// repairGaps asks the data source again for the holes inside the stored history of a series (at most maxGapsPerScan an hour a series, and a hole
// that stayed empty is not asked for again for gapRetryAfter) and returns how many samples it stored. The first collection backfills the
// range, later ones continue from the newest sample, so before this a hole stayed one for good, and a history with a hole in it had a
// coverage that only grew by a step a step.
func (s *Service) repairGaps(ctx context.Context, querier collector.RangeQuerier, query string, shaping collector.Shaping, series *model.MetricSeries,
	histFrom, to time.Time, opt collector.Options) (int, error) {
	now := s.now()
	if last, ok := s.gapScans.Load(series.ID); ok && now.Sub(last.(time.Time)) < gapScanEvery {
		return 0, nil
	}
	s.gapScans.Store(series.ID, now)
	samples, err := s.Samples.Range(ctx, series.ID, histFrom, to)
	if err != nil || len(samples) < 2 {
		return 0, err
	}
	total, asked := 0, 0
	for _, g := range findGaps(samples, shaping.Step) {
		if asked >= maxGapsPerScan {
			break
		}
		key := fmt.Sprintf("%d/%d", series.ID, g.from.Unix())
		if at, ok := s.gapTries.Load(key); ok && now.Sub(at.(time.Time)) < gapRetryAfter {
			continue
		}
		s.gapTries.Store(key, now)
		asked++
		_, _, stored, err := s.storeRange(ctx, querier, query, shaping, series.ID, g.from, g.to, opt)
		if err != nil {
			return total, err
		}
		total += stored
	}
	return total, nil
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
