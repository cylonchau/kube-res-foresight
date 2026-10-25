package forecastsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kubeforecast/pkg/model"
)

// addSource gives the module another metric source of a kind, mapped to a query the synthetic querier recognises by name.
func (f *fixture) addSource(t *testing.T, inst *model.AlgorithmRun, name, kind string, weight float64) *model.MetricSource {
	t.Helper()
	mapping := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: name, Metric: kind, Level: "workload", Unit: model.MetricKinds[kind],
		StepSeconds: 60, Enabled: true, Query: `sum(` + name + `_series{namespace="{{namespace}}",pod=~"{{workload}}-.*"})`}
	require.NoError(t, f.db.Create(mapping).Error)
	src := &model.MetricSource{ModuleID: inst.ID, Name: name, Kind: kind, MappingID: mapping.ID, Weight: weight, Stat: "max", Enabled: true, Priority: 10}
	require.NoError(t, src.Validate())
	require.NoError(t, f.svc.Modules.SaveSource(context.Background(), src))
	return src
}

func (f *fixture) setModule(t *testing.T, mutate func(*model.ForecastModule)) {
	t.Helper()
	m, err := f.svc.Modules.Get(context.Background(), f.module.ID)
	require.NoError(t, err)
	mutate(m)
	require.NoError(t, m.Validate())
	require.NoError(t, f.svc.Modules.Update(context.Background(), m))
	f.module = m
}

func constant(by map[string]float64, otherwise func(time.Time) float64) func(string, time.Time) float64 {
	return func(query string, ts time.Time) float64 {
		for name, v := range by {
			if strings.Contains(query, name) {
				return v
			}
		}
		return otherwise(ts)
	}
}

func TestSourcesOfOneResourceAreCombinedByWeight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	inst := f.bind(t, "cpu", "naive", nil, 0)
	second := f.addSource(t, inst, "cpub", "cpu", 3)
	require.Equal(t, "cpu_2", second.Key, "a second source of a kind gets its own series key")
	f.querier.value = constant(map[string]float64{"cpub_series": 3, "cpu{": 1}, func(ts time.Time) float64 { return daily(300, 100, ts) })
	f.setModule(t, func(m *model.ForecastModule) { m.HeadroomRatio = 1 })

	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.InDelta(t, 2500, rec.CPUMilli, 2, "(1 x 1 + 3 x 3) / 4 cores: the weighted average of the two sources")
	require.NotNil(t, detail.Multi)
	assert.Len(t, detail.Multi.Rows, 2, "the explanation lists the figures of each source")
}

func scoredFixture(t *testing.T, algorithms ...string) *fixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	for i, a := range algorithms {
		params := map[string]any(nil)
		if a == "seasonal_naive" {
			params = map[string]any{"season_length_steps": 1440}
		}
		f.bind(t, "cpu", a, params, i)
	}
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	_, err = f.backtest(ctx, "cpu", 4)
	require.NoError(t, err)
	return f
}

func TestScoringRanksTheInstancesAndTheBestEligibleOneDecides(t *testing.T) {
	f := scoredFixture(t, "naive", "seasonal_naive")
	ctx := context.Background()
	scores, err := f.svc.ScoreModule(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, scores, 2)
	byName := map[string]*model.AlgorithmScore{}
	for _, s := range scores {
		byName[s.Algorithm] = s
		assert.Greater(t, s.Windows, 0)
	}
	assert.Greater(t, byName["seasonal_naive"].Score, byName["naive"].Score, "a daily pattern is reproduced by the seasonal algorithm only")
	assert.Equal(t, 1, byName["seasonal_naive"].Rank)

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "seasonal_naive", detail.Algorithm)
	assert.Equal(t, "seasonal_naive", rec.Algorithm)
	assert.InDelta(t, byName["seasonal_naive"].Score, rec.Score, 0.01)
	require.NotEmpty(t, detail.Selection)
	assert.Equal(t, "seasonal_naive", detail.Selection[0].Algorithm)
	assert.True(t, detail.Selection[0].Chosen)

	// the stored history answers the ranking and the latest score of each instance
	latest, err := f.svc.Predictions.LatestScores(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Len(t, latest, 2)
}

func TestAScoreBelowTheModuleMinimumFallsBackWithTheReason(t *testing.T) {
	f := scoredFixture(t, "naive")
	ctx := context.Background()
	_, err := f.svc.ScoreModule(ctx, f.module.ID)
	require.NoError(t, err)
	f.setModule(t, func(m *model.ForecastModule) { m.MinScore = 100 })

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, DegradeLowScore, detail.Degraded["cpu"])
	assert.Equal(t, "fallback", rec.CPUSource)
	assert.EqualValues(t, 500, rec.CPUMilli, "the module's fallback value")
	assert.True(t, rec.Degraded)
	assert.Contains(t, detail.Selection[0].Reason, "below the module minimum")
}

func TestTheReplicaCeilingExpandsStepByStepUpToTheHardMaximum(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.querier.value = constant(map[string]float64{"cpu{": 1}, func(time.Time) float64 { return 1000 }) // 1000 rps, 100 per pod: 11 replicas
	f.bind(t, "rps", "naive", nil, 0)
	f.setModule(t, func(m *model.ForecastModule) {
		m.HeadroomRatio, m.MinReplicas, m.MaxReplicas = 1, 2, 5
		m.MaxReplicasMode, m.HardMaxReplicas, m.ExpandStepRatio = model.MaxReplicasDynamic, 8, 0.2
	})

	var seen []int
	for i := 0; i < 3; i++ {
		res := f.cycle(t)
		require.NotNil(t, res.Recommend, "%v", res.Errors)
		seen = append(seen, res.Recommend.Replicas)
		*f.clock = f.clock.Add(time.Minute)
	}
	assert.Equal(t, []int{6, 8, 8}, seen, "5 -> 6 (a 20% step) -> 8 (the hard maximum), never above it")
	st, err := f.svc.RunStates.RunState(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, st.EffectiveMaxReplicas)

	// a fixed ceiling never expands
	f.setModule(t, func(m *model.ForecastModule) { m.MaxReplicasMode = model.MaxReplicasFixed })
	res := f.cycle(t)
	assert.Equal(t, 5, res.Recommend.Replicas)
}

func TestAModuleCollectsPredictsAndBacktestsWithItsOwnSourcesAndTheEnabledAlgorithms(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// nothing is created or bound: the module has a source and one algorithm is enabled
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)

	collected, err := f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, collected, 1)
	assert.Empty(t, collected[0].Error)
	outcomes, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	assert.Equal(t, StatusOK, outcomes[0].Status, outcomes[0].Reason)
	runs, err := f.svc.Backtest(ctx, f.module.ID, "cpu", 3)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.True(t, runs[0].Promoted, "a baseline is always eligible")
	res, err := f.svc.RunData(ctx, f.module.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Errors)

	// enabling another algorithm reaches the module on the next cycle, with nothing to create or bind
	f.bind(t, "cpu", "ewma", nil, 1)
	outcomes, err = f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Len(t, outcomes, 2)
	// and disabling it takes it away again
	cfg, err := f.svc.Algorithms.Get(ctx, "ewma")
	require.NoError(t, err)
	cfg.Enabled = false
	require.NoError(t, f.svc.Algorithms.Save(ctx, cfg))
	outcomes, err = f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Len(t, outcomes, 1)
}

func TestAnAlgorithmEnabledAfterAModuleWasCreatedAppliesToItAndEveryOtherModule(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	second := &model.ForecastModule{Name: "web", Enabled: true, Mode: model.ModeRecommend, Namespace: "prod", Workload: "web"}
	require.NoError(t, second.Validate())
	require.NoError(t, f.svc.Modules.Create(ctx, second))
	require.NoError(t, f.svc.Modules.SaveSource(ctx, &model.MetricSource{ModuleID: second.ID, Name: "cpu", Kind: "cpu", MappingID: f.cpuMap.ID, Weight: 1, Stat: "max", Enabled: true}))

	for _, id := range []uint{f.module.ID, second.ID} {
		_, err := f.svc.Collect(ctx, id)
		require.NoError(t, err)
		outcomes, err := f.svc.Predict(ctx, id)
		require.NoError(t, err)
		require.Len(t, outcomes, 1, "the module was never told about the algorithm: it is associated automatically")
		assert.Equal(t, "naive", outcomes[0].AlgorithmID)
	}
	f.bind(t, "cpu", "ewma", nil, 1)
	for _, id := range []uint{f.module.ID, second.ID} {
		outcomes, err := f.svc.Predict(ctx, id)
		require.NoError(t, err)
		assert.Len(t, outcomes, 2)
	}
	// the history is the module's, and each module has its own predictions
	var series int64
	require.NoError(t, f.db.Model(&model.MetricSeries{}).Count(&series).Error)
	assert.EqualValues(t, 2, series)
}

func TestTheAlgorithmsOfAModuleShareItsHistoryAndKeepSeparateForecasts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	var series int64
	require.NoError(t, f.db.Model(&model.MetricSeries{}).Count(&series).Error)
	assert.EqualValues(t, 1, series, "the module's source is collected once, whatever the number of algorithms")
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "algorithm", rec.CPUSource)
	assert.Len(t, detail.Selection, 2, "both enabled algorithms take part in the ranking")
}

func TestADaemonSetHasNoReplicaCount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	f.setModule(t, func(m *model.ForecastModule) { m.WorkloadKind = model.KindDaemonSet })
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.True(t, rec.CPUSet, "its requests are adjusted")
	assert.False(t, rec.ReplicasSet, "its replicas are never set")
}
