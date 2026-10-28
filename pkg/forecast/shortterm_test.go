package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var shortTermEnd = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// quiet is a long quiet history with a little noise, then rise: the last len(rise) values.
func quiet(n int, level float64, rise ...float64) Input {
	v := make([]float64, 0, n+len(rise))
	for i := 0; i < n; i++ {
		v = append(v, level+0.002*math.Sin(float64(i)))
	}
	v = append(v, rise...)
	return Input{Values: v, Step: time.Minute, End: shortTermEnd}
}

func percentile(t *testing.T) Model {
	t.Helper()
	m, err := Build("hist_percentile", map[string]any{})
	require.NoError(t, err)
	return m
}

// A burst the long-period forecast has not seen moves it up at once; a quiet history leaves it alone.
func TestAShortTermRiseLiftsTheForecastOfAnAlgorithmThatRemembersLong(t *testing.T) {
	inner := percentile(t)
	st := ShortTerm{Inner: inner, Cfg: DefaultShortTerm()}
	ctx := context.Background()

	steady := quiet(300, 0.02)
	raw, err := inner.Forecast(ctx, steady, 10)
	require.NoError(t, err)
	got, rep, err := st.ForecastReport(ctx, steady, 10)
	require.NoError(t, err)
	assert.Equal(t, raw, got, "nothing changed: the long-period forecast stands")
	assert.False(t, rep.Applied)
	assert.Contains(t, rep.Reason, "where the long-period forecast has it")

	// the load jumps from 0.02 to about 0.7 over the last five minutes
	burst := quiet(300, 0.02, 0.05, 0.3, 0.55, 0.7, 0.72)
	raw, err = inner.Forecast(ctx, burst, 10)
	require.NoError(t, err)
	got, rep, err = st.ForecastReport(ctx, burst, 10)
	require.NoError(t, err)
	require.True(t, rep.Applied, rep.Reason)
	assert.Greater(t, rep.Gap, 0.5)
	assert.Greater(t, got[0], raw[0]+0.5, "the first step already sees the burst")
	assert.Greater(t, got[0], 0.7)
	assert.LessOrEqual(t, got[0], 1.5*0.72+1e-9, "never above 1.5 times the highest recent value")
	// a ramp is followed along its slope for as many steps as were measured (five), then the lift fades
	for h := 6; h < len(got); h++ {
		assert.LessOrEqual(t, got[h]-raw[h], got[h-1]-raw[h-1]+1e-9, "the lift fades along the horizon")
	}

	// a step that is not still growing is added as it is and fades from the first step
	level := quiet(300, 0.02, 0.5, 0.5, 0.5, 0.5, 0.5)
	rawLevel, err := inner.Forecast(ctx, level, 10)
	require.NoError(t, err)
	gotLevel, repLevel, err := st.ForecastReport(ctx, level, 10)
	require.NoError(t, err)
	require.True(t, repLevel.Applied, repLevel.Reason)
	for h := 1; h < len(gotLevel); h++ {
		assert.LessOrEqual(t, gotLevel[h]-rawLevel[h], gotLevel[h-1]-rawLevel[h-1]+1e-9)
	}
	assert.Equal(t, inner.ID(), st.ID())
}

// A fall is not followed by default: a percentile or an envelope forecasts high on purpose. With a share it is followed that far.
func TestAShortTermFallIsFollowedOnlyByTheShareThatIsSet(t *testing.T) {
	ctx := context.Background()
	history := quiet(300, 0.8, 0.5, 0.3, 0.1, 0.02, 0.02)
	inner := percentile(t)
	raw, err := inner.Forecast(ctx, history, 6)
	require.NoError(t, err)

	got, rep, err := ShortTerm{Inner: inner, Cfg: DefaultShortTerm()}.ForecastReport(ctx, history, 6)
	require.NoError(t, err)
	assert.Equal(t, raw, got, "never lowered by default")
	assert.False(t, rep.Applied && rep.Gap > 0)

	cfg := DefaultShortTerm()
	cfg.DownShare = 0.5
	lowered, rep, err := ShortTerm{Inner: inner, Cfg: cfg}.ForecastReport(ctx, history, 6)
	require.NoError(t, err)
	require.True(t, rep.Applied)
	assert.Less(t, rep.Gap, 0.0)
	assert.Less(t, lowered[0], raw[0])
	assert.Greater(t, lowered[0], raw[0]+rep.Gap, "half of the gap, not all of it")
	for _, v := range lowered {
		assert.GreaterOrEqual(t, v, 0.0, "a load is not negative")
	}
}

// The correction uses nothing after the forecast origin, and does nothing without history to forecast the recent steps from.
func TestShortTermNeedsHistoryAndIsOffWhenSwitchedOff(t *testing.T) {
	ctx := context.Background()
	inner := percentile(t)
	short := quiet(3, 0.02, 0.5, 0.6) // too little before the recent steps
	st := ShortTerm{Inner: inner, Cfg: DefaultShortTerm()}
	raw, err := inner.Forecast(ctx, short, 4)
	require.NoError(t, err)
	got, rep, err := st.ForecastReport(ctx, short, 4)
	require.NoError(t, err)
	assert.Equal(t, raw, got)
	assert.False(t, rep.Applied)

	burst := quiet(300, 0.02, 0.05, 0.3, 0.55, 0.7, 0.72)
	off, rep, err := ShortTerm{Inner: inner, Cfg: ShortTermConfig{}}.ForecastReport(ctx, burst, 5)
	require.NoError(t, err)
	assert.False(t, rep.Applied)
	assert.Equal(t, "off", rep.Reason)
	want, _ := inner.Forecast(ctx, burst, 5)
	assert.Equal(t, want, off)

	// the future of the history changes nothing about the correction made at an earlier origin
	full := quiet(300, 0.02, 0.05, 0.3, 0.55, 0.7, 0.72, 0.9, 0.95)
	cutAt := cut(full, len(full.Values)-2)
	a, _ := st.Forecast(ctx, burst, 5)
	b, _ := st.Forecast(ctx, cutAt, 5)
	assert.Equal(t, a, b, "values after the origin are not read")
}

func TestOnlyTheAlgorithmsThatRememberLongGetTheCorrection(t *testing.T) {
	for _, id := range []string{"hist_percentile", "seasonal_profile", "seasonal_naive", "mstl", "holt_winters", "peak_envelope"} {
		def, ok := Lookup(id)
		require.True(t, ok, id)
		assert.True(t, LongMemory(def), id)
	}
	for _, id := range []string{"naive", "ewma", "theta", "knn", "ridge_ar", "gbdt", "random_forest", "combination"} {
		def, ok := Lookup(id)
		require.True(t, ok, id)
		assert.False(t, LongMemory(def), id+" follows a change on its own")
	}
	assert.True(t, LongMemory(Definition{ID: "peak_margin", Base: "peak_envelope"}), "a variant takes after its base")
}
