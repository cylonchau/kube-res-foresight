package forecastsvc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
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
	f.perPodLevel(t) // the values are one Pod's (a workload total is spread over the Pods, see TestAWorkloadTotalIsSpreadOverThePodsForTheirRequests)

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
	second := &model.ForecastModule{PredictEnabled: true, Name: "web", Enabled: true, Mode: model.ModeRecommend, Namespace: "prod", Workload: "web"}
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

// forecastsOf stores forecasts the platform had for the last steps, made 5 minutes before each: the value of each step comes from
// value(actual), so a test chooses how well an algorithm followed the load.
func (f *fixture) forecastsOf(t *testing.T, metric, algorithm string, steps int, value func(actual float64) float64) {
	t.Helper()
	ctx := context.Background()
	series, err := f.svc.Samples.Series(ctx, f.module.ID, metric)
	require.NoError(t, err)
	step := time.Duration(series.StepSeconds) * time.Second
	now := floorToStep(testNow, step)
	samples, err := f.svc.Samples.Range(ctx, series.ID, now.Add(-time.Duration(steps-1)*step), now)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(samples), steps)
	for _, sm := range samples {
		target := time.Unix(sm.TS, 0).UTC()
		require.NoError(t, f.db.Create(&model.Prediction{ModuleID: f.module.ID, Metric: metric, AlgorithmID: algorithm, ModelVersion: "lag",
			PredictionTime: target.Add(-5 * time.Minute), TargetTime: target, QuantilePermille: 500, Value: value(sm.Value), Unit: "cores"}).Error)
	}
}

// The algorithm with the best replay score is not followed once its newest forecasts have missed the load by far: the score is
// refreshed every few hours and looks back over old windows, a load that has just changed shows at once in what it forecast a
// few minutes ago. Before this, a top algorithm that copied the past stayed in use through a drop of 90% until its score caught up.
func TestAnAlgorithmThatHasJustMissedTheLoadByFarIsNotFollowed(t *testing.T) {
	f := scoredFixture(t, "naive", "seasonal_naive")
	ctx := context.Background()
	scores, err := f.svc.ScoreModule(ctx, f.module.ID)
	require.NoError(t, err)
	byName := map[string]*model.AlgorithmScore{}
	for _, s := range scores {
		byName[s.Algorithm] = s
	}
	require.Greater(t, byName["seasonal_naive"].Score, byName["naive"].Score)

	// without anything to compare, the score decides, as it did
	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "seasonal_naive", detail.Algorithm)

	// the last ten steps: naive followed the load, seasonal_naive forecast ten times what happened
	f.forecastsOf(t, "cpu", "naive", 10, func(a float64) float64 { return a })
	f.forecastsOf(t, "cpu", "seasonal_naive", 10, func(a float64) float64 { return a*10 + 1 })
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "naive", detail.Algorithm, "the algorithm that followed the load, though its score is lower")
	assert.Equal(t, "naive", rec.Algorithm)
	var lagging, following SelectionRow
	for _, row := range detail.Selection {
		switch row.Algorithm {
		case "seasonal_naive":
			lagging = row
		case "naive":
			following = row
		}
	}
	assert.True(t, lagging.Lagging)
	assert.False(t, lagging.Eligible)
	assert.True(t, lagging.Tracked)
	assert.Equal(t, 10, lagging.TrackingPoints)
	assert.Greater(t, lagging.TrackingWAPE, 5.0)
	assert.Greater(t, lagging.TrackingBias, 0.0, "above the load")
	assert.Contains(t, lagging.Reason, "lagging")
	assert.Contains(t, lagging.Reason, "above")
	assert.True(t, following.Chosen)
	assert.InDelta(t, 0, following.TrackingWAPE, 1e-9)
}

func TestTheLagIsJudgedOnEnoughStepsAndOnlyWhenAnotherAlgorithmFollows(t *testing.T) {
	f := scoredFixture(t, "naive", "seasonal_naive")
	ctx := context.Background()
	_, err := f.svc.ScoreModule(ctx, f.module.ID)
	require.NoError(t, err)

	// two steps are noise: nothing is dropped
	f.forecastsOf(t, "cpu", "seasonal_naive", 2, func(a float64) float64 { return a*10 + 1 })
	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "seasonal_naive", detail.Algorithm)
	assert.False(t, detail.Selection[0].Lagging)

	require.NoError(t, f.db.Where("model_version = ?", "lag").Delete(&model.Prediction{}).Error)
	// when every algorithm is off, there is nothing better to switch to: the score decides
	f.forecastsOf(t, "cpu", "seasonal_naive", 10, func(a float64) float64 { return a*10 + 1 })
	f.forecastsOf(t, "cpu", "naive", 10, func(a float64) float64 { return a*10 + 1 })
	_, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "seasonal_naive", detail.Algorithm)
	for _, row := range detail.Selection {
		assert.False(t, row.Lagging, row.Algorithm)
	}
}

func TestTrackingMeasuresTheMissAsAShareOfTheLoadAndItsDirection(t *testing.T) {
	assert.False(t, Tracking{Points: 2, WAPE: 9}.Lagging(), "too few steps")
	assert.False(t, Tracking{Points: 10, WAPE: 0.49}.Lagging())
	assert.True(t, Tracking{Points: 10, WAPE: 0.51}.Lagging())

	one := &instanceState{eligible: true, tracking: Tracking{Points: 10, WAPE: 2, Bias: -2}}
	two := &instanceState{eligible: true, tracking: Tracking{Points: 10, WAPE: 0.1}}
	assert.Equal(t, 1, dropLagging([]*instanceState{one, two}, ""))
	assert.True(t, one.lagging)
	assert.Contains(t, one.reason, "below", "an algorithm under the load says so")
	assert.False(t, two.lagging)
}

// The decision comes before the action: a recommendation older than the operator should act on is made again from the load as it
// is, and one that is recent is left alone. The scheduled task runs every few minutes; the scaler reads every 30 seconds.
func TestARecommendationOlderThanTheLimitIsMadeAgainBeforeTheOperatorReadsIt(t *testing.T) {
	f := scoredFixture(t, "naive")
	ctx := context.Background()
	_, err := f.svc.RunPipeline(ctx, f.module.ID)
	require.NoError(t, err)
	first, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, first)

	// 30 seconds later it is recent enough: the stored one is read
	*f.clock = f.clock.Add(30 * time.Second)
	rec, refreshed, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.False(t, refreshed)
	assert.Equal(t, first.Version, rec.Version)

	// three minutes later it is not: the platform decides again, and the operator is given the new version
	*f.clock = f.clock.Add(150 * time.Second)
	rec, refreshed, err = f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.True(t, refreshed)
	assert.Greater(t, rec.Version, first.Version)
	assert.False(t, rec.CreatedAt.Before(*f.clock), "made at the time of the request")

	// the next poll is within the limit of the new one
	_, refreshed, err = f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.False(t, refreshed)

	// 0 turns it off
	*f.clock = f.clock.Add(time.Hour)
	_, refreshed, err = f.svc.FreshRecommendation(ctx, f.module.ID, 0)
	require.NoError(t, err)
	assert.False(t, refreshed)
}

func TestTheRefreshLeavesAModuleOnItsOwnIntervalAndAnObserveModuleAlone(t *testing.T) {
	f := scoredFixture(t, "naive")
	ctx := context.Background()
	_, err := f.svc.RunPipeline(ctx, f.module.ID)
	require.NoError(t, err)
	first, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)

	// a module that runs every 30 minutes is not made again every minute because the scaler asks
	f.setModule(t, func(m *model.ForecastModule) { m.RunIntervalMinutes = 30 })
	*f.clock = f.clock.Add(5 * time.Minute)
	rec, refreshed, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.False(t, refreshed)
	assert.Equal(t, first.Version, rec.Version)

	f.setModule(t, func(m *model.ForecastModule) { m.RunIntervalMinutes = 0; m.Mode = model.ModeObserve })
	*f.clock = f.clock.Add(5 * time.Minute)
	_, refreshed, err = f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.False(t, refreshed, "an observe module makes no recommendation")
}

func TestManyRequestsAtOnceMakeTheRecommendationOnce(t *testing.T) {
	f := scoredFixture(t, "naive")
	ctx := context.Background()
	_, err := f.svc.RunPipeline(ctx, f.module.ID)
	require.NoError(t, err)
	first, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)
	*f.clock = f.clock.Add(5 * time.Minute)

	var wg sync.WaitGroup
	versions := make([]int, 6)
	for i := range versions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, _, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
			if err == nil && rec != nil {
				versions[i] = rec.Version
			}
		}(i)
	}
	wg.Wait()
	for _, v := range versions {
		assert.Equal(t, first.Version+1, v, "every request reads the one new version")
	}
}

func TestTheDecisionAgeIsASystemParameterWithADefault(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.Equal(t, DefaultDecisionMaxAge, f.svc.DecisionMaxAge(ctx), "nothing set: the default")
	f.svc.Config = nil
	assert.Equal(t, DefaultDecisionMaxAge, f.svc.DecisionMaxAge(ctx), "no config at all: the default")
}

// The operator hangs up after its own timeout. The refresh it asked for is not cut off with it: the request reads the stored
// recommendation, the refresh finishes on its own and stores the new one with the training records of every run (none is lost to a
// cancelled context), and the next poll reads it.
func TestARefreshTheOperatorStoppedWaitingForFinishesAndIsStored(t *testing.T) {
	f := scoredFixture(t, "naive")
	ctx := context.Background()
	_, err := f.svc.RunPipeline(ctx, f.module.ID)
	require.NoError(t, err)
	first, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)
	var trainingsBefore int64
	require.NoError(t, f.db.Model(&model.TrainingRun{}).Count(&trainingsBefore).Error)

	release := make(chan struct{})
	inner := f.querier.value
	var once sync.Once
	f.querier.value = func(query string, ts time.Time) float64 {
		once.Do(func() { <-release }) // the first query of the refresh is slow
		return inner(query, ts)
	}

	*f.clock = f.clock.Add(11 * time.Minute) // older than the 10 minutes forecasts are reused for: the refresh forecasts again, and is slow
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	rec, refreshed, err := f.svc.FreshRecommendation(short, f.module.ID, time.Minute)
	require.NoError(t, err, "the request that stopped waiting is not an error")
	assert.False(t, refreshed)
	assert.Equal(t, first.Version, rec.Version, "it reads the stored recommendation")

	close(release)
	require.Eventually(t, func() bool { _, busy := f.svc.flights.Load(f.module.ID); return !busy }, 30*time.Second, 20*time.Millisecond)

	latest, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Greater(t, latest.Version, first.Version, "the refresh finished and stored its recommendation")
	var trainingsAfter int64
	require.NoError(t, f.db.Model(&model.TrainingRun{}).Count(&trainingsAfter).Error)
	assert.Greater(t, trainingsAfter, trainingsBefore, "the runs of the refresh were recorded")

	_, refreshed, err = f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.False(t, refreshed, "the next poll finds it recent")
}

// When the operator asks and the stored recommendation is old, the platform decides again on the load measured now, on the forecasts
// the scheduled task made a few minutes ago: the algorithms are not run a second time. The new samples are collected, and they move the
// decision (a load that jumped since the last run raises the replicas at once); only forecasts older than the parameter are made again.
func TestARefreshDecidesOnTheNewLoadWithoutRunningTheAlgorithmsAgain(t *testing.T) {
	f := aheadFixture(t)
	ctx := context.Background()
	f.fullCycle(t)
	count := func(table any) int64 {
		var n int64
		require.NoError(t, f.db.Model(table).Count(&n).Error)
		return n
	}
	predictions, trainings := count(&model.Prediction{}), count(&model.TrainingRun{})
	first, err := f.svc.Predictions.LatestRecommendation(ctx, f.module.ID)
	require.NoError(t, err)

	// five minutes later the load has jumped; the scheduled task has not run since
	*f.clock = f.clock.Add(5 * time.Minute)
	f.querier.value = func(query string, ts time.Time) float64 {
		if contains(query, "kube_deployment") {
			return 4
		}
		return 1500
	}
	queriesBefore := len(f.querier.queries)
	rec, refreshed, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.True(t, refreshed)
	assert.Greater(t, rec.Version, first.Version)
	assert.Greater(t, len(f.querier.queries), queriesBefore, "the newest samples were collected")
	assert.Equal(t, predictions, count(&model.Prediction{}), "no algorithm forecast again")
	assert.Equal(t, trainings, count(&model.TrainingRun{}), "and none left a record of running")
	assert.Equal(t, 15, rec.Replicas, "the load measured now needs 15 Pods: the decision followed it")
	assert.True(t, rec.Urgent)

	// the forecasts are older than the parameter: they are made again, as part of the refresh
	*f.clock = f.clock.Add(30 * time.Minute)
	rec2, refreshed, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.True(t, refreshed)
	assert.Greater(t, rec2.Version, rec.Version)
	assert.Greater(t, count(&model.Prediction{}), predictions, "forecasts that old are made again")
	assert.Greater(t, count(&model.TrainingRun{}), trainings)
}

// The age forecasts may have is the system parameter: a short one makes the same refresh forecast again.
func TestTheForecastReuseAgeIsTheSystemParameter(t *testing.T) {
	f := aheadFixture(t)
	ctx := context.Background()
	f.fullCycle(t)
	predictions := func() int64 {
		var n int64
		require.NoError(t, f.db.Model(&model.Prediction{}).Count(&n).Error)
		return n
	}
	before := predictions()
	f.setParameter(t, "forecast.decision_forecast_max_age_seconds", "60") // the lowest allowed
	*f.clock = f.clock.Add(5 * time.Minute)
	_, refreshed, err := f.svc.FreshRecommendation(ctx, f.module.ID, time.Minute)
	require.NoError(t, err)
	assert.True(t, refreshed)
	assert.Greater(t, predictions(), before, "five minutes is older than the 60 seconds asked for: forecast again")
}
