package forecastsvc

import (
	"encoding/json"

	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/backtest"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type windowCall struct {
	model   string
	origins []int
}

// The same time of the previous day needs a day of history, so it can only be scored on the later windows. The candidate is
// then scored again on exactly those windows: a verdict never compares two models on two different stretches of data.
func TestACandidateAndItsBaselineAreScoredOnTheSameWindows(t *testing.T) {
	svc := &Service{}
	bm, err := bind(&model.AlgorithmRun{Name: "ewma", AlgorithmID: "ewma", Params: `{"alpha":0.5}`})
	require.NoError(t, err)

	for _, tc := range []struct {
		name      string
		n         int
		wantDaily bool
	}{
		{"a day and more of history", 2000, true},
		{"just over a day", 1500, true},
		{"less than a day: the daily baseline cannot be scored", 1450, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
			data := forecast.Input{Values: make([]float64, tc.n), Step: time.Minute, End: end}
			shape := backtest.Config{HorizonSteps: 15, Cutoffs: 6, Spread: true, SeasonSteps: 1440}
			ownOrigins := backtest.Origins(tc.n, shape, bm.model.MinSamples())
			var calls []windowCall
			run := func(m forecast.Model, _ bool, _ float64, origins []int) (backtest.Result, error) {
				calls = append(calls, windowCall{m.ID(), origins})
				return backtest.Result{Metrics: backtest.Metrics{Points: len(origins) * 15, Cutoffs: len(origins), WAPE: 0.1, RiskWAPE: 0.1, UnderWeight: 3}}, nil
			}
			own, err := run(bm.model, true, 0.5, nil)
			require.NoError(t, err)
			own.Cutoffs, own.Points = len(ownOrigins), len(ownOrigins)*15
			calls = nil

			got := svc.compareWithBaselines(context.Background(), bm, time.Minute, data, data, shape, 0.5, own, backtest.Policy{}, run)
			require.NoError(t, got.err)

			var dailyOrigins, ewmaReruns []int
			for _, c := range calls {
				switch {
				case c.model == "seasonal_naive":
					dailyOrigins = c.origins
				case c.model == "ewma":
					ewmaReruns = c.origins
				}
			}
			if !tc.wantDaily {
				assert.Empty(t, dailyOrigins, "a baseline that would leave too few windows is not used")
				assert.Equal(t, "naive", got.baselineID)
				return
			}
			require.NotEmpty(t, dailyOrigins)
			assert.Equal(t, dailyOrigins, ewmaReruns, "the candidate is scored again on the windows the daily baseline can be scored on")
			for _, c := range dailyOrigins {
				assert.GreaterOrEqual(t, c, 1440, "every shared window has a day of history before it")
			}
			assert.Equal(t, got.candidate.Points, got.baseline.Points, "the reported candidate and baseline cover the same points")
			assert.Equal(t, got.candidate.Cutoffs, got.baseline.Cutoffs)
		})
	}
}

// A quantile forecast is stored with its level and scored at that level: the pinball loss and the coverage of 0.9, not of the middle.
func TestAQuantileForecastIsStoredAndScoredAtItsLevel(t *testing.T) {
	bm, err := bind(&model.AlgorithmRun{Name: "ewma", AlgorithmID: "ewma", Params: `{"alpha":0.5,"quantile_level":0.9}`})
	require.NoError(t, err)
	assert.Equal(t, 900, quantilePermille(bm))
	point, err := bind(&model.AlgorithmRun{Name: "ewma", AlgorithmID: "ewma", Params: `{"alpha":0.5}`})
	require.NoError(t, err)
	assert.Equal(t, 500, quantilePermille(point), "the default is still the middle")
	assert.NotEqual(t, bm.version, point.version, "asking for a quantile is a different model version, so no promotion carries over")

	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5, "quantile_level": 0.9}, 0)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, outcomes)
	assert.Equal(t, StatusOK, outcomes[0].Status, outcomes[0].Reason)
	rows, _, err := f.svc.Predictions.ListPredictions(ctx, rdb.PredictionFilter{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "ewma"}, 1, 50)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Equal(t, 900, r.QuantilePermille)
	}
	runs, err := f.backtest(ctx, "cpu", 4)
	require.NoError(t, err)
	for _, r := range runs {
		if r.AlgorithmID == "ewma" {
			var m backtest.Result
			require.NoError(t, json.Unmarshal([]byte(r.Metrics), &m))
			assert.Empty(t, m.Skipped)
			assert.Equal(t, 0.9, m.Alpha, "scored at the level of the forecast")
		}
	}
}
