package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) predictRows(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&model.Prediction{}).Count(&n).Error)
	return n
}

func (f *fixture) trainings(t *testing.T) []model.TrainingRun {
	t.Helper()
	var list []model.TrainingRun
	require.NoError(t, f.db.Order("id").Find(&list).Error)
	return list
}

func (f *fixture) setParam(t *testing.T, p timing.Param, value string) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&model.SysConfig{}))
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: p.Key, ConfigValue: value, ConfigName: p.Name}).Error)
}

// The input of a forecast is the history up to the step the clock is in: a call that comes while the grid has not moved is given what the last one was given.
func TestAForecastMadeFromTheSameInputsIsNotComputedOrStoredAgainAndItsRecordSaysWhichRunStandsForIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	_, err := f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)

	before := Reuse()
	first, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, StatusOK, first[0].Status, first[0].Reason)
	assert.False(t, first[0].Reused)
	rows := f.predictRows(t)
	require.Positive(t, rows)

	// the grid has not moved (the step is a minute): the same question has the same answer
	*f.clock = f.clock.Add(10 * time.Second)
	second, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, StatusOK, second[0].Status)
	assert.True(t, second[0].Reused)
	require.NotNil(t, second[0].ReusedFrom)
	assert.Equal(t, first[0].Points, second[0].Points)
	assert.Contains(t, second[0].Reason, "reused")
	assert.Equal(t, rows, f.predictRows(t), "no rows were written for a forecast that was not made again")

	runs := f.trainings(t)
	require.Len(t, runs, 2, "the record of every run is filed: the forecast still counts as current")
	assert.Nil(t, runs[0].ReusedFrom)
	require.NotNil(t, runs[1].ReusedFrom)
	assert.WithinDuration(t, runs[0].PredictionTime, *runs[1].ReusedFrom, time.Second)
	assert.Equal(t, runs[0].InputHash, runs[1].InputHash)
	assert.Equal(t, int64(0), runs[1].DurationMs)
	assert.Equal(t, model.TrainingOK, runs[1].Status)

	// a third stands on the first run, not on the second: the window is counted from the run whose rows it is
	*f.clock = f.clock.Add(10 * time.Second)
	third, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.True(t, third[0].Reused)
	assert.WithinDuration(t, runs[0].PredictionTime, *third[0].ReusedFrom, time.Second)

	after := Reuse()
	assert.EqualValues(t, 1, after.Computed-before.Computed)
	assert.EqualValues(t, 2, after.Reused-before.Reused)

	// the record of a reused run shows the rows of the run it stands for
	data, err := f.svc.TrainingData(ctx, runs[1].ID)
	require.NoError(t, err)
	assert.NotEmpty(t, data.Forecast)
}

func TestAForecastIsMadeAgainWhenTheHistoryOrTheParametersChanged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	_, err := f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	first, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.False(t, first[0].Reused)

	// the grid moved: the window has a new end
	*f.clock = f.clock.Add(2 * time.Minute)
	_, err = f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	moved, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, moved[0].Reused, "the history has a new end")
	same, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.True(t, same[0].Reused)

	// the same end, but one point of the history is not what it was (a hole that was filled again)
	require.NoError(t, f.db.Exec("UPDATE metric_sample SET value = value + 1 WHERE ts = (SELECT MAX(ts) - 600 FROM metric_sample)").Error)
	changed, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, changed[0].Reused, "a changed point is a different input")
	again, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.True(t, again[0].Reused)

	// other parameters are another model version
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 720}, 0)
	other, err := f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, other[0].Reused, "the parameters are part of what the forecast is made from")
}

func TestTheReuseWindowIsCountedFromTheRunWhoseRowsStandForItAndTheParameterCanSwitchItOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	runs, err := f.svc.runsOf(ctx, f.module)
	require.NoError(t, err)
	b := runs[0]
	bm, err := bind(b)
	require.NoError(t, err)
	t0 := f.clock.Add(-time.Hour)
	source := model.TrainingRun{ModuleID: b.ID, Metric: "cpu", AlgorithmID: b.Key(), Status: model.TrainingOK, ModelVersion: bm.version, InputHash: "h", Points: 15, PredictionTime: t0}
	require.NoError(t, f.db.Create(&source).Error)
	// a record that stands for it, made 10 minutes later
	later := t0.Add(10 * time.Minute)
	require.NoError(t, f.db.Create(&model.TrainingRun{ModuleID: b.ID, Metric: "cpu", AlgorithmID: b.Key(), Status: model.TrainingOK, ModelVersion: bm.version, InputHash: "h", Points: 15,
		PredictionTime: later, ReusedFrom: &t0}).Error)

	from, points, ok := f.svc.reusable(ctx, b, bm, "cpu", "h", t0.Add(29*time.Minute))
	assert.True(t, ok)
	assert.Equal(t, t0, from, "the rows are those of the first run")
	assert.Equal(t, 15, points)
	_, _, ok = f.svc.reusable(ctx, b, bm, "cpu", "h", t0.Add(31*time.Minute))
	assert.False(t, ok, "31 minutes after the run that made the forecast it is made again, though a record 21 minutes old stands for it")
	_, _, ok = f.svc.reusable(ctx, b, bm, "cpu", "other", t0.Add(time.Minute))
	assert.False(t, ok, "other inputs")
	_, _, ok = f.svc.reusable(ctx, b, bm, "memory", "h", t0.Add(time.Minute))
	assert.False(t, ok, "another source")
	_, _, ok = f.svc.reusable(ctx, b, bm, "cpu", "h", t0.Add(-time.Minute))
	assert.False(t, ok, "a clock that went back")

	f.setParam(t, timing.ForecastReuse, "0")
	_, _, ok = f.svc.reusable(ctx, b, bm, "cpu", "h", t0.Add(time.Minute))
	assert.False(t, ok, "0 minutes is no reuse")
}

func TestTheInputHashChangesWithAnythingTheForecastIsMadeFrom(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	runs, err := f.svc.runsOf(context.Background(), f.module)
	require.NoError(t, err)
	bm, err := bind(runs[0])
	require.NoError(t, err)
	in, _, err := f.svc.loadTarget(context.Background(), runs[0], "cpu")
	if err != nil { // nothing is collected yet: the hash does not need the service
		in.Values, in.End, in.Step = []float64{1, 2, 3}, *f.clock, time.Minute
	}
	base := inputHash(bm, &in, 15)
	assert.Equal(t, base, inputHash(bm, &in, 15), "the same inputs, the same hash")
	assert.NotEqual(t, base, inputHash(bm, &in, 30), "the horizon")
	moved := in
	moved.End = in.End.Add(time.Minute)
	assert.NotEqual(t, base, inputHash(bm, &moved, 15), "the end")
	edited := in
	edited.Values = append([]float64(nil), in.Values...)
	edited.Values[0]++
	assert.NotEqual(t, base, inputHash(bm, &edited, 15), "one value")
	withCover := in
	withCover.Covariates = map[string][]float64{"rps": {1, 2, 3}}
	assert.NotEqual(t, base, inputHash(bm, &withCover, 15), "a covariate")
}
