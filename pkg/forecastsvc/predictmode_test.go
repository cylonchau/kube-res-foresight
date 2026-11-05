package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPredict changes the prediction settings of the fixture's module in the store.
func (f *fixture) setPredict(t *testing.T, enabled bool, mode, algorithm string) {
	t.Helper()
	f.module.PredictEnabled, f.module.PredictMode, f.module.PredictAlgorithm = enabled, mode, algorithm
	require.NoError(t, f.module.Validate())
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).
		Updates(map[string]any{"predict_enabled": enabled, "predict_mode": f.module.PredictMode, "predict_algorithm": f.module.PredictAlgorithm}).Error)
}

func algorithmsPredicted(outcomes []PredictOutcome) map[string]bool {
	out := map[string]bool{}
	for _, o := range outcomes {
		if o.Status == StatusOK {
			out[o.AlgorithmID] = true
		}
	}
	return out
}

// A module that does not predict still collects its history, runs no algorithm, and is told so; its recommendation is the fixed default
// configuration and says why.
func TestAModuleWithThePredictionOffOrInModeNoneRunsNoAlgorithmAndUsesTheDefaultConfiguration(t *testing.T) {
	for name, set := range map[string]func(*fixture, *testing.T){
		"switched off": func(f *fixture, t *testing.T) { f.setPredict(t, false, model.PredictDynamic, "") },
		"mode none":    func(f *fixture, t *testing.T) { f.setPredict(t, true, model.PredictNone, "") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.bind(t, "cpu", "naive", nil, 0)
			_, err := f.collect(ctx)
			require.NoError(t, err, "the history is collected whatever the prediction does")
			set(f, t)

			_, err = f.predict(ctx)
			require.ErrorIs(t, err, ErrPredictionOff)

			res, err := f.svc.RunData(ctx, f.module.ID, nil)
			require.NoError(t, err)
			assert.Empty(t, res.Predict)
			assert.Empty(t, res.Errors, "a module that is not asked to predict has nothing to report as an error: %v", res.Errors)

			rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
			require.NoError(t, err)
			assert.Equal(t, "fallback", rec.CPUSource, "the fixed default configuration of the module")
			assert.EqualValues(t, 500, rec.CPUMilli)
			assert.Empty(t, rec.Algorithm)
			require.NotNil(t, detail.Predict)
			assert.Equal(t, name == "switched off", !detail.Predict.Enabled)
			assert.Empty(t, detail.Degraded, "no forecast was expected, so the fallback is not a degradation")
		})
	}
}

// Fixed mode: the named algorithm decides, whatever its score and whether or not a backtest promoted it. The enabled algorithms still
// forecast and are scored beside it (so what it is worth can be read against them) but are never followed.
func TestAFixedModuleIsFollowedWhileTheOtherAlgorithmsStillForecast(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "ewma", nil, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)

	f.setPredict(t, true, model.PredictDynamic, "")
	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true, "ewma": true}, algorithmsPredicted(outcomes), "dynamic: every enabled algorithm")

	f.setPredict(t, true, model.PredictFixed, "ewma")
	outcomes, err = f.predict(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true, "ewma": true}, algorithmsPredicted(outcomes), "fixed: the others forecast too")

	// an algorithm the platform has switched off is still the module's own choice, and runs beside the enabled ones
	require.NoError(t, f.db.Model(&model.AlgorithmConfig{}).Where("algorithm_id = ?", "theta").Update("enabled", false).Error)
	f.setPredict(t, true, model.PredictFixed, "theta")
	outcomes, err = f.predict(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true, "ewma": true, "theta": true}, algorithmsPredicted(outcomes))

	// a minimum score does not keep the named algorithm out: it was never scored against others
	f.module.MinScore = 90
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("min_score", 90).Error)
	f.setPredict(t, true, model.PredictFixed, "naive")
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "naive", rec.Algorithm)
	assert.Equal(t, "algorithm", rec.CPUSource)
	require.NotNil(t, detail.Predict)
	assert.Equal(t, model.PredictFixed, detail.Predict.Mode)
	chosen := 0
	for _, row := range detail.Selection {
		if row.Chosen {
			chosen++
			assert.Equal(t, "naive", row.Algorithm)
		} else {
			assert.False(t, row.Eligible, "%s is drawn for comparison, it does not decide", row.Algorithm)
			assert.Contains(t, row.Reason, "the module is fixed to naive")
		}
	}
	assert.Equal(t, 1, chosen)
	assert.Greater(t, len(detail.Selection), 1, "the others are listed beside the named one")

	// the administrator's choice needs no promoting backtest: an algorithm that is not a baseline is followed all the same
	f.setPredict(t, true, model.PredictFixed, "ewma")
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "ewma", rec.Algorithm)
	assert.Equal(t, "algorithm", rec.CPUSource)
	assert.Empty(t, detail.Degraded)
}

// A fixed module does not fall back to the default the moment its algorithm has nothing usable: the last result is held while the
// algorithm's newest forecast is within FixedGraceFactor x the TTL, then the default applies.
func TestAFixedModuleHoldsTheLastResultOfItsAlgorithmForAWhile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	f.setPredict(t, true, model.PredictFixed, "naive")
	_, err = f.predict(ctx)
	require.NoError(t, err)
	first, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.Equal(t, "algorithm", first.CPUSource)

	// the forecast is now older than the TTL and its horizon is over: nothing usable, but within the grace
	*f.clock = f.clock.Add(20 * time.Minute)
	held, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "algorithm", held.CPUSource, "the last result is held, not the default")
	assert.Equal(t, first.CPUMilli, held.CPUMilli)
	assert.Equal(t, DegradeHeldLast, detail.Degraded["cpu"])
	assert.Contains(t, detail.DegradeNotes["cpu"], "held until")

	// past the grace the default configuration applies
	*f.clock = f.clock.Add(time.Hour)
	late, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fallback", late.CPUSource)
	assert.NotEqual(t, DegradeHeldLast, detail.Degraded["cpu"])
}

func TestTheFixedModeNeedsAnAlgorithmThatExists(t *testing.T) {
	f := newFixture(t)
	f.setPredict(t, true, model.PredictFixed, "no_such_algorithm")
	_, err := f.predict(context.Background())
	require.ErrorIs(t, err, ErrNotConfigured)

	m := &model.ForecastModule{Name: "x", Namespace: "n", Workload: "w", PredictEnabled: true, PredictMode: model.PredictFixed}
	assert.Error(t, m.Validate(), "fixed mode without an algorithm")
	m.PredictAlgorithm = "naive"
	assert.NoError(t, m.Validate())
	m.PredictMode = "sometimes"
	assert.Error(t, m.Validate())
}

// The OOM weight is the module's: off, an OOM kill enters no score; on, it takes the weight of the memory score.
func TestTheOOMWeightOfAModuleIsOffUnlessItIsSwitchedOn(t *testing.T) {
	m := &model.ForecastModule{Name: "x", Namespace: "n", Workload: "w"}
	require.NoError(t, m.Validate())
	assert.Zero(t, m.OOMScoreWeight(), "not configured: the kills are not weighed")
	m.OOMWeightEnabled = true
	assert.InDelta(t, model.DefaultOOMWeight, m.OOMScoreWeight(), 1e-9, "switched on with the default value")
	m.OOMWeight = 0.35
	assert.InDelta(t, 0.35, m.OOMScoreWeight(), 1e-9)
	m.OOMWeight = 1.5
	assert.Error(t, m.Validate())
}
