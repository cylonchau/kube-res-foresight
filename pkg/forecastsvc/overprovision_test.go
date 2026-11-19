package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
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

func TestTheDetailHasTheChartTheMovingAveragesAndTheHistory(t *testing.T) {
	f := opFixture(t)
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
	assert.Len(t, c.MA5, len(c.TS))
	assert.Len(t, c.MA25, len(c.TS))
	assert.Equal(t, 300, c.StepSecs, "a window of a day is read every 5 minutes")
	assert.Equal(t, []string{"MA5@5m", "MA15@5m", "MA25@5m"}, c.Labels)
	assert.InDelta(t, 4.0, c.Request, 1e-9)
	assert.Nil(t, d.JVM, "the module has no JVM metric")
	assert.NotEmpty(t, d.History)
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

// A long window is thinned for the chart, but the moving averages keep the step of the window: MA5@5m is five points of five minutes whatever the
// chart shows.
func TestALongWindowIsThinnedForTheChartAndKeepsTheStepOfItsMovingAverages(t *testing.T) {
	f := opFixture(t)
	ctx := context.Background()
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 7*24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, d.Charts)
	c := d.Charts[0]
	assert.LessOrEqual(t, len(c.TS), opMaxPoints)
	assert.Len(t, c.MA15, len(c.TS))
	assert.Equal(t, 300, c.StepSecs)
	assert.Equal(t, "MA5@5m", c.Labels[0])
}

func TestOpChartThinsLongSeriesAndKeepsTheLabelsTrue(t *testing.T) {
	pts := make([]overprovision.Point, 2016) // 7 days every 5 minutes
	for i := range pts {
		pts[i] = overprovision.Point{TS: int64(i) * 300, V: float64(i % 10)}
	}
	c := opChart("cpu", pts, 300, 7*24*60, &overprovision.Result{Request: 4, Suggested: 1})
	assert.LessOrEqual(t, len(c.TS), opMaxPoints)
	assert.Greater(t, len(c.TS), opMaxPoints/2)
	assert.Len(t, c.Value, len(c.TS))
	assert.Len(t, c.MA25, len(c.TS))
	assert.Equal(t, []string{"MA5@5m", "MA15@5m", "MA25@5m"}, c.Labels)
	assert.Equal(t, 300, c.StepSecs)
	// the second point of the thinned series is the 8th point of the full one (stride 7): the label's step is not the thinned step
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
func TestTheLimitComesFromTheModuleRatioWhenNoMetricSaysIt(t *testing.T) {
	f := opFixture(t)
	f.setModule(t, func(m *model.ForecastModule) { m.LimitCPURatio, m.LimitMemoryRatio = 2, 1.5 })
	d, err := f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	cpu, mem := d.Windows[0].CPU, d.Windows[0].Memory
	require.NotNil(t, cpu)
	require.NotNil(t, mem)
	assert.InDelta(t, 8.0, cpu.Limit, 1e-9, "4 cores x 2")
	assert.Equal(t, "module", cpu.LimitFrom)
	assert.InDelta(t, 6*float64(costGiB), mem.Limit, 1, "4 GiB x 1.5")
	f.setModule(t, func(m *model.ForecastModule) { m.LimitCPURatio, m.LimitMemoryRatio = 0, 0 })
	d, err = f.svc.OverprovisionDetail(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, 0.0, d.Windows[0].CPU.Limit)
	assert.Empty(t, d.Windows[0].CPU.LimitFrom)
}

func TestTheClusterCapacityIsReadFromTheDataSourceOfTheModule(t *testing.T) {
	f := opFixture(t)
	got, err := f.svc.ClusterCapacityOf(context.Background(), f.module.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Nodes)
	assert.InDelta(t, 16.0, got.AllocatableCPU, 1e-9)
	assert.InDelta(t, 64*float64(costGiB), got.AllocatableMemory, 1)
	assert.NotEmpty(t, got.Datasource.Name)
	_, err = f.svc.ClusterCapacityOf(context.Background(), 9999, 0)
	assert.Error(t, err)
}
