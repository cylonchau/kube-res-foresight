package forecastsvc

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The module of costFixture requests 4 cores and 4 GiB a Pod; the 4 Pods use 0.3 to 0.7 cores and 0.75 to 1.25 GiB between them (a total that swings
// daily). The analysis must call both requests over-provisioned, from the use alone.
func TestTheAnalysisSaysAPodThatUsesAFractionOfItsRequestIsOverProvisioned(t *testing.T) {
	f := opFixture(t)
	ctx := context.Background()
	got, err := f.svc.AnalyzeOverprovision(ctx, f.module)
	require.NoError(t, err)
	require.Len(t, got.Windows, len(overprovision.Windows))
	assert.Equal(t, 4, got.Replicas)

	var day *WindowOverprovision
	for _, w := range got.Windows {
		if w.Label == "1d" {
			day = w
		}
	}
	require.NotNil(t, day)
	require.NotNil(t, day.CPU)
	require.NotNil(t, day.Memory)
	cpu, mem := day.CPU, day.Memory
	t.Logf("cpu: %+v", *cpu)
	t.Logf("memory: %+v", *mem)

	assert.InDelta(t, 4.0, cpu.Request, 1e-9)
	assert.Equal(t, overprovision.StatusOver, cpu.Status)
	assert.Equal(t, overprovision.GradeSevere, cpu.Grade)
	assert.Greater(t, cpu.Suggested, cpu.Usage.P95, "the suggestion is above what was used")
	assert.Less(t, cpu.Suggested, 1.0)
	assert.InDelta(t, cpu.Request-cpu.Suggested, cpu.Reclaimable, 1e-9)
	assert.InDelta(t, cpu.Usage.P95*4, cpu.Total.P95, 0.01, "the total is the four Pods together")
	assert.Greater(t, cpu.Total.P95, cpu.Usage.P95*3, "four Pods use about four times one Pod")
	assert.Equal(t, overprovision.StatusOver, mem.Status)
	assert.Equal(t, overprovision.FormulaVersion, mem.FormulaVersion)
	assert.NotEmpty(t, mem.Formula)

	// every window and resource that has a result is stored once
	rows, err := f.svc.Overprovision.Latest(ctx, 24*60)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestTheBoardAddsUpWhatTheWorkloadsCouldGiveBack(t *testing.T) {
	f := opFixture(t)
	ctx := context.Background()
	board, err := f.svc.OverprovisionBoard(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, board.Modules)
	assert.Equal(t, 0, board.Analyzed, "nothing is analysed until the job or the button runs")
	assert.Nil(t, board.AnalyzedAt)
	assert.Equal(t, HealthUnknown, board.Rows[0].Health)

	done, err := f.svc.RunOverprovision(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, done)

	board, err = f.svc.OverprovisionBoard(ctx, 24*60)
	require.NoError(t, err)
	require.Equal(t, 1, board.Analyzed)
	require.NotNil(t, board.AnalyzedAt)
	row := board.Rows[0]
	require.NotNil(t, row.CPU)
	require.NotNil(t, row.Memory)
	assert.Equal(t, HealthOver, row.Health)
	assert.Equal(t, 1, board.Over)
	// 4 replicas of 4 cores
	assert.InDelta(t, 16.0, board.CPU.Request, 1e-9)
	assert.InDelta(t, row.CPU.Reclaimable*4, board.CPU.Reclaimable, 1e-9)
	assert.InDelta(t, board.CPU.Request-board.CPU.Reclaimable, board.CPU.After, 1e-9)
	assert.Greater(t, board.CPU.Share, 0.5)
	assert.Contains(t, board.Basis, "no forecast takes part")

	// a second run adds a second result and the board shows the newest
	_, err = f.svc.RunOverprovision(ctx, f.module.ID)
	require.NoError(t, err)
	hist, err := f.svc.Overprovision.History(ctx, f.module.ID, "cpu", 24*60, 10)
	require.NoError(t, err)
	assert.Len(t, hist, 2)
	assert.Greater(t, hist[0].ID, hist[1].ID, "newest first")
	board, err = f.svc.OverprovisionBoard(ctx, 24*60)
	require.NoError(t, err)
	assert.Equal(t, hist[0].ID, board.Rows[0].CPU.ID)

	_, err = f.svc.OverprovisionBoard(ctx, 17)
	assert.ErrorIs(t, err, ErrOverprovisionWindow)
}

func TestTheDetailIsReadFromPrometheusWithTheChartAndTheQueries(t *testing.T) {
	f := opFixture(t)
	f.withRequests(t, 1.5)
	ctx := context.Background()
	_, err := f.svc.RunOverprovision(ctx, f.module.ID)
	require.NoError(t, err)

	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Len(t, d.Windows, 1)
	require.Len(t, d.Charts, 2)
	c := d.Charts[0]
	assert.Equal(t, "cpu", c.Resource)
	require.NotEmpty(t, c.TS)
	assert.Len(t, c.Value, len(c.TS))
	assert.Equal(t, 300, c.StepSecs, "a window of a day is drawn every 5 minutes")
	assert.InDelta(t, 4.0, c.Request, 1e-9)
	// the use of all the replicas together is drawn next to the use of one Pod
	require.NotEmpty(t, c.Total, "the module has a sum over the Pods")
	assert.Len(t, c.TotalTS, len(c.Total))
	assert.Equal(t, d.Replicas, c.Replicas)
	assert.Greater(t, c.Total[0], c.Value[0], "the sum of the replicas is more than one of them")
	assert.Nil(t, d.JVM, "the module has no JVM metric")
	// every figure is a query sent to Prometheus, listed with its points
	require.NotEmpty(t, d.Queries)
	assert.False(t, d.Partial)
	for _, q := range d.Queries {
		if q.Kind == "cpu" || q.Kind == "memory" {
			assert.NotEmpty(t, q.Query, q.Kind)
			assert.Greater(t, q.Points, 100, q.Kind)
			assert.Empty(t, q.Error, q.Kind)
		}
	}
	assert.NotEmpty(t, d.Datasource.Name)
	assert.Equal(t, 0.9, d.Params.PeakNearLimit)

	// a window that is not one of the seven, and a range that is back to front
	_, err = f.svc.OverprovisionDetail(ctx, f.module.ID, 17, time.Time{}, time.Time{})
	assert.ErrorIs(t, err, ErrOverprovisionWindow)
	now := f.svc.now()
	_, err = f.svc.OverprovisionDetail(ctx, f.module.ID, 0, now, now.Add(-1))
	assert.ErrorIs(t, err, ErrOverprovisionWindow)
	// a custom range is analysed and not stored
	before, _ := f.svc.Overprovision.History(ctx, f.module.ID, "", 0, 500)
	custom, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 0, now.Add(-6*time.Hour), now)
	require.NoError(t, err)
	assert.Equal(t, "custom", custom.Windows[0].Label)
	assert.Equal(t, 360, custom.Windows[0].WindowMinutes)
	after, _ := f.svc.Overprovision.History(ctx, f.module.ID, "", 0, 500)
	assert.Len(t, after, len(before))
}

func TestTheBaselineWindowFollowsTheParameterToTheNearestWindow(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, 7*24*60, opFixture(t).svc.OverprovisionBaseline(ctx))
	// the parameter is read through a short cache, so each value gets a service of its own
	for days, want := range map[string]int{"1": 24 * 60, "10": 7 * 24 * 60, "12": 15 * 24 * 60, "30": 30 * 24 * 60} {
		f := opFixture(t)
		require.NoError(t, f.db.Exec("INSERT INTO sys_config (config_key, config_value, config_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", "analysis.baseline_window_days", days, "x", time.Now(), time.Now()).Error)
		assert.Equal(t, want, f.svc.OverprovisionBaseline(ctx), days)
	}
}

// opFixture is costFixture with the tables of the analysis and of the system parameters.
func opFixture(t *testing.T) *fixture {
	t.Helper()
	f, _ := costFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.OverprovisionAnalysis{}, &model.SysConfig{}))
	return f
}

// A CPU that was throttled and a memory that was OOM-killed are not called over-provisioned: the use understates the need. The JVM metrics are shown
// beside the working set, with hints.
func TestThrottlingOOMAndTheJVMKeepTheAnalysisFromLoweringARequest(t *testing.T) {
	f := opFixture(t)
	for _, kind := range []string{"k8s_cpu_throttle", "oom", "oom_working_set", "jvm_heap_used", "jvm_heap_max"} {
		f.addAux(t, kind)
	}
	f.values(map[string]func(ts time.Time) float64{
		"memory":           func(ts time.Time) float64 { return daily(4*float64(costGiB), 1*float64(costGiB), ts) },
		"cpu":              func(ts time.Time) float64 { return daily(2, 0.8, ts) },
		"k8s_cpu_throttle": func(time.Time) float64 { return 0.4 }, // 0.4 s a second over 4 Pods: 0.1 of a second a Pod, 10% of the time
		"oom":              func(time.Time) float64 { return 1 },
		"oom_working_set":  func(time.Time) float64 { return 3 * float64(costGiB) }, // 3 GiB over 4 Pods: 0.75 GiB a Pod... the raw series is not divided
		"jvm_heap_used":    func(time.Time) float64 { return 2 * float64(costGiB) },
		"jvm_heap_max":     func(time.Time) float64 { return 32 * float64(costGiB) },
	})
	f.withReplicas()
	f.withRequests(t, 0)
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)

	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	cpu, mem := d.Windows[0].CPU, d.Windows[0].Memory
	require.NotNil(t, cpu)
	require.NotNil(t, mem)
	assert.InDelta(t, 0.1, cpu.Throttle, 0.02, "the throttled seconds of the Pods, per Pod")
	assert.Equal(t, overprovision.StatusLimited, cpu.Status)
	assert.Contains(t, cpu.Reasons, overprovision.ReasonThrottled)
	assert.InDelta(t, cpu.Request, cpu.Suggested, 1e-9, "never below the request")

	assert.Equal(t, overprovision.StatusLimited, mem.Status)
	assert.Contains(t, mem.Reasons, overprovision.ReasonOOM)
	assert.Equal(t, overprovision.High, mem.Risk)
	assert.Greater(t, mem.OOMKills, 0.0)
	assert.GreaterOrEqual(t, mem.Suggested, mem.Request)

	require.NotNil(t, d.JVM)
	assert.True(t, d.JVM.Expected)
	assert.True(t, d.JVM.Present)
	require.NotNil(t, d.JVM.HeapUsed)
	require.NotNil(t, d.JVM.HeapMax)
	assert.Contains(t, d.JVM.Hints, "heap_max_above_working_set")
	assert.NotContains(t, cpu.Reasons, overprovision.ReasonJVMMissing)

	// the board counts it as a risk, and what is limited is not given back
	_, err = f.svc.RunOverprovision(ctx, 0)
	require.NoError(t, err)
	board, err := f.svc.OverprovisionBoard(ctx, 24*60)
	require.NoError(t, err)
	assert.Equal(t, 1, board.Risk)
	assert.Equal(t, 0.0, board.Memory.Reclaimable)
	assert.Equal(t, 0.0, board.CPU.Reclaimable)
}

// A long window is thinned for the chart and keeps the step of the window.
func TestALongWindowIsThinnedForTheChart(t *testing.T) {
	f := opFixture(t)
	ctx := context.Background()
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 7*24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, d.Charts)
	c := d.Charts[0]
	assert.LessOrEqual(t, len(c.TS), opMaxPoints)
	assert.GreaterOrEqual(t, c.StepSecs, 300, "a week is read at a coarser step than a day: at most 1500 steps a query")
	assert.Equal(t, 0, c.StepSecs%60)
}

func TestOpChartThinsLongSeries(t *testing.T) {
	pts := make([]overprovision.Point, 2016) // 7 days every 5 minutes
	for i := range pts {
		pts[i] = overprovision.Point{TS: int64(i) * 300, V: float64(i % 10)}
	}
	c := opChart("cpu", pts, 300, 7*24*60, &overprovision.Result{Request: 4, Suggested: 1})
	assert.LessOrEqual(t, len(c.TS), opMaxPoints)
	assert.Greater(t, len(c.TS), opMaxPoints/2)
	assert.Len(t, c.Value, len(c.TS))
	assert.Equal(t, 300, c.StepSecs)
	// the second point of the thinned series is the 8th point of the full one (stride 7)
	assert.Equal(t, pts[7].TS, c.TS[1])
}

// The page shows the metrics the module has set up and nothing else: the throttle and the JVM here, not the CPU and the memory (they are the main
// figures) and not what is not configured.
func TestTheDetailListsTheOtherMetricsTheModuleHasSetUp(t *testing.T) {
	f := opFixture(t)
	for _, kind := range []string{"k8s_cpu_throttle", "jvm_heap_used", "jvm_heap_max"} {
		f.addAux(t, kind)
	}
	f.values(map[string]func(ts time.Time) float64{
		"memory":           func(ts time.Time) float64 { return daily(4*float64(costGiB), 1*float64(costGiB), ts) },
		"cpu":              func(ts time.Time) float64 { return daily(2, 0.8, ts) },
		"k8s_cpu_throttle": func(time.Time) float64 { return 0.04 },
		"jvm_heap_used":    func(time.Time) float64 { return 2 * float64(costGiB) },
		"jvm_heap_max":     func(time.Time) float64 { return 8 * float64(costGiB) },
	})
	f.withReplicas()
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	groups := map[string]string{}
	for _, m := range d.Metrics {
		groups[m.Kind] = m.Group
		assert.Greater(t, m.Points, 0, m.Kind)
		assert.GreaterOrEqual(t, m.P99, m.P90, m.Kind)
		assert.GreaterOrEqual(t, m.P90, m.P50, m.Kind)
	}
	assert.Equal(t, map[string]string{"k8s_cpu_throttle": "throttle", "jvm_heap_used": "jvm", "jvm_heap_max": "jvm", "rps": "other"}, groups, "the request rate of the fixture is a source too")
	// the P90, the P95 and the P99 are all there, and stored
	cpu := d.Windows[0].CPU
	require.NotNil(t, cpu)
	assert.LessOrEqual(t, cpu.Usage.P90, cpu.Usage.P95)
	assert.LessOrEqual(t, cpu.Usage.P95, cpu.Usage.P99)
	assert.Greater(t, cpu.Usage.P90, 0.0)
	_, err = f.svc.RunOverprovision(ctx, f.module.ID)
	require.NoError(t, err)
	stored, err := f.svc.Overprovision.Latest(ctx, 24*60)
	require.NoError(t, err)
	require.NotEmpty(t, stored)
	for _, r := range stored {
		assert.Greater(t, r.P90, 0.0, "the P90 is stored with the rest")
	}
}

// With no limits metric the limit is what the module is configured to give a new Pod: the request times the ratio, said to come from the module.
func TestTheRequestsAndTheLimitsAreThoseOfPrometheusAndNeverTheSettingsOfTheModule(t *testing.T) {
	f := opFixture(t)
	// the module is set up to request 4 cores and 4 GiB with a limit ratio, but no metric says so: nothing of it is shown as a request or a limit
	f.setModule(t, func(m *model.ForecastModule) { m.LimitCPURatio, m.LimitMemoryRatio = 2, 1.5 })
	d, err := f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	cpu := d.Windows[0].CPU
	require.NotNil(t, cpu)
	assert.Zero(t, cpu.Request, "the Pod request of the module is not a Prometheus figure")
	assert.Zero(t, cpu.Limit, "the limit ratio of the module is not a Prometheus figure")
	assert.Empty(t, cpu.LimitFrom)
	assert.Equal(t, overprovision.StatusUnknown, cpu.Status)

	// with the metrics declared, the figures are their answers
	f.withRequests(t, 1.5)
	d, err = f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	cpu, mem := d.Windows[0].CPU, d.Windows[0].Memory
	assert.InDelta(t, 4.0, cpu.Request, 1e-9)
	assert.InDelta(t, 6.0, cpu.Limit, 1e-9, "4 Pods limited to 24 cores in all")
	assert.Equal(t, "metric", cpu.LimitFrom)
	assert.InDelta(t, 4*float64(costGiB), mem.Request, 1)
	assert.InDelta(t, 6*float64(costGiB), mem.Limit, 1)
}

// The committed heap, the maximum of the non-heap and the GC are shown next to the used heap and the working set; committed is said not to be use, and a
// JVM without the GC metrics is still analysed.
func TestTheJVMShowsCommittedAndTheGCBesideUsedAndMaxAndNeverTakesCommittedForUse(t *testing.T) {
	f := opFixture(t)
	kinds := []string{"jvm_heap_used", "jvm_heap_max", "jvm_heap_committed", "jvm_nonheap_used", "jvm_nonheap_committed", "jvm_nonheap_max", "jvm_gc_count", "jvm_gc_pause"}
	for _, kind := range kinds {
		f.addAux(t, kind)
	}
	gib := float64(costGiB)
	f.values(map[string]func(ts time.Time) float64{
		"memory":                func(ts time.Time) float64 { return daily(4*gib, 1*gib, ts) },
		"cpu":                   func(ts time.Time) float64 { return daily(2, 0.8, ts) },
		"jvm_heap_used":         func(time.Time) float64 { return 2 * gib },
		"jvm_heap_max":          func(time.Time) float64 { return 8 * gib },
		"jvm_heap_committed":    func(time.Time) float64 { return 2.1 * gib },
		"jvm_nonheap_used":      func(time.Time) float64 { return 0.25 * gib },
		"jvm_nonheap_committed": func(time.Time) float64 { return 0.3 * gib },
		"jvm_nonheap_max":       func(time.Time) float64 { return 0.5 * gib },
		"jvm_gc_count":          func(time.Time) float64 { return 12 },
		"jvm_gc_pause":          func(time.Time) float64 { return 0.4 },
	})
	f.withReplicas()
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, d.JVM)
	j := d.JVM
	for name, u := range map[string]interface{}{"heap committed": j.HeapCommitted, "non-heap committed": j.NonHeapCommitted, "non-heap max": j.NonHeapMax, "gc count": j.GCCount, "gc pause": j.GCPause} {
		assert.NotNil(t, u, name)
	}
	assert.Greater(t, j.HeapCommitted.P99, j.HeapUsed.P99, "the committed heap is shown as it is: above the used one")
	assert.Contains(t, j.Hints, "committed_is_not_use")
	assert.Contains(t, j.Hints, "heap_near_committed", "2 of 2.1 GiB used is 95% of what was committed")
	assert.NotContains(t, j.Hints, "gc_missing")
	// the container memory is the working set, whatever the JVM says
	mem := d.Windows[0].Memory
	require.NotNil(t, mem)
	assert.InDelta(t, mem.Usage.P99, j.WorkingSetP99, 1e-6)
	groups := map[string]string{}
	for _, m := range d.Metrics {
		groups[m.Kind] = m.Group
	}
	for _, kind := range kinds {
		assert.Equal(t, "jvm", groups[kind], kind)
	}
}

func TestAJVMWithoutTheGCMetricsIsStillAnalysedAndSaysTheGCIsMissing(t *testing.T) {
	f := opFixture(t)
	for _, kind := range []string{"jvm_heap_used", "jvm_heap_max"} {
		f.addAux(t, kind)
	}
	f.values(map[string]func(ts time.Time) float64{
		"memory":        func(ts time.Time) float64 { return daily(4*float64(costGiB), 1*float64(costGiB), ts) },
		"cpu":           func(ts time.Time) float64 { return daily(2, 0.8, ts) },
		"jvm_heap_used": func(time.Time) float64 { return 2 * float64(costGiB) },
		"jvm_heap_max":  func(time.Time) float64 { return 8 * float64(costGiB) },
	})
	f.withReplicas()
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, d.JVM)
	assert.True(t, d.JVM.Present)
	assert.Contains(t, d.JVM.Hints, "gc_missing")
	assert.NotContains(t, d.JVM.Hints, "committed_is_not_use", "nothing committed is shown")
	assert.Nil(t, d.JVM.HeapCommitted)
	assert.NotNil(t, d.Windows[0].Memory, "the analysis goes on")
}

// withReplicas keeps the replicas of the fixture at 4 whatever else f.values answers.
func (f *fixture) withReplicas() {
	inner := f.querier.value
	f.querier.value = func(query string, ts time.Time) float64 {
		if strings.Contains(query, "kube_deployment") {
			return 4
		}
		return inner(query, ts)
	}
}

// slowRange answers the queries that contain fast at once and makes the others wait for the context to end.
type slowRange struct {
	inner collector.RangeQuerier
	fast  string
}

func (q slowRange) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	if !strings.Contains(query, q.fast) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return q.inner.QueryRange(ctx, query, start, end, step)
}

// What the dashboard shows is read from Prometheus: with nothing stored for the module the detail is the same, and it never writes what it read.
func TestTheDetailDoesNotReadOrWriteTheStoredSamples(t *testing.T) {
	f := opFixture(t)
	f.withRequests(t, 1.5)
	require.NoError(t, f.db.Exec("DELETE FROM metric_sample").Error)
	var before int64
	require.NoError(t, f.db.Model(&model.MetricSample{}).Count(&before).Error)
	d, err := f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, d.Windows[0].CPU)
	assert.Equal(t, overprovision.StatusOver, d.Windows[0].CPU.Status)
	assert.Equal(t, 4, d.Replicas, "the replicas are a query too")
	var after int64
	require.NoError(t, f.db.Model(&model.MetricSample{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

// A slow Prometheus gives the metrics that came and tells which did not, in the time of the request: no 504.
func TestWhenPrometheusIsSlowTheDetailGivesWhatCameAndSaysWhatDidNot(t *testing.T) {
	f := opFixture(t)
	f.addAux(t, "k8s_cpu_throttle")
	f.addAux(t, "jvm_heap_used")
	f.withReplicas()
	inner := f.svc.NewQuerier
	f.svc.NewQuerier = func(ds *model.Datasource) (collector.RangeQuerier, error) {
		q, err := inner(ds)
		return slowRange{inner: q, fast: "cpu{"}, err
	}
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: timing.PrometheusTotalConcurrency.Key, ConfigValue: "64", ConfigName: "x"}).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Less(t, time.Since(started), 1500*time.Millisecond)
	assert.True(t, d.Partial)
	assert.Positive(t, d.Skipped)
	for _, q := range d.Queries {
		if q.Skipped {
			assert.Contains(t, q.Error, "time limit")
			assert.Zero(t, q.Points)
		}
	}
	assert.Nil(t, d.Windows[0].Memory, "the memory did not come, so there is no verdict made of nothing")
}

// withRequests declares the requests and the limits of the Pods as Prometheus metrics of the module (the dashboard reads them from nowhere else): 4 Pods that
// request 4 cores and 4 GiB and are limited to the given multiple of them.
func (f *fixture) withRequests(t *testing.T, limitTimes float64) {
	t.Helper()
	for _, kind := range []string{"k8s_cpu_requests", "k8s_mem_requests", "k8s_cpu_limits", "k8s_mem_limits"} {
		f.addAux(t, kind)
	}
	inner := f.querier.value
	f.querier.value = func(query string, ts time.Time) float64 {
		switch {
		case strings.Contains(query, "k8s_cpu_requests_series{"):
			return 16
		case strings.Contains(query, "k8s_mem_requests_series{"):
			return 16 * float64(costGiB)
		case strings.Contains(query, "k8s_cpu_limits_series{"):
			return 16 * limitTimes
		case strings.Contains(query, "k8s_mem_limits_series{"):
			return 16 * limitTimes * float64(costGiB)
		}
		return inner(query, ts)
	}
}

// vectorFn answers an instant query by its text.
type vectorFn func(query string) []promclient.VectorSample

func (v vectorFn) QueryVector(_ context.Context, query string, _ time.Time) ([]promclient.VectorSample, error) {
	return v(query), nil
}

// The table of the dashboard has the requests and the limits of each module, one Pod's, as the Prometheus data source answers them now; a module that has no such
// metric says which one is missing instead of showing what the platform holds.
func TestTheResourceTableIsTheRequestsAndLimitsPrometheusAnswers(t *testing.T) {
	f := opFixture(t)
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) {
		return vectorFn(func(q string) []promclient.VectorSample {
			value := 0.0
			switch {
			case strings.Contains(q, "kube_deployment"):
				value = 4
			case strings.Contains(q, "k8s_cpu_requests_series{"):
				value = 16
			case strings.Contains(q, "k8s_cpu_limits_series{"):
				value = 24
			case strings.Contains(q, "k8s_mem_requests_series{"):
				value = 16 * float64(costGiB)
			default:
				return nil
			}
			return []promclient.VectorSample{{Value: value}}
		}), nil
	}
	ctx := context.Background()
	// nothing declared on the module: no figure, and the table says what to add
	got, err := f.svc.OverprovisionResources(ctx)
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)
	row := got.Rows[0]
	assert.Nil(t, row.CPURequest)
	assert.Nil(t, row.MemoryLimit)
	assert.Contains(t, row.Errors["cpu_request"], "k8s_cpu_requests")
	assert.Contains(t, row.Errors["memory_limit"], "k8s_mem_limits")

	for _, kind := range []string{"k8s_cpu_requests", "k8s_cpu_limits", "k8s_mem_requests"} {
		f.addAux(t, kind)
	}
	got, err = f.svc.OverprovisionResources(ctx)
	require.NoError(t, err)
	row = got.Rows[0]
	require.NotNil(t, row.CPURequest)
	assert.InDelta(t, 4.0, *row.CPURequest, 1e-9, "16 cores over 4 replicas")
	require.NotNil(t, row.CPULimit)
	assert.InDelta(t, 6.0, *row.CPULimit, 1e-9)
	require.NotNil(t, row.MemoryRequest)
	assert.InDelta(t, 4*float64(costGiB), *row.MemoryRequest, 1)
	assert.Nil(t, row.MemoryLimit, "the memory limit metric is still not declared")
	assert.Contains(t, row.Errors, "memory_limit")
	assert.False(t, got.Partial)

	// the module says what its query answers: a query of one Pod is not divided by the replicas, a query of all of them is
	require.NoError(t, f.db.Model(&model.MetricSource{}).Where("kind = ?", "k8s_cpu_requests").Update("per", model.PerPod).Error)
	require.NoError(t, f.db.Model(&model.MetricSource{}).Where("kind = ?", "k8s_cpu_limits").Update("per", model.PerTotal).Error)
	got, err = f.svc.OverprovisionResources(ctx)
	require.NoError(t, err)
	row = got.Rows[0]
	assert.InDelta(t, 16.0, *row.CPURequest, 1e-9, "answered for one Pod")
	assert.InDelta(t, 6.0, *row.CPULimit, 1e-9, "answered for all the replicas: 24 over 4")
}

// Every module of the module management is on the dashboard, one that is switched off too.
func TestTheDashboardListsEveryModuleOfTheModuleManagement(t *testing.T) {
	f := opFixture(t)
	other := &model.ForecastModule{Name: "idle", Namespace: "prod", WorkloadKind: "Deployment", Workload: "idle", Enabled: false}
	require.NoError(t, f.db.Create(other).Error)
	got, err := f.svc.OverprovisionResources(context.Background())
	require.NoError(t, err)
	ids := []uint{}
	for _, row := range got.Rows {
		ids = append(ids, row.ModuleID)
	}
	assert.Contains(t, ids, other.ID)
	assert.Contains(t, ids, f.module.ID)
}

// The clusters are the data sources that have cluster metric templates, and the dashboard says how much of the set each declares.
func TestTheManagedClustersAreTheDataSourcesWithClusterMetricTemplates(t *testing.T) {
	f, _ := clusterFixture(t, "node_ready", "cluster_pods")
	got, err := f.svc.ClusterList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, got.Count)
	require.Len(t, got.Clusters, 1)
	c := got.Clusters[0]
	assert.Equal(t, f.cpuMap.DatasourceID, c.DatasourceID)
	assert.Equal(t, 16, c.Total)
	assert.Equal(t, 14, c.Declared)
	assert.Equal(t, []string{"cluster_pods", "node_ready"}, c.Missing)

	// the data source of the system parameter is a cluster even with nothing declared on it
	other := &model.Datasource{Name: "other", Type: "prometheus", BaseURL: "http://127.0.0.1:1", AuthType: "none", TimeoutSeconds: 5, MaxConcurrency: 1, Enabled: true}
	require.NoError(t, f.db.Create(other).Error)
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: timing.ClusterDatasource.Key, ConfigValue: strconv.Itoa(int(other.ID)), ConfigName: "x"}).Error)
	got, err = f.svc.ClusterList(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, got.Count)
	assert.True(t, got.Clusters[0].Primary, "the primary cluster is first")
	assert.Equal(t, 0, got.Clusters[0].Declared)
}

// The Requests of a module are the query of the template the module bound to them, whichever kind the template was filed under; every metric the module has is
// asked and listed (two CPU templates are two queries), and what is only shown is collected for the history but never read by an algorithm.
func TestTheRequestsAreReadFromTheTemplateTheModuleBoundAndEveryMetricIsListed(t *testing.T) {
	f := opFixture(t)
	f.withReplicas()
	ctx := context.Background()
	mk := func(name, metric, series string) uint {
		mp := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: name, Metric: metric, Level: "workload", Unit: model.MetricKinds[metric], StepSeconds: 60, Enabled: true,
			Query: `sum(` + series + `{namespace="{{namespace}}",pod=~"{{workload}}-.*"})`}
		require.NoError(t, f.db.Create(mp).Error)
		return mp.ID
	}
	// the template of cores that a user files under CPU, written for the Requests
	f.maps["k8s_cpu_requests"] = mk("dc-cpu-requests", "cpu", "k8s_cpu_requests_series")
	f.addSourceTo(t, nil, "k8s_cpu_requests")
	// a second CPU metric: nothing for the algorithms to confuse with the first
	alt := &model.MetricSource{ModuleID: f.module.ID, Name: "cpu-alt", Kind: "cpu", MappingID: mk("cpu-alt", "cpu", "cpu_alt_series"), Weight: 1, Stat: "p95", Enabled: true, Priority: 90}
	require.NoError(t, alt.Validate())
	require.NoError(t, f.svc.Modules.SaveSource(ctx, alt))
	inner := f.querier.value
	f.querier.value = func(query string, ts time.Time) float64 {
		if strings.Contains(query, "k8s_cpu_requests_series{") {
			return 16
		}
		if strings.Contains(query, "cpu_alt_series{") {
			return 8
		}
		return inner(query, ts)
	}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) {
		return vectorFn(func(q string) []promclient.VectorSample {
			switch {
			case strings.Contains(q, "kube_deployment"):
				return []promclient.VectorSample{{Value: 4}}
			case strings.Contains(q, "k8s_cpu_requests_series{"):
				return []promclient.VectorSample{{Value: 16}}
			}
			return nil
		}), nil
	}

	got, err := f.svc.OverprovisionResources(ctx)
	require.NoError(t, err)
	row := got.Rows[0]
	require.NotNil(t, row.CPURequest, "%v", row.Errors)
	assert.InDelta(t, 4.0, *row.CPURequest, 1e-9, "16 cores over 4 replicas")

	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	cpuQueries, shown := 0, 0
	for _, q := range d.Queries {
		if q.Kind == "cpu" {
			cpuQueries++
		}
		if q.Kind == "k8s_cpu_requests" {
			assert.True(t, q.Display, "the Requests are a display metric")
			assert.Equal(t, "dc-cpu-requests", q.Template)
			shown++
		}
	}
	assert.Equal(t, 2, cpuQueries, "both CPU templates are asked")
	assert.Equal(t, 1, shown)
	assert.InDelta(t, 4.0, d.Windows[0].CPU.Request, 1e-9)
	names := []string{}
	for _, m := range d.Metrics {
		names = append(names, m.Name)
	}
	assert.Contains(t, names, "cpu-alt", "the other CPU metric is listed with its quantiles")

	// the algorithms do not read the display metric; the history collects it
	mod, err := f.svc.Modules.Get(ctx, f.module.ID)
	require.NoError(t, err)
	for _, src := range (&model.AlgorithmRun{Sources: mod.Sources}).ActiveSources() {
		assert.NotEqual(t, "k8s_cpu_requests", src.Kind)
	}
	collected, err := f.collect(ctx)
	require.NoError(t, err)
	keys := []string{}
	for _, c := range collected {
		keys = append(keys, c.Metric)
	}
	found := false
	for _, k := range keys {
		found = found || strings.HasPrefix(k, "k8s_cpu_reques")
	}
	assert.True(t, found, "the history collects the display metric: %v", keys)
}

// A query pasted from Grafana still has its variables: when it finds nothing the answer says so.
func TestAGrafanaVariableInAQueryIsNamedWhenNothingIsFound(t *testing.T) {
	assert.Contains(t, grafanaHint(`up{cluster="$cluster", namespace="${namespace}"}`), "$cluster")
	assert.Equal(t, "", grafanaHint(`up{namespace="prod"} and foo$`))
}

// A query that answers a series for each Pod says how to join them, and a template whose name and query disagree is pointed out.
func TestAQueryOfManySeriesAndAMisnamedTemplateAreExplained(t *testing.T) {
	assert.Contains(t, seriesHint("permanent query error: query returned more than one series; aggregate it to a single series (95 series)"), "sum(...)")
	assert.Equal(t, "", seriesHint("no data"))
	assert.Contains(t, nameMismatchNote("demo-cpu-limits", `kube_pod_container_resource_requests{resource="cpu"}`), "resource_requests")
	assert.Contains(t, nameMismatchNote("demo-cpu-requests", `sum(kube_pod_container_resource_limits)`), "resource_limits")
	assert.Equal(t, "", nameMismatchNote("demo-cpu-limits", `sum(kube_pod_container_resource_limits)`))
}

// The Requests of 12 Pods are summed to 48 cores while 11 of them are ready: one Pod is 4 cores, not 4.36, and the 6 GiB of the memory are not 6.55.
func TestThePodsOfASumOfRequestsAreTheOnesThatGiveAFigureASpecCanSay(t *testing.T) {
	assert.InDelta(t, 12.0, podsOfTotal(48, 11, "cores"), 1e-9, "48 cores over 11 is 4.36, over 12 is 4")
	assert.InDelta(t, 12.0, podsOfTotal(72*float64(costGiB), 11, "bytes"), 1e-9, "72 GiB over 12 is 6 GiB")
	assert.InDelta(t, 11.0, podsOfTotal(44, 11, "cores"), 1e-9, "the ready replicas themselves when they give a figure")
	assert.InDelta(t, 11.0, podsOfTotal(48.7, 11, "cores"), 1e-9, "no count near gives one: the ready replicas")
	assert.InDelta(t, 1.0, podsOfTotal(3, 0, "cores"), 1e-9)
	assert.True(t, specLike(0.25, "cores"))
	assert.True(t, specLike(1.5, "cores"))
	assert.False(t, specLike(4.3636, "cores"))
	assert.True(t, specLike(6*float64(costGiB), "bytes"))
	assert.True(t, specLike(512*float64(1<<20), "bytes"))
	assert.False(t, specLike(6.5454*float64(costGiB), "bytes"))
}

// The recommendation has a period of its own: the window of the page changes the usage figures and the charts, not the verdict.
func TestTheRecommendationDoesNotFollowTheWindowOfThePage(t *testing.T) {
	f := opFixture(t)
	f.withRequests(t, 1.5)
	ctx := context.Background()
	short, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 60, time.Time{}, time.Time{})
	require.NoError(t, err)
	long, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, short.Recommend)
	require.NotNil(t, long.Recommend)
	assert.Equal(t, 60, short.Windows[0].WindowMinutes)
	assert.Equal(t, 24*60, long.Windows[0].WindowMinutes)
	for _, d := range []*DetailOverprovision{short, long} {
		assert.Equal(t, 7*24*60, d.Recommend.WindowMinutes, "the period of the recommendation is the system parameter")
		assert.Equal(t, "7d", d.Recommend.Label)
		require.NotNil(t, d.Recommend.CPU)
	}
	assert.Equal(t, long.Recommend.CPU.Status, short.Recommend.CPU.Status)
	assert.InDelta(t, long.Recommend.CPU.Suggested, short.Recommend.CPU.Suggested, 1e-9)
	assert.NotZero(t, short.Recommend.CPU.Needed)
	// the line of the recommended Requests in the chart is the recommendation's
	assert.InDelta(t, long.Recommend.CPU.Suggested, long.Charts[0].Suggest, 1e-9)
}

// The totals of the Requests and the Limits are what Prometheus summed, and the figure of one Pod is that sum over the Pods it was made of: 12 Pods of 4 cores are 48 cores
// and 4 cores, whatever number of them is ready.
func TestTheRequestsOfTwelvePodsWithElevenReadyAreStillFourCoresAPod(t *testing.T) {
	f := opFixture(t)
	f.withRequests(t, 1.5)
	inner := f.querier.value
	f.querier.value = func(query string, ts time.Time) float64 {
		switch {
		case strings.Contains(query, "k8s_cpu_requests_series{"):
			return 48
		case strings.Contains(query, "k8s_cpu_limits_series{"):
			return 96
		case strings.Contains(query, "kube_deployment"):
			return 11
		}
		return inner(query, ts)
	}
	d, err := f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 11, d.Replicas)
	cpu := d.Windows[0].CPU
	require.NotNil(t, cpu)
	assert.InDelta(t, 4.0, cpu.Request, 1e-9, "48 cores of 12 Pods, not 48 over 11 ready")
	assert.InDelta(t, 8.0, cpu.Limit, 1e-9)
	assert.InDelta(t, 48.0, cpu.RequestTotal, 1e-9)
	assert.InDelta(t, 96.0, cpu.LimitTotal, 1e-9)
}
