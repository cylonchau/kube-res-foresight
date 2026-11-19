package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The newest sample may be as old as two cycles of the collection: the history is collected once a cycle, so a step of 30 seconds with a limit of five
// steps called it stale during half of every cycle.
func TestTheStaleLimitIsNeverShorterThanTwoCyclesOfTheCollection(t *testing.T) {
	m := &model.AlgorithmRun{}
	assert.Equal(t, 10*time.Minute, staleAfter(m, 30*time.Second), "5 steps of 30 s is 2.5 minutes: the two cycles of 5 minutes are longer")
	assert.Equal(t, 10*time.Minute, staleAfter(m, time.Minute))
	assert.Equal(t, 50*time.Minute, staleAfter(m, 10*time.Minute), "5 steps of 10 minutes are longer than the cycles")
	m.RunIntervalMinutes = 15
	assert.Equal(t, 30*time.Minute, staleAfter(m, 30*time.Second), "a module that runs every 15 minutes is collected that often")
}

func samplesAt(step int64, from, to int64, skip func(ts int64) bool) []model.MetricSample {
	var out []model.MetricSample
	for ts := from; ts <= to; ts += step {
		if skip != nil && skip(ts) {
			continue
		}
		out = append(out, model.MetricSample{TS: ts, Value: 1})
	}
	return out
}

// A hole of two days in a month of history, ten days ago: the run after it is 10 days of 30, which used to be 33% (under the 80% asked for) and grew a step
// a step. Over the newest steps the algorithm needs (a week of history here) it is whole.
func TestTheQualityIsJudgedOverTheStepsTheAlgorithmNeeds(t *testing.T) {
	const step = 300
	day := int64(24 * 3600)
	end := 30 * day
	hole := func(ts int64) bool { return ts >= 18*day && ts < 20*day }
	samples := samplesAt(step, 0, end, hole)
	span := int(end/step) + 1
	run := int((end-20*day)/step) + 1
	need := int(7*day/step) * 100 / needMarginPercent // 7 days of 5-minute steps with the room kept over the need

	present, usable, steps := qualityWindow(samples, run, span, step, need, 0)
	assert.Equal(t, int(7*day/step), steps, "the window is the need with its room")
	assert.Equal(t, steps, usable)
	m := &model.AlgorithmRun{FallbackMinHistoryRatio: 0.8, MaxMissingRatio: 0.3}
	assert.NoError(t, checkQuality(m, "cpu", present, usable, steps, func() string { return "" }))
	// judged over the whole span it is refused, as it was
	assert.Error(t, checkQuality(m, "cpu", len(samples), run, span, func() string { return "" }))

	// a hole inside the window is still refused
	hole2 := func(ts int64) bool { return ts >= 26*day && ts < 28*day }
	samples = samplesAt(step, 0, end, hole2)
	run = int((end-28*day)/step) + 1
	present, usable, steps = qualityWindow(samples, run, span, step, need, 0)
	assert.Error(t, checkQuality(m, "cpu", present, usable, steps, func() string { return "" }))

	// the need is not known, or the span is shorter than the window: the whole span
	present, usable, steps = qualityWindow(samples, run, span, step, 0, 0)
	assert.Equal(t, span, steps)
	assert.Equal(t, len(samples), present)
	_, _, steps = qualityWindow(samples[:100], 100, 100, step, need, 0)
	assert.Equal(t, 100, steps)
	// never over less than a day, however little the algorithm needs
	_, _, steps = qualityWindow(samples, run, span, step, 3, 0)
	assert.Equal(t, int(day/step), steps)
}

func TestTheHolesOfTheStoredHistoryAreFoundLargestFirst(t *testing.T) {
	samples := samplesAt(60, 0, 6000, func(ts int64) bool { return (ts >= 600 && ts < 780) || (ts >= 3000 && ts < 3600) || ts == 4980 })
	gaps := findGaps(samples, time.Minute)
	require.Len(t, gaps, 3)
	assert.Equal(t, 10, gaps[0].steps)
	assert.Equal(t, time.Unix(3000, 0).UTC(), gaps[0].from)
	assert.Equal(t, time.Unix(3540, 0).UTC(), gaps[0].to)
	assert.Equal(t, 3, gaps[1].steps)
	assert.Equal(t, 1, gaps[2].steps)
	assert.Empty(t, findGaps(samplesAt(60, 0, 600, nil), time.Minute))
}

// The first collection backfills the range and the later ones continue from the newest sample, so a hole in what was stored stayed one for good. Now
// the next collection after an hour asks the data source for it again.
func TestAHoleInTheStoredHistoryIsFetchedAgainFromTheDataSource(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	series, err := f.svc.Samples.Series(ctx, f.module.ID, "cpu")
	require.NoError(t, err)
	before, err := f.svc.Samples.Range(ctx, series.ID, time.Unix(0, 0), testNow.Add(24*time.Hour))
	require.NoError(t, err)
	require.Greater(t, len(before), 100)

	// the stored history loses the middle of its range
	mid := before[len(before)/2].TS
	holeFrom, holeTo := mid-40*60, mid+40*60
	require.NoError(t, f.db.Where("series_id = ? AND ts >= ? AND ts <= ?", series.ID, holeFrom, holeTo).Delete(&model.MetricSample{}).Error)
	gone, _ := f.svc.Samples.Range(ctx, series.ID, time.Unix(0, 0), testNow.Add(24*time.Hour))
	require.Less(t, len(gone), len(before))

	// within the hour the series is not looked at again
	res, err := f.collect(ctx)
	require.NoError(t, err)
	assert.Zero(t, res[0].Repaired)
	still, _ := f.svc.Samples.Range(ctx, series.ID, time.Unix(0, 0), testNow.Add(24*time.Hour))
	assert.Len(t, still, len(gone))

	// after the hour it is, and the hole is filled from the data source
	*f.clock = testNow.Add(61 * time.Minute)
	res, err = f.collect(ctx)
	require.NoError(t, err)
	assert.Greater(t, res[0].Repaired, 0)
	assert.Empty(t, res[0].Error)
	filled, _ := f.svc.Samples.Range(ctx, series.ID, time.Unix(holeFrom, 0), time.Unix(holeTo, 0))
	assert.Greater(t, len(filled), 30, "the hole of 81 minutes has its samples again")

	// a hole the data source never had is asked for once, then left alone for a day
	f.querier.skip = func(ts time.Time) bool { return ts.Unix() >= holeFrom && ts.Unix() <= holeTo }
	require.NoError(t, f.db.Where("series_id = ? AND ts >= ? AND ts <= ?", series.ID, holeFrom, holeTo).Delete(&model.MetricSample{}).Error)
	*f.clock = testNow.Add(123 * time.Minute)
	res, err = f.collect(ctx)
	require.NoError(t, err)
	assert.Zero(t, res[0].Repaired, "the source has nothing there")
	*f.clock = testNow.Add(185 * time.Minute)
	f.querier.skip = nil
	res, err = f.collect(ctx)
	require.NoError(t, err)
	assert.Zero(t, res[0].Repaired, "asked for a few hours ago: not again before a day has passed")
	*f.clock = testNow.Add(26 * time.Hour)
	res, err = f.collect(ctx)
	require.NoError(t, err)
	assert.Greater(t, res[0].Repaired, 0, "a day later it is asked for again")
}
