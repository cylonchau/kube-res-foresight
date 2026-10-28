package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- A6: the K analogues are K different events

// Windows that start one step apart look almost the same: without a rule the nearest K are one event and its shifted copies.
func TestKNNExclusionZoneKeepsTheChosenWindowsApart(t *testing.T) {
	sorted := []candidate{{100, 0.1}, {101, 0.2}, {99, 0.3}, {102, 0.4}, {140, 0.5}, {141, 0.6}, {200, 0.7}, {60, 0.8}}

	overlap := KNN{Window: 20, K: 3, AllowOverlap: true}.pick(sorted)
	assert.Equal(t, []int{100, 101, 99}, endsOf(overlap), "without the zone the best event and its shifted copies are the three")

	apart := KNN{Window: 20, K: 3}.pick(sorted)
	assert.Equal(t, []int{100, 140, 200}, endsOf(apart), "with the zone each of the three is another event")
	for i := range apart {
		for j := i + 1; j < len(apart); j++ {
			assert.GreaterOrEqual(t, int(math.Abs(float64(apart[i].end-apart[j].end))), 20)
		}
	}
}

func endsOf(c []candidate) []int {
	out := make([]int, len(c))
	for i := range c {
		out[i] = c[i].end
	}
	return out
}

// On a smooth load the nearest windows of the most similar cycle are its own shifted copies. With the zone the analogues of a
// forecast come from different cycles; without it they all come from one.
func TestKNNAnaloguesOnASmoothLoadComeFromDifferentCycles(t *testing.T) {
	const period, window = 200, 20
	// the amplitude grows with every cycle, so the nearest cycle to the current one is the last, and its neighbours in time
	// are nearer than the same phase of the cycle before
	in := series(period*5+37, func(i int) float64 {
		amp := 10 + float64(i/period)*6
		return 100 + amp*math.Sin(2*math.Pi*float64(i%period)/period)
	})
	horizon := 5

	spread, err := KNN{Window: window, K: 3, Normalize: "none"}.analogues(context.Background(), in, horizon)
	require.NoError(t, err)
	ends := endsOf(spread)
	for i := range ends {
		for j := i + 1; j < len(ends); j++ {
			assert.GreaterOrEqual(t, int(math.Abs(float64(ends[i]-ends[j]))), window, "the zone: %v", ends)
		}
	}

	crowded, err := KNN{Window: window, K: 3, Normalize: "none", AllowOverlap: true}.analogues(context.Background(), in, horizon)
	require.NoError(t, err)
	e := endsOf(crowded)
	assert.Less(t, maxInt(e)-minInt(e), window, "without the zone the three are copies of one event: %v", e)
}

func maxInt(v []int) int {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

func minInt(v []int) int {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

// K windows that do not overlap need K windows of history: what the algorithm asks for follows from the zone.
func TestKNNHistoryNeedFollowsTheExclusionZone(t *testing.T) {
	m := KNN{Window: 48, K: 5}
	assert.Equal(t, 5*48+1, m.MinSamples())
	assert.Equal(t, 5*48+10, MinSamplesFor(m, 10))
	m.AllowOverlap = true
	assert.Equal(t, 48+5+1, m.MinSamples(), "before the zone: a window and K more steps")
	assert.Equal(t, 48+5+10-1, MinSamplesFor(m, 10))

	// a history too short for K separate windows is skipped, not padded with copies
	_, err := KNN{Window: 20, K: 5}.Forecast(context.Background(), series(90, func(i int) float64 { return float64(i % 17) }), 3)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
}

// The time of day constraint keeps only windows that end when the current one does (UTC, around midnight too).
func TestKNNSameTimeOfDayKeepsOnlyWindowsThatEndAtThatTime(t *testing.T) {
	// one minute steps for three days, a daily pattern
	in := series(3*1440, func(i int) float64 { return 50 + 20*math.Sin(2*math.Pi*float64(i%1440)/1440) })
	query := in.TimeAt(len(in.Values) - 1)
	m := KNN{Window: 30, K: 2, Normalize: "none", SameTimeOfDay: true, TimeToleranceMinutes: 15}
	got, err := m.analogues(context.Background(), in, 10)
	require.NoError(t, err)
	for _, c := range got {
		assert.True(t, sameTimeOfDay(in.TimeAt(c.end), query, 15), "window ending at %s", in.TimeAt(c.end))
	}

	assert.True(t, sameTimeOfDay(time.Date(2026, 1, 1, 23, 50, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 5, 0, 0, time.UTC), 15), "around midnight")
	assert.False(t, sameTimeOfDay(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC), 15))
	_, err = KNN{Window: 5, K: 1, TimeToleranceMinutes: 900}.Forecast(context.Background(), in, 1)
	assert.Error(t, err, "a tolerance above half a day is refused")
}

// The registry builds the model with the new parameters, and a stored set without them keeps its canonical form.
func TestKNNParametersAreBuiltFromTheRegistry(t *testing.T) {
	m, err := Build("knn", map[string]any{"window": 24, "k": 4})
	require.NoError(t, err)
	k := m.(KNN)
	assert.False(t, k.AllowOverlap, "the zone is on by default")
	assert.False(t, k.SameTimeOfDay)
	normalized, err := Normalize("knn", map[string]any{"window": 24, "k": 4})
	require.NoError(t, err)
	assert.NotContains(t, Canonical("knn", normalized), "exclusion", "a default new parameter is not stored, so the stored sets stay valid")

	m, err = Build("knn", map[string]any{"window": 24, "k": 4, "exclusion": "none", "same_time_of_day": true, "time_tolerance_minutes": 30})
	require.NoError(t, err)
	k = m.(KNN)
	assert.True(t, k.AllowOverlap)
	assert.True(t, k.SameTimeOfDay)
	assert.Equal(t, 30, k.TimeToleranceMinutes)

	d, _ := Lookup("knn")
	assert.Equal(t, "2", d.ImplVersion, "the zone changes the output, so the promotions of version 1 expire")
}

// ---- A7: MSTL

// A load with a daily cycle and a different level on the weekend: one hour steps, a day is 24 steps, a week 168. Weekdays have
// a morning peak, the weekend is lower and flat.
func weeklyLoad(i int) float64 {
	day := (i / 24) % 7
	hour := i % 24
	level := 100.0
	if day >= 5 {
		level = 60
	}
	return level + 30*math.Sin(2*math.Pi*float64(hour-6)/24)
}

func hourly(n int, f func(i int) float64) Input {
	v := make([]float64, n)
	for i := range v {
		v[i] = f(i)
	}
	return Input{Values: v, Step: time.Hour, End: t0.Add(time.Duration(n-1) * time.Hour)}
}

func meanAbsError(got, want []float64) float64 {
	var e float64
	for i := range got {
		e += math.Abs(got[i] - want[i])
	}
	return e / float64(len(got))
}

// Over a weekend boundary an algorithm that knows only the day repeats the weekday, MSTL knows the weekend is another level.
func TestMSTLRestoresTheDailyAndTheWeeklyCycle(t *testing.T) {
	const weeks = 5
	// the history ends on a Friday evening, so the next 48 steps are the weekend start
	end := weeks*168 + 4*24 + 18
	history := hourly(end, weeklyLoad)
	want := make([]float64, 48)
	for h := range want {
		want[h] = weeklyLoad(end + h)
	}
	ctx := context.Background()

	got, err := MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, history, 48)
	require.NoError(t, err)
	require.Len(t, got, 48)
	mstl := meanAbsError(got, want)

	dailyOnly, err := SeasonalNaive{Season: 24}.Forecast(ctx, history, 48)
	require.NoError(t, err)
	assert.Less(t, mstl, 0.25*meanAbsError(dailyOnly, want), "a day-only repeat misses the weekend (mstl %.2f)", mstl)

	justDaily, err := MSTL{Daily: 24}.Forecast(ctx, history, 48)
	require.NoError(t, err)
	assert.Less(t, mstl, 0.5*meanAbsError(justDaily, want), "without the weekly season the weekend is not found either")

	assert.Less(t, mstl, 3.0, "on a noise free two-cycle signal it is nearly exact")
}

func TestMSTLRequirementsValidationAndDeterminism(t *testing.T) {
	assert.Equal(t, 2*168, MSTL{Daily: 24, Weekly: 168}.MinSamples(), "two cycles of the longest period")
	assert.Equal(t, 2*24, MSTL{Daily: 24}.MinSamples())

	ctx := context.Background()
	history := hourly(3*168, weeklyLoad)
	_, err := MSTL{Daily: 1}.Forecast(ctx, history, 5)
	assert.ErrorContains(t, err, "daily_season_steps")
	_, err = MSTL{Daily: 24, Weekly: 24}.Forecast(ctx, history, 5)
	assert.ErrorContains(t, err, "weekly_season_steps")
	_, err = MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, hourly(300, weeklyLoad), 5)
	assert.ErrorIs(t, err, ErrInsufficientHistory, "less than two weeks is skipped, never padded")

	a, err := MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, history, 24)
	require.NoError(t, err)
	b, err := MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, history, 24)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

// The forecast uses only the history it is given: a value appended after the origin does not change a forecast made at it.
func TestMSTLUsesNothingAfterTheForecastOrigin(t *testing.T) {
	ctx := context.Background()
	full := hourly(4*168, weeklyLoad)
	origin := Input{Values: full.Values[:3*168], Step: time.Hour, End: t0.Add(time.Duration(3*168-1) * time.Hour)}
	want, err := MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, origin, 24)
	require.NoError(t, err)

	changed := append([]float64(nil), full.Values...)
	for i := 3 * 168; i < len(changed); i++ {
		changed[i] = 1e6
	}
	again := Input{Values: changed[:3*168], Step: time.Hour, End: origin.End}
	got, err := MSTL{Daily: 24, Weekly: 168}.Forecast(ctx, again, 24)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestMSTLNeverForecastsBelowZero(t *testing.T) {
	// a series that falls to zero in the weekend trough: the seasonal part plus a trend must not go negative
	history := hourly(4*168, func(i int) float64 {
		v := weeklyLoad(i) - 55
		if v < 0 {
			return 0
		}
		return v
	})
	got, err := MSTL{Daily: 24, Weekly: 168}.Forecast(context.Background(), history, 72)
	require.NoError(t, err)
	for _, v := range got {
		assert.GreaterOrEqual(t, v, 0.0)
	}
}

func TestMSTLIsRegisteredWithItsParametersAndExplanation(t *testing.T) {
	d, ok := Lookup("mstl")
	require.True(t, ok)
	assert.Equal(t, "statistical", d.Category)
	assert.NotEmpty(t, d.Method)
	assert.Contains(t, d.Method, "S_日")

	_, err := Build("mstl", map[string]any{})
	assert.Error(t, err, "the daily season length is required, as the season length of seasonal_naive is")
	m, err := Build("mstl", map[string]any{"daily_season_steps": 288, "weekly_season_steps": 2016})
	require.NoError(t, err)
	assert.Equal(t, MSTL{Daily: 288, Weekly: 2016}, m)
}
