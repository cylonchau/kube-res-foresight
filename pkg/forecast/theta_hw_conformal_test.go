package forecast

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inputOf(values []float64) Input {
	return Input{Values: values, Step: time.Minute, End: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(values)-1) * time.Minute)}
}

func gen(n int, f func(t int) float64) []float64 {
	v := make([]float64, n)
	for t := range v {
		v[t] = f(t)
	}
	return v
}

// ---- Holt-Winters: multiplicative season, non-negative output, damping

// When the swing of the season grows with the level, a multiplicative model follows it and an additive one cannot.
func TestHoltWintersMultiplicativeSeasonFollowsALevelThatGrows(t *testing.T) {
	factors := []float64{0.5, 1, 1.5, 1}
	level := func(t int) float64 {
		if t < 40 {
			return 100
		}
		return 200
	}
	y := gen(80, func(t int) float64 { return level(t) * factors[t%4] })
	want := make([]float64, 8)
	for h := range want {
		want[h] = 200 * factors[(80+h)%4]
	}
	fit := func(m HoltWinters) float64 {
		out, err := m.Forecast(context.Background(), inputOf(y), 8)
		require.NoError(t, err)
		var e float64
		for h := range out {
			e += math.Abs(out[h] - want[h])
		}
		return e / 8
	}
	mult := fit(HoltWinters{Season: 4, Trend: false, Multiplicative: true, NonNegative: true})
	add := fit(HoltWinters{Season: 4, Trend: false, NonNegative: true})
	assert.Less(t, mult, add, "the proportional season is followed by the multiplicative model")
	assert.Less(t, mult, 15.0, "and it lands close to the real next season")
}

func TestHoltWintersMultiplicativeRefusesAHistoryThatReachesZero(t *testing.T) {
	y := gen(40, func(t int) float64 { return float64(t % 4) }) // touches zero every season
	_, err := HoltWinters{Season: 4, Multiplicative: true}.Forecast(context.Background(), inputOf(y), 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strictly positive")
	_, err = HoltWinters{Season: 4}.Forecast(context.Background(), inputOf(y), 4)
	assert.NoError(t, err, "the additive season takes it")
	_, err = HoltWinters{Multiplicative: true}.Forecast(context.Background(), inputOf(y), 4)
	require.Error(t, err, "a multiplicative model needs a season")
}

func TestHoltWintersNeverForecastsBelowZeroWhenAsked(t *testing.T) {
	y := gen(12, func(t int) float64 { return float64(12 - t) }) // falls to 1: the trend runs below zero
	free, err := HoltWinters{Trend: true}.Forecast(context.Background(), inputOf(y), 30)
	require.NoError(t, err)
	floored, err := HoltWinters{Trend: true, NonNegative: true}.Forecast(context.Background(), inputOf(y), 30)
	require.NoError(t, err)
	assert.Less(t, minOf(free), 0.0, "the trend extrapolates below zero on its own")
	assert.GreaterOrEqual(t, minOf(floored), 0.0)
}

func minOf(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		m = math.Min(m, x)
	}
	return m
}

func TestHoltWintersDampingFactorIsAParameter(t *testing.T) {
	y := gen(40, func(t int) float64 { return 10 + 2*float64(t) })
	last := func(phi float64) float64 {
		out, err := HoltWinters{Trend: true, Damped: true, DampingPhi: phi, NonNegative: true, Alpha: 0.5, Beta: 0.3}.Forecast(context.Background(), inputOf(y), 20)
		require.NoError(t, err)
		return out[19]
	}
	assert.Less(t, last(0.8), last(0.98), "a smaller factor flattens the trend sooner")
	_, err := HoltWinters{Trend: true, Damped: true, DampingPhi: 1.5}.Forecast(context.Background(), inputOf(y), 3)
	assert.Error(t, err)
}

func TestHoltWintersStartingTrendIsNotSetByTwoPoints(t *testing.T) {
	// a spike on the fourth point used to set the whole starting trend; the slope of the first ten points is far steadier
	y := gen(30, func(t int) float64 { return 100 })
	y[3] = 160
	st := HoltWinters{Trend: true}.init(y)
	assert.Less(t, math.Abs(st.trend), 10.0, "the old two-point start gave 20")
}

func TestHoltWintersNewParametersAreInvisibleUntilUsed(t *testing.T) {
	normalized, err := Normalize("holt_winters", map[string]any{"season_length_steps": 24, "trend": true})
	require.NoError(t, err)
	canonical := Canonical("holt_winters", normalized)
	assert.NotContains(t, canonical, "seasonal_mode")
	assert.NotContains(t, canonical, "damping_phi")
	assert.NotContains(t, canonical, "non_negative")
	assert.NotContains(t, canonical, "quantile_level", "the stored parameters of an existing setting are unchanged")

	_, err = Normalize("holt_winters", map[string]any{"seasonal_mode": "multiplicative"})
	require.Error(t, err, "multiplicative without a season is refused when the setting is saved")
	m, err := Build("holt_winters", mustNormalize(t, "holt_winters", map[string]any{"season_length_steps": 24, "seasonal_mode": "multiplicative"}))
	require.NoError(t, err)
	assert.True(t, m.(HoltWinters).Multiplicative)
	assert.True(t, m.(HoltWinters).NonNegative, "non-negative by default")
}

func mustNormalize(t *testing.T, id string, raw map[string]any) map[string]any {
	t.Helper()
	p, err := Normalize(id, raw)
	require.NoError(t, err)
	return p
}

// ---- Theta

func TestThetaOnAConstantSeriesIsTheConstant(t *testing.T) {
	out, err := Theta{}.Forecast(context.Background(), inputOf(gen(20, func(int) float64 { return 42 })), 5)
	require.NoError(t, err)
	for _, v := range out {
		assert.InDelta(t, 42, v, 1e-9)
	}
}

// For a straight line the smoothing coefficient that minimises the one-step error is 1, so the level is the last value and
// the forecast is that value plus half the slope per step ahead: level + slope/2 * h.
func TestThetaOnALineAddsHalfTheSlopePerStep(t *testing.T) {
	y := gen(30, func(t int) float64 { return 10 + 2*float64(t) })
	out, err := Theta{}.Forecast(context.Background(), inputOf(y), 5)
	require.NoError(t, err)
	last := y[len(y)-1]
	for h := 1; h <= 5; h++ {
		assert.InDelta(t, last+float64(h), out[h-1], 1e-9, "step %d", h)
	}
}

func TestThetaFormulaWithAGivenAlpha(t *testing.T) {
	y := []float64{10, 12, 11, 14, 13, 15, 16, 15}
	alpha := 0.4
	level, _ := sesLevel(y, alpha)
	slope := olsSlope(y)
	out, err := Theta{Alpha: alpha}.Forecast(context.Background(), inputOf(y), 3)
	require.NoError(t, err)
	for h := 1; h <= 3; h++ {
		want := level + slope/2*(float64(h-1)+1/alpha-math.Pow(1-alpha, float64(len(y)))/alpha)
		assert.InDelta(t, want, out[h-1], 1e-9)
	}
}

func TestThetaKeepsTheShapeOfASeason(t *testing.T) {
	factors := []float64{0.6, 1.0, 1.6, 0.8}
	y := gen(96, func(t int) float64 { return (100 + 0.2*float64(t)) * factors[t%4] })
	out, err := Theta{Season: 4}.Forecast(context.Background(), inputOf(y), 8)
	require.NoError(t, err)
	// the position of the largest factor is the largest of its season, and the ratio between positions follows the factors
	assert.Greater(t, out[2], out[1])
	assert.Greater(t, out[1], out[0])
	assert.InDelta(t, factors[2]/factors[0], out[2]/out[0], 0.15)
	assert.InDelta(t, factors[2]/factors[0], out[6]/out[4], 0.15)
}

func TestThetaIsNeverNegativeAndNeedsEnoughHistory(t *testing.T) {
	y := gen(12, func(t int) float64 { return float64(12 - t) })
	out, err := Theta{}.Forecast(context.Background(), inputOf(y), 40)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, minOf(out), 0.0)

	_, err = Theta{Season: 24}.Forecast(context.Background(), inputOf(y), 3)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientHistory))
	_, err = Theta{Alpha: 2}.Forecast(context.Background(), inputOf(y), 3)
	assert.Error(t, err)
}

func TestThetaIsRegisteredAndEnabledByTheCatalog(t *testing.T) {
	def, ok := Lookup("theta")
	require.True(t, ok)
	assert.Equal(t, "statistical", def.Category)
	m, err := Build("theta", mustNormalize(t, "theta", map[string]any{}))
	require.NoError(t, err)
	assert.Equal(t, 4, m.MinSamples())
}

// ---- conformal quantile

func noisy(seed int64, n int) []float64 {
	rng := rand.New(rand.NewSource(seed))
	return gen(n, func(t int) float64 { return 100 + 10*rng.NormFloat64() })
}

func TestConformalQuantileCoversAboutItsLevel(t *testing.T) {
	y := noisy(7, 3000)
	m := Conformal{Base: EWMA{Alpha: 0.2}, Level: 0.9, Windows: 24}
	horizon := 5
	var covered, total int
	for c := 400; c+horizon <= len(y); c += 12 {
		pred, err := m.Forecast(context.Background(), inputOf(y[:c]), horizon)
		require.NoError(t, err)
		for j := 0; j < horizon; j++ {
			total++
			if y[c+j] <= pred[j] {
				covered++
			}
		}
	}
	share := float64(covered) / float64(total)
	assert.InDelta(t, 0.9, share, 0.06, "the actual stays at or below the line about nine times in ten (%d of %d)", covered, total)

	plain, err := EWMA{Alpha: 0.2}.Forecast(context.Background(), inputOf(y[:2000]), horizon)
	require.NoError(t, err)
	upper, err := m.Forecast(context.Background(), inputOf(y[:2000]), horizon)
	require.NoError(t, err)
	for j := range plain {
		assert.Greater(t, upper[j], plain[j], "the 0.9 line sits above the middle")
	}
}

func TestConformalIsDeterministicAndOnlyReadsTheHistoryItIsGiven(t *testing.T) {
	y := noisy(3, 800)
	m := Conformal{Base: EWMA{Alpha: 0.3}, Level: 0.9}
	a, err := m.Forecast(context.Background(), inputOf(y), 6)
	require.NoError(t, err)
	b, err := m.Forecast(context.Background(), inputOf(y), 6)
	require.NoError(t, err)
	assert.Equal(t, a, b)

	// a window is cut from the history: nothing after its cut-off is passed to the base model
	in := inputOf(y)
	in.Covariates = map[string][]float64{"x": append([]float64(nil), y...)}
	part := cut(in, 500)
	assert.Len(t, part.Values, 500)
	assert.Len(t, part.Covariates["x"], 500)
	assert.True(t, part.End.Equal(in.TimeAt(499)))

	spy := &lengthSpy{}
	_, err = Conformal{Base: spy, Level: 0.9}.Forecast(context.Background(), in, 6)
	require.NoError(t, err)
	for _, n := range spy.lengths {
		assert.LessOrEqual(t, n, len(y))
	}
	assert.Equal(t, len(y), spy.lengths[len(spy.lengths)-1], "the last call is the real forecast on the whole history")
	assert.Greater(t, len(spy.lengths), 3, "and the earlier ones are the calibration windows")
}

type lengthSpy struct{ lengths []int }

func (*lengthSpy) ID() string      { return "spy" }
func (*lengthSpy) MinSamples() int { return 3 }
func (s *lengthSpy) Forecast(_ context.Context, in Input, h int) ([]float64, error) {
	s.lengths = append(s.lengths, len(in.Values))
	out := make([]float64, h)
	for i := range out {
		out[i] = in.Values[len(in.Values)-1]
	}
	return out, nil
}

func TestConformalLiftsAForecastThatRunsLow(t *testing.T) {
	y := gen(600, func(t int) float64 { return 50 + 0.5*float64(t) }) // a rising series: the last value always under-forecasts
	plain, err := Naive{}.Forecast(context.Background(), inputOf(y), 10)
	require.NoError(t, err)
	upper, err := Conformal{Base: Naive{}, Level: 0.9}.Forecast(context.Background(), inputOf(y), 10)
	require.NoError(t, err)
	for j := range plain {
		assert.Greater(t, upper[j], plain[j])
		assert.InDelta(t, 50+0.5*float64(600+j), upper[j], 0.6, "calibrated on its own errors it lands on the line")
	}
}

func TestConformalNeedsEnoughHistoryAndAValidLevel(t *testing.T) {
	_, err := Conformal{Base: EWMA{Alpha: 0.3}, Level: 0.9}.Forecast(context.Background(), inputOf(noisy(1, 30)), 12)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientHistory))
	for _, level := range []float64{0, 0.4, 1} {
		_, err = Conformal{Base: EWMA{Alpha: 0.3}, Level: level}.Forecast(context.Background(), inputOf(noisy(1, 500)), 4)
		assert.Error(t, err, "level %v", level)
	}
	m := Conformal{Base: KNN{Window: 6, K: 5, Normalize: "relative"}, Level: 0.9}
	assert.Equal(t, MinSamplesFor(KNN{Window: 6, K: 5}, 10)+3*10, MinSamplesFor(m, 10), "the calibration windows and the base horizon requirement add up")
}

func TestConformalConformalQuantileOrderStatistic(t *testing.T) {
	errs := []float64{5, 1, 3, 2, 4, 9, 7, 6, 8, 10} // K = 10
	assert.Equal(t, 10.0, conformalQuantile(errs, 0.95), "rank ceil(11*0.95)=11 is past the end: the largest")
	assert.Equal(t, 9.0, conformalQuantile(errs, 0.8), "rank ceil(11*0.8)=9")
	assert.Equal(t, 1.0, conformalQuantile(errs, 0.01))
}

func TestEveryNonBaselineAlgorithmCanForecastAQuantileAndBaselinesCannot(t *testing.T) {
	for _, id := range []string{"ewma", "holt_winters", "theta", "ridge_ar", "gbdt", "knn"} {
		def, ok := Lookup(id)
		require.True(t, ok, id)
		names := map[string]bool{}
		for _, p := range def.Params {
			names[p.Name] = true
		}
		assert.True(t, names["quantile_level"] && names["calibration_windows"], id)
	}
	for _, id := range []string{"naive", "seasonal_naive"} {
		def, _ := Lookup(id)
		for _, p := range def.Params {
			assert.NotEqual(t, "quantile_level", p.Name, "a baseline stays a baseline: %s", id)
		}
	}

	params := mustNormalize(t, "ewma", map[string]any{"quantile_level": 0.9})
	m, err := Build("ewma", params)
	require.NoError(t, err)
	c, ok := m.(Conformal)
	require.True(t, ok)
	level, on := c.QuantileLevel()
	assert.True(t, on)
	assert.Equal(t, 0.9, level)
	assert.Equal(t, "ewma", m.ID())

	plain, err := Build("ewma", mustNormalize(t, "ewma", map[string]any{}))
	require.NoError(t, err)
	_, isConformal := plain.(Conformal)
	assert.False(t, isConformal, "at the default the model is the point forecast, as before")

	_, err = Normalize("ewma", map[string]any{"quantile_level": 0.3})
	assert.Error(t, err, "a level at or below one half is not an upper line")
	normalized, err := Normalize("ewma", map[string]any{"quantile_level": 0.9})
	require.NoError(t, err)
	assert.Contains(t, Canonical("ewma", normalized), "quantile_level")
}
