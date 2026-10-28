package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// series builds an input of n steps of one minute starting at t0 with values f(i).
func series(n int, f func(i int) float64) Input {
	values := make([]float64, n)
	for i := range values {
		values[i] = f(i)
	}
	step := time.Minute
	return Input{Values: values, Step: step, End: t0.Add(time.Duration(n-1) * step)}
}

func TestNaiveAndSeasonalNaiveExactValues(t *testing.T) {
	in := series(8, func(i int) float64 { return float64(i % 4) }) // 0 1 2 3 0 1 2 3
	got, err := Naive{}.Forecast(context.Background(), in, 3)
	require.NoError(t, err)
	assert.Equal(t, []float64{3, 3, 3}, got)

	got, err = SeasonalNaive{Season: 4}.Forecast(context.Background(), in, 6)
	require.NoError(t, err)
	assert.Equal(t, []float64{0, 1, 2, 3, 0, 1}, got)
}

func TestSeasonalNaiveNeedsAFullSeasonAndNeverPads(t *testing.T) {
	in := series(3, func(i int) float64 { return float64(i) })
	_, err := SeasonalNaive{Season: 4}.Forecast(context.Background(), in, 2)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = SeasonalNaive{Season: 1}.Forecast(context.Background(), series(5, func(int) float64 { return 1 }), 2)
	assert.Error(t, err)
}

func TestEWMAMatchesTheRecurrence(t *testing.T) {
	in := Input{Values: []float64{10, 20, 30}, Step: time.Minute, End: t0}
	got, err := EWMA{Alpha: 0.5}.Forecast(context.Background(), in, 2)
	require.NoError(t, err)
	// level: 10 -> 0.5*20+0.5*10=15 -> 0.5*30+0.5*15=22.5
	assert.Equal(t, []float64{22.5, 22.5}, got)
	_, err = EWMA{Alpha: 0}.Forecast(context.Background(), in, 1)
	assert.Error(t, err)
}

func TestModelsRejectNaNAndBadHorizon(t *testing.T) {
	in := series(10, func(i int) float64 { return float64(i) })
	in.Values[4] = math.NaN()
	_, err := Naive{}.Forecast(context.Background(), in, 2)
	assert.Error(t, err)
	_, err = Naive{}.Forecast(context.Background(), series(5, func(int) float64 { return 1 }), 0)
	assert.Error(t, err)
}

func TestHoltWintersFollowsTrendAndSeason(t *testing.T) {
	const season = 12
	f := func(i int) float64 {
		return 100 + 0.5*float64(i) + 10*math.Sin(2*math.Pi*float64(i%season)/season)
	}
	in := series(season*8, f)
	got, err := HoltWinters{Season: season, Trend: true}.Forecast(context.Background(), in, season)
	require.NoError(t, err)
	var maxErr float64
	for h := 0; h < season; h++ {
		maxErr = math.Max(maxErr, math.Abs(got[h]-f(len(in.Values)+h)))
	}
	assert.Less(t, maxErr, 2.0, "a noiseless trend+season series is forecast closely")

	// A non-seasonal Holt model extrapolates a pure linear trend.
	linear := series(40, func(i int) float64 { return 5 + 2*float64(i) })
	got, err = HoltWinters{Trend: true}.Forecast(context.Background(), linear, 3)
	require.NoError(t, err)
	for h := 0; h < 3; h++ {
		assert.InDelta(t, 5+2*float64(40+h), got[h], 1.0, "step %d", h)
	}
}

func TestHoltWintersNeedsTwoSeasons(t *testing.T) {
	_, err := HoltWinters{Season: 10, Trend: true}.Forecast(context.Background(), series(15, func(i int) float64 { return float64(i) }), 2)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = HoltWinters{Alpha: 2}.Forecast(context.Background(), series(15, func(i int) float64 { return float64(i) }), 2)
	assert.Error(t, err)
}

func TestRidgeARRecoversALinearAutoregression(t *testing.T) {
	// y[t] = 0.6*y[t-1] + 0.3*y[t-2] + 2 has an exact solution, so a tiny penalty must reproduce it.
	y := []float64{1, 2}
	for len(y) < 200 {
		n := len(y)
		y = append(y, 0.6*y[n-1]+0.3*y[n-2]+2)
	}
	in := Input{Values: y, Step: time.Minute, End: t0}
	got, err := RidgeAR{Lags: 2, L2Lambda: 1e-9}.Forecast(context.Background(), in, 3)
	require.NoError(t, err)
	want := append([]float64(nil), y...)
	for h := 0; h < 3; h++ {
		n := len(want)
		want = append(want, 0.6*want[n-1]+0.3*want[n-2]+2)
		assert.InDelta(t, want[n], got[h], 1e-3, "step %d", h)
	}
}

func TestRidgeARCalendarFeaturesLearnADailyShape(t *testing.T) {
	// Hourly data with a pure time-of-day pattern: lags alone cannot know the phase, calendar features can.
	const hours = 24 * 14
	f := func(i int) float64 { return 50 + 20*math.Sin(2*math.Pi*float64(i%24)/24) }
	values := make([]float64, hours)
	for i := range values {
		values[i] = f(i)
	}
	in := Input{Values: values, Step: time.Hour, End: t0.Add(time.Duration(hours-1) * time.Hour)}
	got, err := RidgeAR{Lags: 1, Calendar: true, L2Lambda: 1e-6}.Forecast(context.Background(), in, 24)
	require.NoError(t, err)
	var maxErr float64
	for h := 0; h < 24; h++ {
		maxErr = math.Max(maxErr, math.Abs(got[h]-f(hours+h)))
	}
	assert.Less(t, maxErr, 3.0)
}

func TestGBDTFitsAPeriodicSeriesDeterministically(t *testing.T) {
	const season = 24
	f := func(i int) float64 { return 100 + 30*math.Sin(2*math.Pi*float64(i%season)/season) }
	in := series(season*20, f)
	m := GBDT{Lags: season, Trees: 60, LearningRate: 0.2, MaxDepth: 3, MinLeaf: 3}
	first, err := m.Forecast(context.Background(), in, season)
	require.NoError(t, err)
	second, err := m.Forecast(context.Background(), in, season)
	require.NoError(t, err)
	assert.Equal(t, first, second, "no sampling, so the same history gives the same forecast")
	var maxErr float64
	for h := 0; h < season; h++ {
		maxErr = math.Max(maxErr, math.Abs(first[h]-f(len(in.Values)+h)))
	}
	assert.Less(t, maxErr, 12.0)
}

func TestGBDTQuantileObjectiveSitsAboveTheMedian(t *testing.T) {
	// Deterministic noise around a flat level: the 0.9 quantile forecast must lie above the squared-error one.
	in := series(400, func(i int) float64 { return 100 + float64((i*37)%21) - 10 })
	mean, err := GBDT{Lags: 3, Trees: 30, LearningRate: 0.2, MaxDepth: 2, MinLeaf: 5}.Forecast(context.Background(), in, 1)
	require.NoError(t, err)
	q90, err := GBDT{Lags: 3, Trees: 30, LearningRate: 0.2, MaxDepth: 2, MinLeaf: 5, Quantile: true, Alpha: 0.9}.Forecast(context.Background(), in, 1)
	require.NoError(t, err)
	assert.Greater(t, q90[0], mean[0])
}

func TestGBDTValidation(t *testing.T) {
	in := series(200, func(i int) float64 { return float64(i % 7) })
	for name, m := range map[string]GBDT{
		"no lags":      {Trees: 5, LearningRate: 0.1, MaxDepth: 2, MinLeaf: 2},
		"bad rate":     {Lags: 2, Trees: 5, LearningRate: 0, MaxDepth: 2, MinLeaf: 2},
		"bad alpha":    {Lags: 2, Trees: 5, LearningRate: 0.1, MaxDepth: 2, MinLeaf: 2, Quantile: true, Alpha: 1},
		"short series": {Lags: 150, Trees: 5, LearningRate: 0.1, MaxDepth: 2, MinLeaf: 20},
	} {
		_, err := m.Forecast(context.Background(), in, 2)
		assert.Error(t, err, name)
	}
}

func TestModelsHonourCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := series(500, func(i int) float64 { return float64(i % 11) })
	_, err := GBDT{Lags: 5, Trees: 50, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 5}.Forecast(ctx, in, 2)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRegistryNormalizeBuildAndRejects(t *testing.T) {
	m, err := Build("seasonal_naive", map[string]any{"season_length_steps": 1440.0})
	require.NoError(t, err)
	assert.Equal(t, 1440, m.MinSamples())

	m, err = Build("gbdt", map[string]any{"lags": 12.0, "objective": "quantile"})
	require.NoError(t, err)
	assert.Equal(t, "gbdt", m.ID())

	p, err := Normalize("ewma", nil)
	require.NoError(t, err)
	assert.Equal(t, 0.3, p["alpha"])

	for name, c := range map[string]struct {
		id     string
		params map[string]any
	}{
		"unknown algorithm": {"arima", nil},
		"unknown parameter": {"ewma", map[string]any{"alpha": 0.5, "beta": 1.0}},
		"missing required":  {"seasonal_naive", nil},
		"wrong type":        {"ewma", map[string]any{"alpha": "0.5"}},
		"below minimum":     {"seasonal_naive", map[string]any{"season_length_steps": 1.0}},
		"above maximum":     {"ewma", map[string]any{"alpha": 1.5}},
		"non integer":       {"ridge_ar", map[string]any{"lags": 2.5}},
		"bad enum":          {"gbdt", map[string]any{"lags": 3.0, "objective": "huber"}},
		"bool as number":    {"ridge_ar", map[string]any{"lags": 3.0, "calendar_features": 1.0}},
	} {
		_, err := Build(c.id, c.params)
		assert.Error(t, err, name)
	}
	for _, d := range Definitions() {
		_, ok := Lookup(d.ID)
		assert.True(t, ok)
		assert.NotEmpty(t, d.Description, d.ID)
	}
}

func ramp(n int, from, slope float64) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = from + slope*float64(i)
	}
	return v
}

func TestEWMAParametersSpanBiasCorrectionAndExclusivity(t *testing.T) {
	in := Input{Values: []float64{10, 10, 10, 10, 20}, End: time.Unix(1000, 0), Step: time.Minute}
	plain, err := EWMA{Alpha: 0.5}.Forecast(context.Background(), in, 1)
	require.NoError(t, err)
	spanned, err := EWMA{Span: 3}.Forecast(context.Background(), in, 1) // alpha = 2/(3+1) = 0.5
	require.NoError(t, err)
	assert.InDelta(t, plain[0], spanned[0], 1e-12, "span 3 is alpha 0.5")
	corrected, err := EWMA{Alpha: 0.5, BiasCorrection: true}.Forecast(context.Background(), in, 1)
	require.NoError(t, err)
	assert.NotEqual(t, plain[0], corrected[0], "bias correction weights the history differently")
	assert.InDelta(t, (20+0.5*10+0.25*10+0.125*10+0.0625*10)/(1+0.5+0.25+0.125+0.0625), corrected[0], 1e-12)

	_, err = Normalize("ewma", map[string]any{"alpha": 0.4, "span": 5})
	assert.Error(t, err, "alpha and span together are refused")
	_, err = Normalize("ewma", map[string]any{"span": 5})
	assert.NoError(t, err)
	// parameters added later stay out of the canonical form while at their default: existing promotions stay valid
	n, _ := Normalize("ewma", map[string]any{"alpha": 0.3})
	assert.Equal(t, map[string]any{"alpha": 0.3}, Canonical("ewma", n))
}

func TestCovariatesChangeTheForecastOnlyThroughPastValues(t *testing.T) {
	const n = 400
	// the target follows its covariate with a one step delay: knowing the covariate helps
	cov := make([]float64, n)
	target := make([]float64, n)
	for i := range cov {
		cov[i] = 10 + 5*math.Sin(float64(i)/7) + float64(i%5)
		if i > 0 {
			target[i] = 2*cov[i-1] + 3
		} else {
			target[i] = 3
		}
	}
	base := Input{Values: target, End: time.Unix(100000, 0), Step: time.Minute}
	with := base
	with.Covariates = map[string][]float64{"oom": cov}

	for _, id := range []string{"ridge_ar", "gbdt"} {
		params := map[string]any{"lags": 3, "covariate_lags": 2}
		if id == "gbdt" {
			params["n_estimators"], params["training_window_steps"] = 30, 0
		}
		m, err := Build(id, params)
		require.NoError(t, err)
		plain, err := m.Forecast(context.Background(), base, 3)
		require.NoError(t, err)
		used, err := m.Forecast(context.Background(), with, 3)
		require.NoError(t, err)
		assert.NotEqual(t, plain, used, "%s: the covariate changes the forecast", id)

		// altering a covariate value after the last target step cannot be seen: there is none to alter, but
		// changing only the newest covariate point changes the forecast through lag 1, never the future
		shifted := with
		shifted.Covariates = map[string][]float64{"oom": append(append([]float64(nil), cov[:n-1]...), cov[n-1]+100)}
		moved, err := m.Forecast(context.Background(), shifted, 3)
		require.NoError(t, err)
		assert.NotEqual(t, used, moved, "%s: the newest covariate value is a lag-1 feature", id)
	}

	ridge := RidgeAR{Lags: 3, CovLags: 2, L2Lambda: 0.01}
	wrong := with
	wrong.Covariates = map[string][]float64{"oom": cov[:10]}
	_, err := ridge.Forecast(context.Background(), wrong, 2)
	assert.Error(t, err, "a covariate of another length is refused")
	nan := with
	nan.Covariates = map[string][]float64{"oom": append(append([]float64(nil), cov[:n-1]...), math.NaN())}
	_, err = ridge.Forecast(context.Background(), nan, 2)
	assert.Error(t, err)

	// an algorithm without covariate support is declared as such
	for _, d := range Definitions() {
		reads := map[string]bool{"ridge_ar": true, "gbdt": true, "random_forest": true, "naive_bayes": true, "rnn": true, "gru": true, "lstm": true, "formula": true}
		assert.Equal(t, reads[d.ID], d.Covariates, d.ID)
	}
	// the same algorithm with CovLags 0 ignores covariates entirely
	off := RidgeAR{Lags: 3, L2Lambda: 0.01}
	a, err := off.Forecast(context.Background(), base, 3)
	require.NoError(t, err)
	b, err := off.Forecast(context.Background(), with, 3)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}
