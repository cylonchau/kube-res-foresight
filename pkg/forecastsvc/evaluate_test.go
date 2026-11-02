package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScoreRunOnAKnownRun(t *testing.T) {
	// predictions 10,12,14 against actuals 10,10,20 and a missing fourth step
	s := ScoreRun([]float64{10, 12, 14, 99}, []float64{10, 10, 20, math.NaN()})
	assert.Equal(t, 3, s.Points)
	assert.InDelta(t, (0+2+6)/3.0, s.MAE, 1e-9)
	assert.InDelta(t, math.Sqrt((0+4+36)/3.0), s.RMSE, 1e-9)
	assert.InDelta(t, 8.0/40.0, s.WAPE, 1e-9)
	assert.InDelta(t, (0+2-6)/3.0, s.Bias, 1e-9, "negative bias: the run under-predicted on balance")
	assert.InDelta(t, (20.0-14.0)/20.0, s.PeakUnder, 1e-9)
	assert.InDelta(t, 2.0/3.0, s.Coverage, 1e-9, "10<=10 and 10<=12 are covered, 20<=14 is not")

	assert.Zero(t, ScoreRun([]float64{1, 2}, []float64{math.NaN(), math.NaN()}).Points)
	assert.Equal(t, 2.0, ScoreRun([]float64{2, 2}, []float64{0, 0}).WAPE, "all-zero actuals fall back to MAE")
}

func TestEvaluateScoresFinishedRunsOnceAndRanksByObservedAccuracy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	f.bind(t, "cpu", "naive", nil, 1)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 2)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	results, err := f.evaluate(ctx)
	require.NoError(t, err)
	require.Len(t, results, 1, "one result per source")
	for _, r := range results {
		assert.Zero(t, r.Evaluated, "the horizon has not been observed yet")
		assert.Equal(t, 3, r.Pending, "one run per algorithm")
	}

	*f.clock = testNow.Add(20 * time.Minute) // past the 15 minute horizon
	_, err = f.collect(ctx)
	require.NoError(t, err)
	results, err = f.evaluate(ctx)
	require.NoError(t, err)
	total := func(rs []EvaluateResult) (n int) {
		for _, r := range rs {
			n += r.Evaluated
		}
		return n
	}
	assert.Equal(t, 3, total(results))
	again, err := f.evaluate(ctx)
	require.NoError(t, err)
	assert.Zero(t, total(again), "a run is scored once")

	board, err := f.svc.Leaderboard(ctx, f.module.ID, "cpu", nil, nil)
	require.NoError(t, err)
	require.Len(t, board, 3)
	assert.Equal(t, "seasonal_naive", board[0].AlgorithmID, "an exact seasonal series is predicted perfectly by the seasonal baseline")
	assert.InDelta(t, 0, board[0].WAPE, 1e-9)
	assert.Equal(t, 1, board[0].Rank)
	byAlgo := map[string]LeaderboardEntry{}
	for _, e := range board {
		byAlgo[e.AlgorithmID] = e
		assert.Equal(t, 15, e.Points)
	}
	assert.True(t, byAlgo["naive"].Baseline)
	require.NotNil(t, byAlgo["ewma"].BeatsBaseline)
	assert.False(t, *byAlgo["ewma"].BeatsBaseline)
	assert.Nil(t, byAlgo["naive"].BeatsBaseline, "baselines are not compared with themselves")

	_, found, err := f.svc.Predictions.ListEvaluations(ctx, evalFilter(f.module.ID, "cpu", "naive"), 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, found)
}

func TestEvaluateRecordsRunsWithoutActualsSoTheyAreNotRetried(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	// the collector skips exactly the predicted window, then resumes later
	f.querier.skip = func(ts time.Time) bool { return ts.After(testNow) && ts.Before(testNow.Add(30*time.Minute)) }
	*f.clock = testNow.Add(time.Hour)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	results, err := f.evaluate(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, results[0].Evaluated)
	board, err := f.svc.Leaderboard(ctx, f.module.ID, "cpu", nil, nil)
	require.NoError(t, err)
	// a run with no actuals says nothing about accuracy: the algorithm is listed, unranked, with the reason
	require.Len(t, board, 1)
	assert.Equal(t, 0, board[0].Rank)
	assert.Equal(t, StatusNoResult, board[0].Status)
	assert.NotEmpty(t, board[0].Reason)
}

func TestForecastCollectorExportsBoundedLabelsAndFreshness(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	_, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	_, err = f.backtest(ctx, "cpu", 2)
	require.NoError(t, err)

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(NewCollector(f.svc)))
	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]int{}
	for _, mf := range families {
		names[mf.GetName()] = len(mf.GetMetric())
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.Contains(t, []string{"module", "instance", "mode", "resource", "source", "metric", "algorithm", "state", "datasource"}, l.GetName(), "no unbounded label")
			}
		}
	}
	for _, want := range []string{"kube_res_foresight_module_enabled", "kube_res_foresight_module_bypass", "kube_res_foresight_recommendation_value",
		"kube_res_foresight_recommendation_source", "kube_res_foresight_recommendation_valid", "kube_res_foresight_series_latest_sample_age_seconds",
		"kube_res_foresight_prediction_age_seconds", "kube_res_foresight_backtest_promoted", "kube_res_foresight_forecast_metrics_error"} {
		assert.Contains(t, names, want)
	}
	assert.Equal(t, 4, names["kube_res_foresight_recommendation_source"], "one per resource: cpu, memory, replicas and gpu")

	expected := `# HELP kube_res_foresight_forecast_metrics_error 1 when the last scrape of forecast state failed.
# TYPE kube_res_foresight_forecast_metrics_error gauge
kube_res_foresight_forecast_metrics_error 0
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected), "kube_res_foresight_forecast_metrics_error"))
	assert.NoError(t, f.svc.Ready(ctx))
}

func TestMetricScoresRankTheAlgorithmsOnTheEvaluationsOfTheRealPipeline(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	f.bind(t, "cpu", "naive", nil, 1)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 2)
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	*f.clock = testNow.Add(20 * time.Minute)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.evaluate(ctx)
	require.NoError(t, err)

	res, err := f.svc.MetricScores(ctx, f.module.ID, nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Algorithms, 3)
	assert.True(t, res.Comparable, "every algorithm was measured on both sources")
	assert.ElementsMatch(t, []string{"cpu", "rps"}, res.Sources)
	assert.Equal(t, "seasonal_naive", res.Best, "the series is exactly seasonal")
	assert.InDelta(t, 100, res.Algorithms[0].Score, 1e-6)
	assert.Equal(t, 1, res.Algorithms[0].Rank)
	require.Len(t, res.Metrics, 6, "three algorithms on two sources")
	for _, m := range res.Metrics {
		assert.True(t, m.InComposite)
		assert.InDelta(t, 1, m.Accuracy+m.WAPE, 1e-9)
		assert.InDelta(t, 0.5, m.Weight, 1e-9, "the two sources have the same weight")
		assert.Equal(t, 15, m.Points)
	}
	for _, a := range res.Algorithms {
		if !a.Baseline {
			require.NotNil(t, a.BeatsBaseline, a.AlgorithmID)
		}
	}

	// a window that ends before anything was evaluated has nothing to rank
	early := testNow.Add(-time.Hour)
	none, err := f.svc.MetricScores(ctx, f.module.ID, nil, &early)
	require.NoError(t, err)
	assert.Empty(t, none.Algorithms)
	assert.Empty(t, none.Best)
}

// Two algorithms with the same size of error: the one that runs high ranks above the one that runs low, because a forecast that is
// too low starves the workload. WAPE alone cannot tell them apart; the leaderboard ranks by RiskWAPE.
func TestLeaderboardRanksAnAlgorithmThatRunsLowBelowOneThatRunsHigh(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	*f.clock = testNow.Add(20 * time.Minute)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.evaluate(ctx)
	require.NoError(t, err)

	// the same absolute error, in opposite directions: naive 10 high, ewma 9 low (so ewma has the better WAPE)
	require.NoError(t, f.db.Model(&model.PredictionEvaluation{}).Where("algorithm_id = ?", "naive").
		Updates(map[string]any{"wape": 0.10, "mae": 10.0, "bias": 10.0}).Error)
	require.NoError(t, f.db.Model(&model.PredictionEvaluation{}).Where("algorithm_id = ?", "ewma").
		Updates(map[string]any{"wape": 0.09, "mae": 9.0, "bias": -9.0}).Error)

	board, err := f.svc.Leaderboard(ctx, f.module.ID, "cpu", nil, nil)
	require.NoError(t, err)
	require.Len(t, board, 2)
	byAlgo := map[string]LeaderboardEntry{}
	for _, e := range board {
		byAlgo[e.AlgorithmID] = e
	}
	assert.Less(t, byAlgo["ewma"].WAPE, byAlgo["naive"].WAPE, "ewma has the smaller plain error")
	assert.InDelta(t, 0.10, byAlgo["naive"].RiskWAPE, 1e-9, "an overestimate costs what it costs")
	assert.InDelta(t, 0.27, byAlgo["ewma"].RiskWAPE, 1e-9, "an underestimate costs three times as much")
	assert.Equal(t, "naive", board[0].AlgorithmID, "ranked by RiskWAPE")
	assert.Equal(t, 2, byAlgo["ewma"].Rank)
	require.NotNil(t, byAlgo["ewma"].BeatsBaseline)
	assert.False(t, *byAlgo["ewma"].BeatsBaseline, "it does not beat the baseline once running low is counted")
}

func TestCollectorExportsTheScalingReportsAndTheJobsOfTheLastDay(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.Job{}))
	now := f.svc.now()
	rid := func(s string) *string { return &s }
	for i, r := range []model.ScalingExecution{
		{Action: "ScaleUp", Result: "applied", DegradeState: "normal", RecordID: rid("a")},
		{Action: "ScaleUp", Result: "applied", DegradeState: "normal", RecordID: rid("b")},
		{Action: "ScaleDown", Result: "failed", DegradeState: "cached", RecordID: rid("c")},
	} {
		r.ModuleID, r.Namespace, r.Scaler, r.TargetKind, r.TargetName = f.module.ID, "ns", "s", "Deployment", "d"
		r.CreatedAt = now.Add(-time.Duration(30-i*10) * time.Minute)
		require.NoError(t, f.db.Create(&r).Error)
	}
	old := model.ScalingExecution{ModuleID: f.module.ID, Namespace: "ns", Scaler: "s", TargetKind: "Deployment", TargetName: "d", Action: "ScaleUp", Result: "applied", RecordID: rid("old")}
	old.CreatedAt = now.Add(-48 * time.Hour)
	require.NoError(t, f.db.Create(&old).Error)
	for _, j := range []model.Job{{JobType: "forecast.run", Status: "success"}, {JobType: "forecast.run", Status: "success"}, {JobType: "ai.run", Status: "failed"}} {
		j.IdempotencyKey = fmt.Sprintf("k%d", time.Now().UnixNano())
		time.Sleep(time.Microsecond)
		require.NoError(t, f.db.Create(&j).Error)
	}

	c := NewCollector(f.svc)
	c.Jobs = rdb.NewJobDAO(f.db)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	got := map[string]float64{}
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, l := range m.GetLabel() {
				key += "|" + l.GetValue()
			}
			got[key] = m.GetGauge().GetValue()
		}
	}
	mod := f.module.Name
	assert.Equal(t, 2.0, got["kube_res_foresight_scaling_actions_24h|ScaleUp|"+mod+"|applied"], "the report of two days ago is not counted")
	assert.Equal(t, 1.0, got["kube_res_foresight_scaling_actions_24h|ScaleDown|"+mod+"|failed"])
	assert.Equal(t, 1.0, got["kube_res_foresight_scaling_degrade_state|"+mod+"|cached"], "the state of the newest report")
	assert.Equal(t, 0.0, got["kube_res_foresight_scaling_degrade_state|"+mod+"|normal"])
	assert.Equal(t, 2.0, got["kube_res_foresight_jobs|success|forecast.run"])
	assert.Equal(t, 1.0, got["kube_res_foresight_jobs|failed|ai.run"])
}
