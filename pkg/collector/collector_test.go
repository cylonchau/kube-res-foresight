package collector

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestRenderSubstitutesAndRejectsInjection(t *testing.T) {
	got, err := Render(`sum(rate(x{namespace="{{namespace}}",pod=~"{{ workload }}-.*"}[{{range}}]))`,
		map[string]string{"namespace": "prod", "workload": "api-server", "range": "300s"})
	require.NoError(t, err)
	assert.Equal(t, `sum(rate(x{namespace="prod",pod=~"api-server-.*"}[300s]))`, got)

	for _, bad := range []string{`prod"} or vector(1) #`, `a\b`, `a{b}`, "a b", "-x", "x-", ""} {
		_, err := Render(`x{namespace="{{namespace}}"}`, map[string]string{"namespace": bad})
		assert.Error(t, err, "value %q", bad)
	}
	_, err = Render(`x[{{range}}]`, map[string]string{"range": "5m; drop"})
	assert.Error(t, err)
	_, err = Render(`x{a="{{service}}"}`, map[string]string{})
	assert.Error(t, err)
}

func TestRenderMappingRefusesPodLevelTemplates(t *testing.T) {
	m := &model.MetricMapping{Name: "pod-cpu", Query: `rate(x{pod="{{pod}}"}[{{range}}])`, StepSeconds: 60}
	_, err := RenderMapping(m, Target{Namespace: "prod", Workload: "api"})
	assert.Error(t, err)
	m = &model.MetricMapping{Name: "w", Query: `sum(rate(x{namespace="{{namespace}}"}[{{range}}]))`, StepSeconds: 30}
	got, err := RenderMapping(m, Target{Namespace: "prod"})
	require.NoError(t, err)
	assert.Equal(t, `sum(rate(x{namespace="prod"}[120s]))`, got)
}

func TestRateWindowIsFourStepsAtLeastOneMinute(t *testing.T) {
	assert.Equal(t, "60s", RateWindow(15*time.Second))
	assert.Equal(t, "240s", RateWindow(time.Minute))
}

func TestSegmentsAreContiguousAndBounded(t *testing.T) {
	step := time.Minute
	end := t0.Add(24 * time.Hour)
	segs := Segments(t0, end, step, 500)
	require.NotEmpty(t, segs)
	assert.Equal(t, t0, segs[0].start)
	assert.Equal(t, end, segs[len(segs)-1].end)
	total := 0
	for i, s := range segs {
		points := int(s.end.Sub(s.start)/step) + 1
		assert.LessOrEqual(t, points, 500)
		total += points
		if i > 0 {
			assert.Equal(t, segs[i-1].end.Add(step), s.start, "no overlap and no hole")
		}
	}
	assert.Equal(t, 24*60+1, total)
	assert.Empty(t, Segments(end, t0, step, 500))
}

// fakeQuerier serves a synthetic series, optionally failing the first calls.
type fakeQuerier struct {
	mu        sync.Mutex
	calls     int32
	failFirst int32
	err       error
	maxPoints int
	inflight  int32
	peak      int32
}

func (f *fakeQuerier) QueryRange(_ context.Context, _ string, start, end time.Time, step time.Duration) ([]Point, error) {
	cur := atomic.AddInt32(&f.inflight, 1)
	defer atomic.AddInt32(&f.inflight, -1)
	for {
		p := atomic.LoadInt32(&f.peak)
		if cur <= p || atomic.CompareAndSwapInt32(&f.peak, p, cur) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	n := atomic.AddInt32(&f.calls, 1)
	if n <= f.failFirst {
		return nil, f.err
	}
	var out []Point
	for ts := start; !ts.After(end); ts = ts.Add(step) {
		out = append(out, Point{Time: ts, Value: float64(ts.Unix())})
	}
	f.mu.Lock()
	if len(out) > f.maxPoints {
		f.maxPoints = len(out)
	}
	f.mu.Unlock()
	return out, nil
}

func TestFetchMergesSegmentsWithinTheBudget(t *testing.T) {
	q := &fakeQuerier{}
	end := t0.Add(6 * time.Hour)
	points, err := Fetch(context.Background(), q, "q", t0, end, time.Minute, Options{MaxPointsPerRequest: 100, Concurrency: 3})
	require.NoError(t, err)
	assert.Len(t, points, 6*60+1)
	assert.LessOrEqual(t, q.maxPoints, 100, "no request exceeds the point budget")
	assert.LessOrEqual(t, q.peak, int32(3), "concurrency budget is respected")
	for i := 1; i < len(points); i++ {
		require.True(t, points[i].Time.After(points[i-1].Time), "ordered and without duplicates")
	}
}

func TestFetchRetriesTransientErrorsAndStopsOnPermanentOnes(t *testing.T) {
	slept := 0
	opt := Options{Attempts: 3, Sleep: func(context.Context, time.Duration) error { slept++; return nil }}
	q := &fakeQuerier{failFirst: 2, err: errors.New("503")}
	points, err := Fetch(context.Background(), q, "q", t0, t0.Add(10*time.Minute), time.Minute, opt)
	require.NoError(t, err)
	assert.Len(t, points, 11)
	assert.Equal(t, 2, slept)

	q = &fakeQuerier{failFirst: 99, err: errors.New("503")}
	_, err = Fetch(context.Background(), q, "q", t0, t0.Add(10*time.Minute), time.Minute, opt)
	assert.Error(t, err)
	assert.EqualValues(t, 3, q.calls, "bounded attempts, no retry storm")

	q = &fakeQuerier{failFirst: 99, err: ErrPermanent}
	_, err = Fetch(context.Background(), q, "q", t0, t0.Add(10*time.Minute), time.Minute, opt)
	assert.ErrorIs(t, err, ErrPermanent)
	assert.EqualValues(t, 1, q.calls)
}

func TestFetchHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Fetch(ctx, &fakeQuerier{}, "q", t0, t0.Add(time.Hour), time.Minute, Options{})
	assert.Error(t, err)
}

func grid(n int, step time.Duration, skip map[int]bool, f func(i int) float64) []Point {
	var out []Point
	for i := 0; i < n; i++ {
		if skip[i] {
			continue
		}
		out = append(out, Point{Time: t0.Add(time.Duration(i) * step), Value: f(i)})
	}
	return out
}

func TestAnalyzeReportsGapsNaNAndLag(t *testing.T) {
	step := time.Minute
	pts := grid(100, step, map[int]bool{10: true, 11: true, 12: true, 50: true}, func(i int) float64 { return 10 })
	pts[20].Value = math.NaN()
	end := t0.Add(99 * step)
	q := Analyze(pts, t0, end, step, end.Add(3*time.Minute))
	assert.Equal(t, 100, q.Expected)
	assert.Equal(t, 95, q.Present)
	assert.Equal(t, 1, q.NaN)
	assert.InDelta(t, 0.05, q.MissingRatio, 1e-9)
	assert.Equal(t, 3, q.LongestGapSteps)
	require.Len(t, q.Gaps, 3)
	assert.Equal(t, Gap{From: t0.Add(10 * step), To: t0.Add(12 * step), Steps: 3}, q.Gaps[0])
	assert.Equal(t, 180, q.LatestLagSecs)
}

func TestAnalyzeFlagsOffGridAndDuplicateSamples(t *testing.T) {
	step := time.Minute
	pts := grid(10, step, nil, func(i int) float64 { return 1 })
	pts = append(pts, Point{Time: t0.Add(3 * step), Value: 2}, Point{Time: t0.Add(90 * time.Second), Value: 3})
	q := Analyze(pts, t0, t0.Add(9*step), step, t0.Add(10*step))
	assert.Equal(t, 2, q.Duplicates)
	assert.Equal(t, 10, q.Present)
}

func TestAnalyzeCountsAnomalousJumps(t *testing.T) {
	step := time.Minute
	pts := grid(200, step, nil, func(i int) float64 {
		if i == 120 {
			return 1000
		}
		return 100 + float64(i%3)
	})
	q := Analyze(pts, t0, t0.Add(199*step), step, t0.Add(200*step))
	assert.Equal(t, 2, q.Jumps, "the spike up and the drop back")

	flat := grid(200, step, nil, func(i int) float64 { return 100 + float64(i%2)*0.01 })
	q = Analyze(flat, t0, t0.Add(199*step), step, t0.Add(200*step))
	assert.Zero(t, q.Jumps, "tiny noise on a flat series is not a jump")
}

func TestToInputInterpolatesShortGapsAndStopsAtLongOnes(t *testing.T) {
	step := time.Minute
	// values 0..19, a 2-step gap at 5-6, a 6-step gap at 10-15
	skip := map[int]bool{5: true, 6: true, 10: true, 11: true, 12: true, 13: true, 14: true, 15: true}
	pts := grid(20, step, skip, func(i int) float64 { return float64(i) })
	in, filled, ok := ToInput(pts, t0, t0.Add(19*step), step, 3, FillInterpolate)
	require.True(t, ok)
	assert.Equal(t, t0.Add(19*step), in.End)
	assert.Equal(t, []float64{16, 17, 18, 19}, in.Values, "the run ends at the long gap")
	assert.Zero(t, filled)

	// without the long gap the short one is interpolated linearly
	pts = grid(10, step, map[int]bool{5: true, 6: true}, func(i int) float64 { return float64(i) })
	in, filled, ok = ToInput(pts, t0, t0.Add(9*step), step, 3, FillInterpolate)
	require.True(t, ok)
	assert.Equal(t, 2, filled)
	for i, v := range in.Values {
		assert.InDelta(t, float64(i), v, 1e-9)
	}
	assert.Len(t, in.Values, 10)
}

func TestToInputEndsAtTheLatestFiniteSampleAndRejectsEmpty(t *testing.T) {
	step := time.Minute
	pts := grid(10, step, map[int]bool{8: true, 9: true}, func(i int) float64 { return 1 })
	in, _, ok := ToInput(pts, t0, t0.Add(9*step), step, 3, FillInterpolate)
	require.True(t, ok)
	assert.Equal(t, t0.Add(7*step), in.End, "stale tail is not extrapolated")
	_, _, ok = ToInput(nil, t0, t0.Add(9*step), step, 3, FillInterpolate)
	assert.False(t, ok)
}

// failingAt fails permanently for the segment that starts at or after failFrom.
type failingAt struct{ failFrom time.Time }

func (f failingAt) QueryRange(ctx context.Context, _ string, start, end time.Time, step time.Duration) ([]Point, error) {
	if !start.Before(f.failFrom) {
		return nil, ErrPermanent
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Millisecond):
	}
	return []Point{{Time: start, Value: 1}}, nil
}

func TestFetchReportsTheRealFailureNotTheCancellationItCaused(t *testing.T) {
	q := failingAt{failFrom: t0.Add(5 * time.Hour)}
	_, err := Fetch(context.Background(), q, "q", t0, t0.Add(10*time.Hour), time.Minute, Options{MaxPointsPerRequest: 60, Concurrency: 8})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
	assert.NotErrorIs(t, err, context.Canceled)
}
