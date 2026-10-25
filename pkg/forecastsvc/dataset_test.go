package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
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
	assert.Equal(t, 3, out[0].Points, "a 15 minute horizon is three 5-minute steps")

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

	// a window shorter than what an algorithm needs is reported by the requirements
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
	assert.False(t, seasonal.OK)
	assert.Equal(t, 120, seasonal.AvailableSteps)
	assert.Contains(t, seasonal.Reason, "window_steps")
}

func TestAWindowShorterThanTheMinimumDataIsRefused(t *testing.T) {
	m := &model.ForecastModule{Name: "w", Namespace: "n", Workload: "w", MinPoints: 100, WindowSteps: 50}
	assert.ErrorContains(t, m.Validate(), "window_steps")
	m.WindowSteps = 100
	assert.NoError(t, m.Validate())
}
