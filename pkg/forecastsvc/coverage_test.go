package forecastsvc

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAForecastCoversTheTimeUntilTheNextOneIsMade(t *testing.T) {
	run := func(horizon, interval int) int {
		return (&model.AlgorithmRun{HorizonMinutes: horizon, RunIntervalMinutes: interval}).CoverMinutes()
	}
	assert.Equal(t, 5, run(1, 0), "a 1 minute horizon is lengthened to the 5 minute cycle of the scheduled task")
	assert.Equal(t, 15, run(15, 0), "a longer horizon is kept")
	assert.Equal(t, 30, run(10, 30), "and so is the run interval of the module when that is longer")
	assert.Equal(t, 60, run(60, 15))
}

// A forecast that stops before the next run starts leaves the minutes in between without any prediction: that was the gap
// in the history chart of a module with a 1 minute horizon that is forecast every 5 minutes.
func TestAShortHorizonIsStoredAsLongAsTheRunIntervalSoNoTimeIsLeftWithoutAForecast(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	require.NoError(t, f.db.Model(&model.ForecastModule{PredictEnabled: true}).Where("id = ?", f.module.ID).Update("horizon_minutes", 1).Error)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, outcomes)
	assert.Equal(t, StatusOK, outcomes[0].Status, outcomes[0].Reason)
	assert.Equal(t, model.DefaultRunCycleMinutes, outcomes[0].Points, "five one-minute steps, not one")

	rows, _, err := f.svc.Predictions.ListPredictions(ctx, rdb.PredictionFilter{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "naive"}, 1, 50)
	require.NoError(t, err)
	assert.Len(t, rows, model.DefaultRunCycleMinutes)
}
