package forecastsvc

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kubeforecast/pkg/decision"
	"github.com/cylonchau/kubeforecast/pkg/model"
)

const gib = float64(1 << 30)

// auxInstance is the module as the owner of its measured context sources (OOM, heap, limits, ready replicas): the
// OOM floor and the heap rule read them from the module's own sources.
func (f *fixture) auxInstance(t *testing.T) *model.AlgorithmRun {
	t.Helper()
	mod, err := f.svc.Modules.Get(context.Background(), f.module.ID)
	require.NoError(t, err)
	return mod.Data()
}

// addAux maps an auxiliary or memory metric to a query the synthetic querier recognises. An auxiliary kind becomes
// a source of the module; memory is only mapped, a bind adds it to the module.
func (f *fixture) addAux(t *testing.T, kind string) {
	t.Helper()
	mapping := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: kind, Metric: kind, Level: "workload", Unit: model.MetricKinds[kind],
		StepSeconds: 60, Enabled: true, Query: `sum(` + kind + `_series{namespace="{{namespace}}",pod=~"{{workload}}-.*"})`}
	require.NoError(t, f.db.Create(mapping).Error)
	f.maps[kind] = mapping.ID
	if kind == model.MetricMemory {
		return
	}
	f.addSourceTo(t, f.auxInstance(t), kind)
}

func (f *fixture) values(by map[string]func(ts time.Time) float64) {
	f.querier.value = func(query string, ts time.Time) float64 {
		for kind, fn := range by {
			if strings.Contains(query, kind+"_series{") {
				return fn(ts)
			}
		}
		if strings.Contains(query, "cpu{") {
			return daily(1, 0.5, ts)
		}
		return daily(300, 100, ts)
	}
}

func (f *fixture) cycle(t *testing.T) *PipelineResult {
	t.Helper()
	res, err := f.svc.RunPipeline(context.Background(), f.module.ID)
	require.NoError(t, err)
	return res
}

func TestHealthyRecommendationIsNotDegraded(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	res := f.cycle(t)
	require.NotNil(t, res.Recommend)
	assert.False(t, res.Recommend.Degraded)
	assert.False(t, res.Recommend.OOMFloor)
	assert.Empty(t, res.Recommend.DegradeReasons)
}

func TestDegradationReasonCodes(t *testing.T) {
	ctx := context.Background()
	reason := func(f *fixture, field string) (string, *model.Recommendation) {
		t.Helper()
		rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
		require.NoError(t, err)
		return detail.Degraded[field], rec
	}

	t.Run("no history at all", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		code, rec := reason(f, "cpu")
		assert.Equal(t, DegradeNoData, code)
		assert.True(t, rec.Degraded)
		assert.Equal(t, "fallback", rec.CPUSource)
		assert.JSONEq(t, `{"cpu":"no_data"}`, rec.DegradeReasons)
	})

	t.Run("history with too many gaps", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		f.querier.skip = func(ts time.Time) bool { return ts.Minute()%2 == 0 } // half of the steps are missing
		_, err := f.collect(ctx)
		require.NoError(t, err)
		_, err = f.predict(ctx)
		require.NoError(t, err)
		code, _ := reason(f, "cpu")
		assert.Equal(t, DegradeInsufficient, code)
	})

	t.Run("stale history", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		_, err := f.collect(ctx)
		require.NoError(t, err)
		*f.clock = testNow.Add(3 * time.Hour)
		code, _ := reason(f, "cpu")
		assert.Equal(t, DegradeStaleData, code)
	})

	t.Run("data is fine but nothing was predicted yet", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		_, err := f.collect(ctx)
		require.NoError(t, err)
		code, _ := reason(f, "cpu")
		assert.Equal(t, DegradeNoPrediction, code)
	})

	t.Run("only an unpromoted algorithm", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 0)
		_, err := f.collect(ctx)
		require.NoError(t, err)
		_, err = f.predict(ctx)
		require.NoError(t, err)
		code, _ := reason(f, "cpu")
		assert.Equal(t, DegradeNotEligible, code)
	})

	t.Run("a stale forecast", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		_, err := f.collect(ctx)
		require.NoError(t, err)
		_, err = f.predict(ctx)
		require.NoError(t, err)
		// collection keeps up, the predictor does not
		*f.clock = testNow.Add(20 * time.Minute)
		_, err = f.collect(ctx)
		require.NoError(t, err)
		code, _ := reason(f, "cpu")
		assert.Equal(t, DegradeStalePrediction, code)
	})

	t.Run("the predictor failed in this cycle", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0)
		bad := f.bind(t, "cpu", "ridge_ar", map[string]any{"lags": 12}, 1)
		// a parameter set that the algorithm rejects when it runs (the API refuses to store it)
		require.NoError(t, f.db.Model(&model.AlgorithmConfig{}).Where("algorithm_id = ?", bad.AlgorithmID).Update("params", `{"lags":0}`).Error)
		_, err := f.collect(ctx)
		require.NoError(t, err)
		rec, detail, err := f.svc.recommend(ctx, f.module.ID, map[string]string{"ridge_ar/cpu": "boom"}, false)
		require.NoError(t, err)
		// naive is a usable baseline, so nothing degrades yet because no forecast exists; the failure explains why
		assert.Equal(t, DegradePredictorFailed, detail.Degraded["cpu"])
		assert.Contains(t, detail.DegradeNotes["cpu"], "boom")
		assert.True(t, rec.Degraded)
	})

	t.Run("fixed periods and a metric without a mapping are not degradations", func(t *testing.T) {
		f := newFixture(t)
		f.bind(t, "cpu", "naive", nil, 0) // rps has a mapping but no algorithm; memory has no mapping at all
		_, err := f.collect(ctx)
		require.NoError(t, err)
		_, err = f.predict(ctx)
		require.NoError(t, err)
		_, detail, err := f.svc.Recommend(ctx, f.module.ID)
		require.NoError(t, err)
		assert.Empty(t, detail.Degraded)
	})
}

func TestRecoveryNeedsConsecutiveHealthyCycles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.module.RecoverCycles = 3
	require.NoError(t, f.db.Save(f.module).Error)
	// the previous cycle was degraded for cpu
	require.NoError(t, f.svc.RunStates.SaveRunState(ctx, &model.ModuleRunState{ModuleID: f.module.ID, ConsecutiveFailures: 1,
		DegradedFields: `{"cpu":"predictor_failed"}`}))

	sources := []string{}
	codes := []string{}
	for i := 0; i < 4; i++ {
		res := f.cycle(t)
		require.NotNil(t, res.Recommend, "%v", res.Errors)
		sources = append(sources, res.Recommend.CPUSource)
		var degraded map[string]string
		if res.Recommend.DegradeReasons != "" {
			require.NoError(t, json.Unmarshal([]byte(res.Recommend.DegradeReasons), &degraded))
		}
		codes = append(codes, degraded["cpu"])
		*f.clock = f.clock.Add(time.Minute)
	}
	assert.Equal(t, []string{"fallback", "fallback", "algorithm", "algorithm"}, sources, "held for two cycles, released on the third")
	assert.Equal(t, []string{DegradeRecovering, DegradeRecovering, "", ""}, codes)

	st, err := f.svc.RunStates.RunState(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Empty(t, st.DegradedFields, "the memory of the degradation is cleared once recovered")
	assert.Zero(t, st.ConsecutiveFailures)
	assert.NotNil(t, st.LastSuccessAt)
}

func TestRunStateCountsFailuresAndResetsHealth(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// no bindings: predict fails every cycle
	f.cycle(t)
	f.cycle(t)
	st, err := f.svc.RunStates.RunState(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, st.ConsecutiveFailures)
	assert.NotEmpty(t, st.LastError)
	assert.Zero(t, st.HealthyCycles)

	f.bind(t, "cpu", "naive", nil, 0)
	f.cycle(t)
	st, err = f.svc.RunStates.RunState(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Zero(t, st.ConsecutiveFailures)
	assert.Empty(t, st.LastError)
}

func TestOOMFloorRaisesTheMemoryRecommendation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, kind := range []string{model.MetricMemory, "oom", "oom_working_set", "k8s_mem_limits"} {
		f.addAux(t, kind)
	}
	f.bind(t, "memory", "naive", nil, 0)
	oomAt := testNow.Add(-5 * time.Hour)
	f.values(map[string]func(time.Time) float64{
		"memory":          func(time.Time) float64 { return 1 * gib },
		"oom_working_set": func(time.Time) float64 { return 3 * gib },
		"k8s_mem_limits":  func(time.Time) float64 { return 2 * gib },
		"oom": func(ts time.Time) float64 {
			if ts.Equal(oomAt) {
				return 1
			}
			return 0
		},
	})
	res := f.cycle(t)
	require.NotNil(t, res.Recommend, "%v", res.Errors)
	rec := res.Recommend
	assert.True(t, rec.OOMFloor)
	assert.EqualValues(t, int64(math.Ceil(3*gib*1.2)), rec.MemoryBytes, "max(working set 3 GiB, limit 2 GiB) x 1.2 beats the 1 GiB forecast")
	assert.Equal(t, "algorithm", rec.MemorySource)

	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.OOM)
	assert.True(t, detail.OOM.Active)
	assert.Equal(t, 1, detail.OOM.Events)
	assert.True(t, detail.Memory.Floored)
	assert.Contains(t, detail.Memory.Reason, "OOM floor")

	// no OOM inside the lookback: no floor, and the forecast stands
	f.module.OOMLookbackDays = 1
	require.NoError(t, f.db.Save(f.module).Error)
	*f.clock = testNow.Add(30 * time.Hour)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec2, detail2, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, rec2.OOMFloor)
	assert.False(t, detail2.OOM.Active)
	assert.InDelta(t, 1.1*gib, float64(rec2.MemoryBytes), 1024, "the 1 GiB forecast with the module headroom of 1.1")
}

func TestOOMWithoutABaselineIsReportedButGivesNoFloor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addAux(t, "oom")
	f.values(map[string]func(time.Time) float64{"oom": func(ts time.Time) float64 {
		if ts.Equal(testNow.Add(-2 * time.Hour)) {
			return 1
		}
		return 0
	}})
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	mod, err := f.svc.Modules.Get(ctx, f.module.ID)
	require.NoError(t, err)
	bs, err := f.svc.boundSet(ctx, mod)
	require.NoError(t, err)
	floor, err := f.svc.oomFloor(ctx, f.module, bs, testNow)
	require.NoError(t, err)
	assert.False(t, floor.Active)
	assert.Equal(t, 1, floor.Events)
	assert.Contains(t, floor.Reason, "no working set")
}

func TestHeapPressureRaisesDynamicReplicas(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addAux(t, "jvm_heap_used")
	f.addAux(t, "jvm_heap_max")
	used := 0.5
	f.values(map[string]func(time.Time) float64{
		"jvm_heap_used": func(time.Time) float64 { return used * gib },
		"jvm_heap_max":  func(time.Time) float64 { return gib },
	})
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.dynamic(t, func(m *model.ForecastModule) { m.JVMHeapPressure = 0.8 })

	calm, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, detail.Heap.Over)
	assert.NotEqual(t, decision.ConstraintJVMHeap, detail.Replica.Plans[0].Binding)

	// a hot heap: collect again with the new values
	used = 0.95
	*f.clock = testNow.Add(time.Minute)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	hot, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.True(t, detail.Heap.Over)
	assert.InDelta(t, 0.95, detail.Heap.Ratio, 0.001)
	assert.Equal(t, decision.ConstraintJVMHeap, detail.Replica.Plans[0].Binding)
	assert.Greater(t, hot.Replicas, calm.Replicas)
	assert.Contains(t, strings.Join(detail.Replica.Notes, " "), "JVM heap pressure")
}
