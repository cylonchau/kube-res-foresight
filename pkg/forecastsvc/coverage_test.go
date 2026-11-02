package forecastsvc

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
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
	cover := func(horizon, interval, lead int) int {
		return (&model.AlgorithmRun{HorizonMinutes: horizon, RunIntervalMinutes: interval, LeadCoverMinutes: lead}).CoverMinutes()
	}
	assert.Equal(t, 12, cover(1, 0, 12), "a horizon shorter than what the lead needs is lengthened to it")
	assert.Equal(t, 15, cover(15, 0, 12), "a longer one is kept")
	assert.Equal(t, 30, cover(1, 30, 12), "and so is a longer run interval")
}

// A forecast that stops before the next run starts leaves the minutes in between without any prediction: that was the gap
// in the history chart of a module with a 1 minute horizon that is forecast every 5 minutes. And one that stops before the lead is over
// cannot see the load the replicas asked for now are for: the configured Pod time of 60 s and the decision wait of 90 s make a lead of
// 150 s, three one-minute steps; the forecast reaches twice that, six minutes.
func TestAShortHorizonIsStoredAsLongAsTheRunIntervalSoNoTimeIsLeftWithoutAForecast(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("horizon_minutes", 1).Error)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, outcomes)
	assert.Equal(t, StatusOK, outcomes[0].Status, outcomes[0].Reason)
	assert.Equal(t, 6, outcomes[0].Points, "six one-minute steps (twice the lead of three), not the one of the configured horizon")

	rows, _, err := f.svc.Predictions.ListPredictions(ctx, rdb.PredictionFilter{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "naive"}, 1, 50)
	require.NoError(t, err)
	assert.Len(t, rows, 6)
}
