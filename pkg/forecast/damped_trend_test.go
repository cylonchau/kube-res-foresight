package forecast

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dampedTrend(t *testing.T, raw map[string]any) Model {
	t.Helper()
	m, err := Build("damped_trend", raw)
	require.NoError(t, err)
	return m
}

func risingBy(n int, from, perStep float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = from + perStep*float64(i)
	}
	return out
}

func forecastOf(t *testing.T, m Model, values []float64, h int) []float64 {
	t.Helper()
	out, err := m.Forecast(context.Background(), Input{Values: values, End: monday, Step: time.Minute}, h)
	require.NoError(t, err)
	return out
}

// On a clean ramp the slope is the ramp's, and the forecast follows it for fewer and fewer steps: it bends towards level + slope*phi/(1-phi)
// instead of running away.
func TestDampedTrendBendsTowardsACeilingOnARamp(t *testing.T) {
	m := dampedTrend(t, map[string]any{"alpha": 1.0, "window_steps": 12, "damping": 0.8, "cap_ratio": 10.0})
	values := risingBy(40, 100, 1) // 100 .. 139, one a step
	out := forecastOf(t, m, values, 200)
	assert.InDelta(t, 139+0.8, out[0], 1e-9, "the first step: the level plus one damped slope")
	assert.InDelta(t, 139+1*(0.8+0.64), out[1], 1e-9)
	assert.Greater(t, out[1], out[0])
	ceiling := 139 + 1*0.8/(1-0.8)
	assert.InDelta(t, ceiling, out[199], 1e-6, "it converges to level + slope*phi/(1-phi), it does not run away")
	for i := 1; i < len(out); i++ {
		assert.GreaterOrEqual(t, out[i], out[i-1], "a rising slope never falls back")
	}
}

// One isolated spike or one sudden fall in the window does not move the slope: the median of the slopes between the points is where the rest of
// the window puts it.
func TestDampedTrendSlopeIsNotMovedByOneSpikeOrOneFall(t *testing.T) {
	m := dampedTrend(t, map[string]any{"alpha": 1.0, "window_steps": 15, "damping": 0.9, "cap_ratio": 10.0})
	clean := risingBy(40, 100, 0.5)
	withSpike := append([]float64(nil), clean...)
	withSpike[30] += 200 // a spike inside the window
	withFall := append([]float64(nil), clean...)
	withFall[28] -= 60 // a sudden fall inside the window
	base := forecastOf(t, m, clean, 10)
	for name, v := range map[string][]float64{"spike": withSpike, "fall": withFall} {
		got := forecastOf(t, m, v, 10)
		// the level follows the last value (alpha 1), which these leave alone; the slope must be the clean ramp's
		assert.InDelta(t, base[9]-base[0], got[9]-got[0], 0.2, name)
	}
}

// A falling slope is followed only down_share of the way: by default the forecast is flat, capacity is not given back by the forecast.
func TestDampedTrendDoesNotFollowAFallByDefault(t *testing.T) {
	falling := risingBy(40, 200, -1)
	flat := forecastOf(t, dampedTrend(t, map[string]any{"alpha": 1.0}), falling, 20)
	for _, v := range flat {
		assert.InDelta(t, falling[len(falling)-1], v, 1e-9, "down_share 0: flat at the level")
	}
	half := forecastOf(t, dampedTrend(t, map[string]any{"alpha": 1.0, "down_share": 0.5, "damping": 0.9}), falling, 20)
	assert.Less(t, half[19], falling[len(falling)-1], "a share of the fall is followed")
	assert.Greater(t, half[19], falling[len(falling)-1]-20*1, "but less than the whole of it")
}

// A steep short slope is not extrapolated far: the forecast stays under cap_ratio times the highest value of the window, and never below 0.
func TestDampedTrendIsCappedAndNeverNegative(t *testing.T) {
	steep := risingBy(30, 10, 10) // up to 300
	got := forecastOf(t, dampedTrend(t, map[string]any{"alpha": 1.0, "damping": 1.0, "cap_ratio": 1.2}), steep, 100)
	assert.InDelta(t, 1.2*300, got[99], 1e-9, "no damping, so the cap is what stops it")
	for _, v := range got {
		assert.LessOrEqual(t, v, 1.2*300+1e-9)
	}
	zero := forecastOf(t, dampedTrend(t, map[string]any{"alpha": 1.0, "down_share": 1.0, "damping": 1.0}), risingBy(30, 30, -1), 100)
	for _, v := range zero {
		assert.GreaterOrEqual(t, v, 0.0)
	}
}

func TestDampedTrendNeedsItsWindowAndValidParameters(t *testing.T) {
	m := dampedTrend(t, map[string]any{"window_steps": 20})
	assert.Equal(t, 20, m.MinSamples())
	_, err := m.Forecast(context.Background(), Input{Values: risingBy(10, 1, 1), End: monday, Step: time.Minute}, 5)
	assert.ErrorIs(t, err, ErrInsufficientHistory)

	for name, raw := range map[string]map[string]any{
		"window":  {"window_steps": 2},
		"damping": {"damping": 0.0},
		"cap":     {"cap_ratio": 0.5},
		"share":   {"down_share": 1.5},
		"alpha":   {"alpha": 0.0},
	} {
		_, err := Build("damped_trend", raw)
		assert.Error(t, err, name)
	}
	d, ok := Lookup("damped_trend")
	require.True(t, ok)
	assert.NotEmpty(t, d.Method)
	assert.False(t, d.Baseline, "a candidate: it has to beat the baselines in a backtest like the others")
}
