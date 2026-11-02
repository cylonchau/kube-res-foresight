package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func TestTheDatasetIsSampledAtTheStepOfTheModuleAndShapedBySource(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	ctx := context.Background()
	// the module samples every 5 minutes; the cpu source is averaged and doubled (a unit conversion), two decimals
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Updates(map[string]any{"step_seconds": 300, "min_points": 20}).Error)
	require.NoError(t, f.db.Model(&model.MetricSource{}).Where("metric_source.key = ?", "cpu").Updates(map[string]any{"scale": 2.0, "precision": 2, "aggregation": "avg"}).Error)

	results, err := f.collect(ctx)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Empty(t, results[0].Error)
	assert.Equal(t, 72*60/5+1, results[0].Stored, "one point per 5 minutes over the lookback, both ends included")

	series, err := f.svc.Samples.Series(ctx, f.module.ID, "cpu")
	require.NoError(t, err)
	assert.Equal(t, 300, series.StepSeconds)
	rows, err := f.svc.Samples.Range(ctx, series.ID, testNow.Add(-time.Hour), testNow)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Zero(t, r.TS%300, "every point sits on the 5-minute grid")
		assert.InDelta(t, 2*daily(1, 0.5, time.Unix(r.TS, 0).UTC()), r.Value, 0.5)
		assert.InDelta(t, float64(int(r.Value*100+0.5))/100, r.Value, 1e-9, "two decimals")
	}

	// the forecast runs on that grid: its steps are 5 minutes apart
	f.run(t, "naive")
	out, err := f.predict(ctx)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, StatusOK, out[0].Status, out[0].Reason)
	assert.Equal(t, 4, out[0].Points, "the lead is two 5-minute steps at least, the forecast reaches twice that: 20 minutes, four steps, though the horizon is 15")

	// a module asking for more points than the dataset has is not forecast
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("min_points", 100000).Error)
	out, err = f.predict(ctx)
	require.NoError(t, err)
	assert.Equal(t, StatusSkipped, out[0].Status)
	assert.Contains(t, out[0].Reason, "at least 100000")

	// changing how the values are made drops the stored points: the history is collected again, in the new shape
	require.NoError(t, f.db.Model(&model.MetricSource{}).Where("metric_source.key = ?", "cpu").Update("scale", 1.0).Error)
	results, err = f.collect(ctx)
	require.NoError(t, err)
	assert.Equal(t, 72*60/5+1, results[0].Stored, "the whole window is stored again")
}

func TestADatasetStepFinerThanTheQueryIsRefused(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	require.NoError(t, f.db.Model(&model.MetricMapping{}).Where("id = ?", f.cpuMap.ID).Update("step_seconds", 300).Error)
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("step_seconds", 60).Error)
	results, err := f.collect(context.Background())
	require.NoError(t, err)
	assert.Contains(t, results[0].Error, "finer")
}

func TestTheTrainingWindowKeepsTheNewestStepsAndChangesTheModelVersion(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)

	out, err := f.predict(ctx)
	require.NoError(t, err)
	require.Equal(t, StatusOK, out[0].Status, out[0].Reason)
	full := out[0].ModelVersion
	trained := func() int {
		var last model.TrainingRun
		require.NoError(t, f.db.Order("id DESC").First(&last).Error)
		return last.Samples
	}
	assert.Equal(t, 72*60+1, trained(), "without a window the whole history range is the input")

	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("window_steps", 120).Error)
	f.run(t, "naive")
	out, err = f.predict(ctx)
	require.NoError(t, err)
	require.Equal(t, StatusOK, out[0].Status, out[0].Reason)
	assert.Equal(t, 120, trained(), "the algorithm trains on the newest 120 steps")
	assert.NotEqual(t, full, out[0].ModelVersion, "another window is another model")

	// A window shorter than what an algorithm needs does not starve it: the window is the one the algorithm needs.
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 240}, 0)
	needs, err := f.svc.Requirements(ctx, f.module.ID)
	require.NoError(t, err)
	var seasonal *Requirement
	for i := range needs {
		if needs[i].AlgorithmID == "seasonal_naive" {
			seasonal = &needs[i]
		}
	}
	require.NotNil(t, seasonal)
	assert.True(t, seasonal.OK, seasonal.Reason)
	assert.Equal(t, 240, seasonal.AvailableSteps, "the window is lifted to the 240 steps the algorithm needs")
}

// The range and the window of a module are what the user wants to spend; they must never starve an enabled algorithm. A module
// with a one hour lookback and a window of 10 steps made every algorithm that needs more than 10 steps "insufficient history"
// until someone edited the module by hand.
func TestTheLookbackAndTheWindowNeverStarveAnAlgorithm(t *testing.T) {
	step := time.Minute
	// holt_winters needs 60 steps: with the room kept over it the range is 2 hours, not 1
	assert.Equal(t, 2, lookbackFor(1, 60, step))
	assert.Equal(t, 3, lookbackFor(3, 60, step), "a range that is long enough is left alone")
	assert.Equal(t, 1, lookbackFor(1, 0, step), "an instance with no algorithm keeps the module's lookback")
	assert.Equal(t, 1, lookbackFor(1, 60*24*60, step), "what needs more than the limit is not grown without end")
	assert.Equal(t, 60, windowFor(10, 60), "a window shorter than the need is the need")
	assert.Equal(t, 0, windowFor(0, 60), "no window stays the whole range")
	assert.Equal(t, 200, windowFor(200, 60), "a long enough window is left alone")
}

func TestAWindowShorterThanTheMinimumDataIsRefused(t *testing.T) {
	m := &model.ForecastModule{PredictEnabled: true, Name: "w", Namespace: "n", Workload: "w", MinPoints: 100, WindowSteps: 50}
	assert.ErrorContains(t, m.Validate(), "window_steps")
	m.WindowSteps = 100
	assert.NoError(t, m.Validate())
}

// The cured case end to end: a module with a one hour lookback and a window of 10 steps, and an algorithm that needs 24 steps
// of 5 minutes. It used to be skipped with "needs 24 samples, got 10"; now the range collected and the window it trains on are
// the ones the algorithm needs.
func TestAnAlgorithmIsNotSkippedForAShortLookbackOrWindow(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 24}, 0)
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).
		Updates(map[string]any{"lookback_hours": 1, "window_steps": 10}).Error)
	ctx := context.Background()
	results, err := f.collect(ctx)
	require.NoError(t, err)
	require.Empty(t, results[0].Error)
	assert.Greater(t, results[0].Stored, 12, "more than the one hour of the module is collected: the algorithm needs 24 steps of 5 minutes")

	out, err := f.predict(ctx)
	require.NoError(t, err)
	require.Equal(t, StatusOK, out[0].Status, out[0].Reason)
	var last model.TrainingRun
	require.NoError(t, f.db.Order("id DESC").First(&last).Error)
	assert.GreaterOrEqual(t, last.Samples, 24, "it trained on what it needs, not on the 10 steps of the window")
}

// The setting of an algorithm that is no longer in the catalogue (macd_trend was retired) is not run, so it is not reported as
// failing on every cycle while the upgrade that deletes it has not run yet.
func TestTheSettingOfARetiredAlgorithmIsNotRun(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	require.NoError(t, f.db.Create(&model.AlgorithmConfig{AlgorithmID: "macd_trend", Enabled: true}).Error)
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	out, err := f.predict(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, out)
	for _, o := range out {
		assert.NotEqual(t, "macd_trend", o.AlgorithmID)
	}
}
