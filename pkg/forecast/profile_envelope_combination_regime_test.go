package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noise is a deterministic wobble in [-1, 1], so a series has noise without a random source.
func noise(i int) float64 { return math.Sin(float64(i)*12.9898) * math.Cos(float64(i)*0.31) }

// atStep is a series that starts at the given time, one step per element.
func atStep(start time.Time, step time.Duration, n int, f func(i int, at time.Time) float64) Input {
	v := make([]float64, n)
	for i := range v {
		v[i] = f(i, start.Add(time.Duration(i)*step))
	}
	return Input{Values: v, Step: step, End: start.Add(time.Duration(n-1) * step)}
}

// ---- seasonal_profile

// a Monday 00:00 UTC, so the days of the week are easy to read
var monday = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

// A weekday load that peaks at 09:00-10:00 and is quiet at weekends: 15 minute steps for four weeks.
func officeLoad(i int, at time.Time) float64 {
	v := 100.0
	if at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		return 40 + 2*noise(i)
	}
	if at.Hour() == 9 {
		v = 300
	}
	return v + 3*noise(i)
}

func TestSeasonalProfileForecastsThePeakAtThePeakHour(t *testing.T) {
	// four weeks ending on Sunday 23:45; the next hours are Monday 00:00 and on
	in := atStep(monday, 15*time.Minute, 28*96, officeLoad)
	m := SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: true, HalfLifeDays: 0, MinPerBucket: 6}
	got, err := m.Forecast(context.Background(), in, 12*4) // Monday 00:00 to 12:00
	require.NoError(t, err)
	assert.InDelta(t, 100, got[2*4], 8, "Monday 02:00 is quiet")
	assert.InDelta(t, 300, got[9*4+1], 8, "Monday 09:15 is the peak: a flat percentile or a copy of the last value would not know")
	assert.InDelta(t, 100, got[11*4], 8, "Monday 11:00 is back to normal")
}

func TestSeasonalProfileKnowsTheWeekend(t *testing.T) {
	// four weeks less the weekend of the last one: the history ends on Friday 23:45 and the next day is Saturday
	in := atStep(monday, 15*time.Minute, 4*7*96-2*96, officeLoad)
	require.Equal(t, time.Friday, in.End.Weekday())
	weekly := SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: true, MinPerBucket: 6}
	got, err := weekly.Forecast(context.Background(), in, 96) // all of Saturday
	require.NoError(t, err)
	assert.InDelta(t, 40, got[9*4+1], 8, "the weekend has no peak at nine")

	daily := SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: false, MinPerBucket: 6}
	flat, err := daily.Forecast(context.Background(), in, 96)
	require.NoError(t, err)
	assert.Greater(t, flat[9*4+1], 200.0, "the time of day alone cannot tell Saturday from a weekday")
}

// One odd day is one vote among many; seasonal_naive repeats it.
func TestSeasonalProfileIsNotCarriedAwayByOneOddDay(t *testing.T) {
	odd := func(i int, at time.Time) float64 {
		v := officeLoad(i, at)
		if at.Weekday() == time.Thursday && at.Day() == 26 { // the last Thursday: an incident
			v += 500
		}
		return v
	}
	in := atStep(monday, 15*time.Minute, 4*7*96-3*96, odd) // ends Thursday 23:45 of the fourth week
	require.Equal(t, time.Thursday, in.End.Weekday())
	profile, err := SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: true, MinPerBucket: 6}.Forecast(context.Background(), in, 4*10)
	require.NoError(t, err)
	assert.InDelta(t, 300, profile[9*4+1], 10, "Friday 09:15: the incident of Thursday does not move it")
	naive, err := SeasonalNaive{Season: 96}.Forecast(context.Background(), in, 4*10)
	require.NoError(t, err)
	assert.Greater(t, naive[9*4+1], 700.0, "the copy of yesterday repeats the incident as if it were the pattern")
}

func TestSeasonalProfileFallsBackWhenAThinBucketCannotBeTrusted(t *testing.T) {
	// three hours of one minute steps: no bucket has six samples of a weekday, the time of day has, and the answer is finite
	in := atStep(monday.Add(5*time.Hour), time.Minute, 180, func(i int, _ time.Time) float64 { return 100 + float64(i%10) })
	got, err := SeasonalProfile{Percentile: 95, BucketMinutes: 60, ByWeekday: true, MinPerBucket: 6}.Forecast(context.Background(), in, 30)
	require.NoError(t, err)
	for _, v := range got {
		assert.InDelta(t, 109, v, 1, "the percentile of what there is")
	}
	// a bucket with nothing in it at all (the hour after the history) falls back to the whole history
	_, err = SeasonalProfile{Percentile: 95, BucketMinutes: 15, ByWeekday: true, MinPerBucket: 100000}.Forecast(context.Background(), in, 30)
	require.NoError(t, err)
}

func TestSeasonalProfileHonoursTheTimeZoneAndTheDecay(t *testing.T) {
	// the load peaks at 09:00 local time, which is 01:00 UTC in UTC+8
	in := atStep(monday, 15*time.Minute, 14*96, func(i int, at time.Time) float64 {
		if at.Hour() == 1 {
			return 300
		}
		return 100
	})
	local := SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: false, MinPerBucket: 6, UTCOffsetMinutes: 480}
	got, err := local.Forecast(context.Background(), in, 96)
	require.NoError(t, err)
	assert.InDelta(t, 300, got[1*4+1], 1, "01:15 UTC")
	assert.InDelta(t, 100, got[9*4+1], 1, "09:15 UTC is quiet")

	// the level rose a week ago: with decay the percentile follows it, without it the old half still counts
	rose := atStep(monday, time.Hour, 14*24, func(i int, _ time.Time) float64 {
		if i < 7*24 {
			return 100
		}
		return 200
	})
	flat, err := SeasonalProfile{Percentile: 50, BucketMinutes: 60, ByWeekday: false, MinPerBucket: 1}.Forecast(context.Background(), rose, 1)
	require.NoError(t, err)
	decayed, err := SeasonalProfile{Percentile: 50, BucketMinutes: 60, ByWeekday: false, MinPerBucket: 1, HalfLifeDays: 1}.Forecast(context.Background(), rose, 1)
	require.NoError(t, err)
	assert.Equal(t, 100.0, flat[0], "without decay the median of two equal halves is the lower one")
	assert.Equal(t, 200.0, decayed[0])
}

func TestSeasonalProfileRefusesBadParametersAndIsRegistered(t *testing.T) {
	in := atStep(monday, time.Minute, 100, func(i int, _ time.Time) float64 { return 1 })
	ctx := context.Background()
	for _, bad := range []SeasonalProfile{
		{Percentile: 0, BucketMinutes: 60, MinPerBucket: 1},
		{Percentile: 90, BucketMinutes: 7, MinPerBucket: 1},
		{Percentile: 90, BucketMinutes: 60, MinPerBucket: 0},
		{Percentile: 90, BucketMinutes: 60, MinPerBucket: 1, UTCOffsetMinutes: 900},
		{Percentile: 90, BucketMinutes: 60, MinPerBucket: 1, HalfLifeDays: -1},
	} {
		_, err := bad.Forecast(ctx, in, 5)
		assert.Error(t, err, "%+v", bad)
	}
	_, err := Build("seasonal_profile", map[string]any{"bucket_minutes": 7})
	assert.Error(t, err, "a bucket that does not divide a day is refused by the registry")
	m, err := Build("seasonal_profile", nil)
	require.NoError(t, err)
	assert.Equal(t, SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: true, HalfLifeDays: 14, MinPerBucket: 6, UTCOffsetMinutes: 480}, m, "the clock of the buckets is the platform's, UTC+8")
}

// ---- peak_envelope

// A memory sawtooth: it grows 2 per step, drops to the floor every 20 steps, and the floor itself rises 0.5 per step.
func sawtooth(i int) float64 {
	return 100 + 0.5*float64(i) + 2*float64(i%20) + noise(i)
}

func TestPeakEnvelopeForecastsTheNextPeaksNotTheMean(t *testing.T) {
	in := atStep(monday, time.Minute, 600, func(i int, _ time.Time) float64 { return sawtooth(i) })
	m := PeakEnvelope{EnvelopeWindow: 25}
	got, err := m.Forecast(context.Background(), in, 40)
	require.NoError(t, err)
	// the true peak of the saw at step 600+h-1 is the floor there plus 38
	peak := func(i int) float64 { return 100 + 0.5*float64(i) + 38 }
	for _, h := range []int{1, 20, 40} {
		assert.InDelta(t, peak(600+h-1), got[h-1], 6, "step %d ahead", h)
	}
	mean := 0.0
	for i := 560; i < 600; i++ {
		mean += in.Values[i]
	}
	mean /= 40
	assert.Greater(t, got[0], mean+15, "a forecast of the mean would sit in the middle of the saw")
	for h := 1; h < len(got); h++ {
		assert.GreaterOrEqual(t, got[h], got[h-1]-1e-9, "the envelope does not fall")
	}
}

func TestPeakEnvelopeIsNotDraggedByARestartOrOddPeaks(t *testing.T) {
	values := make([]float64, 600)
	for i := range values {
		values[i] = sawtooth(i)
	}
	for i := 300; i < 310; i++ { // a restart: the process was gone for ten steps
		values[i] = 0
	}
	for _, i := range []int{100, 250, 420} { // peaks that were nothing to do with the leak
		values[i] += 300
	}
	in := Input{Values: values, Step: time.Minute, End: monday.Add(599 * time.Minute)}
	got, err := PeakEnvelope{EnvelopeWindow: 25}.Forecast(context.Background(), in, 10)
	require.NoError(t, err)
	assert.InDelta(t, 100+0.5*599+38, got[0], 20, "the median of the slopes is not moved by a few points")
}

func TestPeakEnvelopeNeverForecastsBelowTheCurrentPeakUnlessAllowed(t *testing.T) {
	// a load that falls: 300 -> 100
	in := atStep(monday, time.Minute, 200, func(i int, _ time.Time) float64 { return 300 - float64(i) })
	hold, err := PeakEnvelope{EnvelopeWindow: 10}.Forecast(context.Background(), in, 30)
	require.NoError(t, err)
	envelopeNow := in.Values[199-9]
	for _, v := range hold {
		assert.GreaterOrEqual(t, v, envelopeNow-1e-9, "protection: not below the peak of the last window")
	}
	falling, err := PeakEnvelope{EnvelopeWindow: 10, AllowDecline: true}.Forecast(context.Background(), in, 30)
	require.NoError(t, err)
	assert.Less(t, falling[29], envelopeNow, "with AllowDecline the line goes on falling")
	assert.GreaterOrEqual(t, falling[29], 0.0)

	zero := atStep(monday, time.Minute, 100, func(i int, _ time.Time) float64 { return 50 - 0.5*float64(i) })
	out, err := PeakEnvelope{EnvelopeWindow: 5, AllowDecline: true}.Forecast(context.Background(), zero, 400)
	require.NoError(t, err)
	for _, v := range out {
		assert.GreaterOrEqual(t, v, 0.0, "a resource is never negative")
	}
}

func TestPeakEnvelopeSaysWhenTheEnvelopeReachesALimit(t *testing.T) {
	in := atStep(monday, time.Minute, 600, func(i int, _ time.Time) float64 { return sawtooth(i) })
	m := PeakEnvelope{EnvelopeWindow: 25}
	limit := 100 + 0.5*(600+200) + 38 // the peak at step 800
	d, ok, err := m.TimeToLimit(in, limit)
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, 200, d.Minutes(), 25, "about 200 steps of one minute from the end of the history")

	_, reached, err := m.TimeToLimit(in, 100)
	require.NoError(t, err)
	assert.True(t, reached, "the limit is already below the envelope")
	d, _, _ = m.TimeToLimit(in, 100)
	assert.Zero(t, d)

	flat := atStep(monday, time.Minute, 200, func(i int, _ time.Time) float64 { return 100 + noise(i) })
	_, ok, err = m.TimeToLimit(flat, 500)
	require.NoError(t, err)
	assert.False(t, ok, "a flat load never gets there")
}

func TestPeakEnvelopeNeedsItsHistoryAndIsRegistered(t *testing.T) {
	_, err := PeakEnvelope{EnvelopeWindow: 30}.Forecast(context.Background(), atStep(monday, time.Minute, 20, func(i int, _ time.Time) float64 { return 1 }), 3)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = PeakEnvelope{EnvelopeWindow: 1}.Forecast(context.Background(), atStep(monday, time.Minute, 50, func(i int, _ time.Time) float64 { return 1 }), 3)
	assert.Error(t, err)
	m, err := Build("peak_envelope", nil)
	require.NoError(t, err)
	assert.Equal(t, PeakEnvelope{EnvelopeWindow: 30}, m)
	assert.Equal(t, 40, m.MinSamples())
}

// ---- combination

func TestCombinationOfUnlikeModelsBeatsItsWorstMemberAndTheMedianIgnoresAnOutlier(t *testing.T) {
	// a rising series with noise: hist_percentile (flat, high) is far off, ewma lags, theta follows the trend
	full := atStep(monday, time.Minute, 400+10, func(i int, _ time.Time) float64 { return 100 + 0.5*float64(i) + 3*noise(i) })
	in := Input{Values: full.Values[:400], Step: time.Minute, End: monday.Add(399 * time.Minute)}
	want := full.Values[400:410]
	ctx := context.Background()

	members, err := ParseMembers("ewma,theta,hist_percentile")
	require.NoError(t, err)
	errOf := func(m Model) float64 {
		got, err := m.Forecast(ctx, in, 10)
		require.NoError(t, err)
		return meanAbsError(got, want)
	}
	worst := 0.0
	for _, member := range members {
		if e := errOf(member); e > worst {
			worst = e
		}
	}
	weighted := errOf(Combination{Members: members, Mode: "inverse_error"})
	median := errOf(Combination{Members: members, Mode: "median"})
	assert.Less(t, weighted, worst, "a combination is never as bad as its worst member")
	assert.Less(t, median, worst)
	assert.Less(t, weighted, median+1e-9, "weighting by the recent error is at least as good as the median here: theta is the right model and gets the weight")
}

func TestCombinationWeightsFollowTheRecentError(t *testing.T) {
	// a flat series with noise: naive-like ewma is right, a very high percentile is wrong by a constant
	in := atStep(monday, time.Minute, 300, func(i int, _ time.Time) float64 { return 100 + noise(i) })
	members, err := ParseMembers("ewma,hist_percentile")
	require.NoError(t, err)
	hist, err := members[1].Forecast(context.Background(), in, 5)
	require.NoError(t, err)
	ew, err := members[0].Forecast(context.Background(), in, 5)
	require.NoError(t, err)
	got, err := Combination{Members: members, Mode: "inverse_error"}.Forecast(context.Background(), in, 5)
	require.NoError(t, err)
	assert.Less(t, math.Abs(got[0]-ew[0]), math.Abs(got[0]-hist[0]), "the combined value sits nearer the member that was right lately")
}

func TestCombinationLeavesOutAMemberThatHasTooLittleHistory(t *testing.T) {
	// 20 steps: ewma runs, holt_winters with a seasonal term does not, the combination still forecasts
	season := HoltWinters{Season: 15, Trend: true, NonNegative: true}
	in := atStep(monday, time.Minute, 20, func(i int, _ time.Time) float64 { return 100 })
	got, err := Combination{Members: []Model{EWMA{Alpha: 0.3}, season}, Mode: "median"}.Forecast(context.Background(), in, 3)
	require.NoError(t, err)
	assert.InDelta(t, 100, got[0], 1e-9)

	_, err = Combination{Members: []Model{season, SeasonalNaive{Season: 50}}}.Forecast(context.Background(), in, 3)
	assert.ErrorIs(t, err, ErrInsufficientHistory, "no member can run: skipped, with the reason a single algorithm gives")
}

func TestCombinationUsesNothingAfterTheForecastOrigin(t *testing.T) {
	full := atStep(monday, time.Minute, 400, func(i int, _ time.Time) float64 { return 100 + 0.2*float64(i) + noise(i) })
	members, err := ParseMembers("ewma,theta,holt_winters")
	require.NoError(t, err)
	m := Combination{Members: members}
	origin := Input{Values: full.Values[:300], Step: time.Minute, End: monday.Add(299 * time.Minute)}
	want, err := m.Forecast(context.Background(), origin, 10)
	require.NoError(t, err)
	changed := append([]float64(nil), full.Values...)
	for i := 300; i < 400; i++ {
		changed[i] = 1e6
	}
	got, err := m.Forecast(context.Background(), Input{Values: changed[:300], Step: time.Minute, End: origin.End}, 10)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestCombinationMembersAreChecked(t *testing.T) {
	for _, bad := range []string{"", "ewma", "ewma,ewma", "ewma,combination", "ewma,not_an_algorithm", "ewma,seasonal_naive", "a,b,c,d,e,f,g"} {
		_, err := ParseMembers(bad)
		assert.Error(t, err, "%q", bad)
	}
	_, err := Build("combination", map[string]any{"members": "ewma,seasonal_naive"})
	assert.Error(t, err, "seasonal_naive needs a season length, so it cannot run with its defaults")
	_, err = Build("combination", map[string]any{"mode": "mean"})
	assert.Error(t, err)

	m, err := Build("combination", nil)
	require.NoError(t, err)
	c := m.(Combination)
	assert.Len(t, c.Members, 3)
	assert.Equal(t, "inverse_error", c.Mode)
	d, ok := Lookup("combination")
	require.True(t, ok)
	assert.NotEmpty(t, d.Method)
}

// ---- change point reset

// A service whose load halves after a release: 100 with noise, then 50 with noise.
func levelShift(at, before, after, wobble float64, i int) float64 {
	v := before
	if float64(i) >= at {
		v = after
	}
	return v + wobble*noise(i)
}

func TestTheLastLevelShiftIsFoundWhereItHappened(t *testing.T) {
	y := make([]float64, 240)
	for i := range y {
		y[i] = levelShift(150, 100, 200, 2, i)
	}
	start := lastShift(y, 8, 0.2)
	assert.InDelta(t, 150, start, 3, "the regime starts at the release")

	// two shifts: the last one wins
	for i := range y {
		switch {
		case i < 60:
			y[i] = 100 + 2*noise(i)
		case i < 150:
			y[i] = 200 + 2*noise(i)
		default:
			y[i] = 120 + 2*noise(i)
		}
	}
	assert.InDelta(t, 150, lastShift(y, 8, 0.2), 3)
}

func TestNothingIsFoundInASteadyRampOrInNoise(t *testing.T) {
	ramp := make([]float64, 300)
	steady := make([]float64, 300)
	for i := range ramp {
		ramp[i] = 100 + 0.8*float64(i) + 2*noise(i)
		steady[i] = 100 + 5*noise(i)
	}
	assert.Zero(t, lastShift(ramp, 8, 0.2), "a steady rise is a trend, not a shift")
	assert.Zero(t, lastShift(steady, 8, 0.2))

	// a clean series with a tiny jump is below the minimum shift
	tiny := make([]float64, 200)
	for i := range tiny {
		tiny[i] = 100
		if i >= 100 {
			tiny[i] = 103
		}
	}
	assert.Zero(t, lastShift(tiny, 8, 0.2))
	assert.NotZero(t, lastShift(tiny, 8, 0.01), "with a smaller minimum it is a shift")

	// a shift that happened a few steps ago cannot be believed yet
	late := make([]float64, 100)
	for i := range late {
		late[i] = 100 + noise(i)
		if i >= 97 {
			late[i] = 300
		}
	}
	assert.Zero(t, lastShift(late, 8, 0.2))
}

func TestForgettingTheOldLevelPutsTheForecastOnTheNewOne(t *testing.T) {
	in := atStep(monday, time.Minute, 300, func(i int, _ time.Time) float64 { return levelShift(180, 100, 200, 2, i) })
	ctx := context.Background()
	plain, err := Theta{}.Forecast(ctx, in, 10)
	require.NoError(t, err)
	reset, err := RegimeReset{Base: Theta{}, Threshold: 8, MinShift: 0.2}.Forecast(ctx, in, 10)
	require.NoError(t, err)
	assert.InDelta(t, 200, reset[0], 6, "only the history since the release")
	// theta's own slope reads the jump as a trend: it overshoots, which is how the old level hurts, in one direction or the other
	assert.Greater(t, math.Abs(plain[9]-200), 2*math.Abs(reset[9]-200), "the whole history is further from the truth than the regime alone")
}

func TestAResetNeverLeavesTheModelWithoutItsHistory(t *testing.T) {
	// a seasonal baseline needs 100 steps; the new regime has 40: it is given the newest 100, which reach back over the shift
	in := atStep(monday, time.Minute, 300, func(i int, _ time.Time) float64 { return levelShift(260, 100, 200, 1, i) })
	got, err := RegimeReset{Base: SeasonalNaive{Season: 100}, Threshold: 8, MinShift: 0.2}.Forecast(context.Background(), in, 5)
	require.NoError(t, err)
	assert.Len(t, got, 5)

	// and a history that has no shift is passed through untouched
	flat := atStep(monday, time.Minute, 300, func(i int, _ time.Time) float64 { return 100 + noise(i) })
	a, err := EWMA{Alpha: 0.3}.Forecast(context.Background(), flat, 5)
	require.NoError(t, err)
	b, err := RegimeReset{Base: EWMA{Alpha: 0.3}, Threshold: 8, MinShift: 0.2}.Forecast(context.Background(), flat, 5)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestTheResetUsesNothingAfterTheForecastOrigin(t *testing.T) {
	full := atStep(monday, time.Minute, 400, func(i int, _ time.Time) float64 { return levelShift(150, 100, 200, 2, i) })
	m := RegimeReset{Base: Theta{}, Threshold: 8, MinShift: 0.2}
	origin := Input{Values: full.Values[:250], Step: time.Minute, End: monday.Add(249 * time.Minute)}
	want, err := m.Forecast(context.Background(), origin, 10)
	require.NoError(t, err)
	changed := append([]float64(nil), full.Values...)
	for i := 250; i < 400; i++ {
		changed[i] = -1e6
	}
	got, err := m.Forecast(context.Background(), Input{Values: changed[:250], Step: time.Minute, End: origin.End}, 10)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestTheResetIsAParameterOfEveryNonBaselineAlgorithmAndOffByDefault(t *testing.T) {
	for _, id := range []string{"theta", "holt_winters", "ewma", "gbdt", "knn", "seasonal_profile", "peak_envelope", "combination"} {
		def, ok := Lookup(id)
		require.True(t, ok, id)
		var names []string
		for _, p := range def.Params {
			names = append(names, p.Name)
		}
		assert.Contains(t, names, "reset_on_change", id)
		normalized, err := Normalize(id, requiredFor(id))
		require.NoError(t, err, id)
		assert.NotContains(t, Canonical(id, normalized), "reset_on_change", "%s: a default new parameter is not stored, so what exists keeps its version", id)
		m, err := Build(id, requiredFor(id))
		require.NoError(t, err, id)
		_, wrapped := m.(RegimeReset)
		assert.False(t, wrapped, id)
	}
	for _, baseline := range []string{"naive", "seasonal_naive"} {
		def, _ := Lookup(baseline)
		for _, p := range def.Params {
			assert.NotEqual(t, "reset_on_change", p.Name, baseline)
		}
	}

	params := requiredFor("theta")
	params["reset_on_change"] = true
	m, err := Build("theta", params)
	require.NoError(t, err)
	r, ok := m.(RegimeReset)
	require.True(t, ok)
	assert.Equal(t, "theta", r.ID())
	assert.Equal(t, 8.0, r.Threshold)

	// a quantile forecast of a reset model is still a quantile forecast
	q, err := Build("theta", map[string]any{"reset_on_change": true, "quantile_level": 0.9})
	require.NoError(t, err)
	level, ok := q.(Quantiled)
	require.True(t, ok)
	got, isQuantile := level.QuantileLevel()
	assert.True(t, isQuantile)
	assert.Equal(t, 0.9, got)
}

// requiredFor gives an algorithm the parameters it cannot do without.
func requiredFor(id string) map[string]any {
	switch id {
	case "gbdt":
		return map[string]any{"lags": 6}
	}
	return map[string]any{}
}

func TestTheNewAlgorithmsAreInTheRegistryWithTheirExplanations(t *testing.T) {
	for _, id := range []string{"seasonal_profile", "peak_envelope", "combination"} {
		d, ok := Lookup(id)
		require.True(t, ok, id)
		assert.Equal(t, "statistical", d.Category, id)
		assert.NotEmpty(t, d.Method, id)
		assert.NotEmpty(t, d.Complexity, id)
		assert.False(t, d.Baseline, id)
	}
}
