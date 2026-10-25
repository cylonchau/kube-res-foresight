package forecastsvc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

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
	assert.Empty(t, board, "a run with no actuals says nothing about accuracy")
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
	for _, want := range []string{"kube_resource_fcst_module_enabled", "kube_resource_fcst_module_bypass", "kube_resource_fcst_recommendation_value",
		"kube_resource_fcst_recommendation_source", "kube_resource_fcst_recommendation_valid", "kube_resource_fcst_series_latest_sample_age_seconds",
		"kube_resource_fcst_prediction_age_seconds", "kube_resource_fcst_backtest_promoted", "kube_resource_fcst_forecast_metrics_error"} {
		assert.Contains(t, names, want)
	}
	assert.Equal(t, 4, names["kube_resource_fcst_recommendation_source"], "one per resource: cpu, memory, replicas and gpu")

	expected := `# HELP kube_resource_fcst_forecast_metrics_error 1 when the last scrape of forecast state failed.
# TYPE kube_resource_fcst_forecast_metrics_error gauge
kube_resource_fcst_forecast_metrics_error 0
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected), "kube_resource_fcst_forecast_metrics_error"))
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
