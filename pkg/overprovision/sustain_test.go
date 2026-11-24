package overprovision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pattern makes points of 5 minutes whose use is given by fn(index).
func pattern(n int, fn func(i int) float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{TS: int64(i) * 300, V: fn(i)}
	}
	return out
}

// 1000 points: 800 at 2 cores, 100 at 3 and 100 at 4. When the 4 are single moments spread over the day, the use was never 4 for long; when they are one stretch it was.
func TestAShortPeakDoesNotRaiseWhatARequestIsSizedFor(t *testing.T) {
	spikes := pattern(1000, func(i int) float64 {
		switch {
		case i%10 == 0:
			return 4
		case i%10 == 5:
			return 3
		}
		return 2
	})
	stretch := pattern(1000, func(i int) float64 {
		switch {
		case i >= 100 && i < 200:
			return 4
		case i >= 500 && i < 600:
			return 3
		}
		return 2
	})
	p := DefaultParams()
	p.SustainMinutes = 30
	in := func(points []Point) Input {
		return Input{Resource: CPU, Request: 6, Points: points, StepSeconds: 300, WindowMinutes: 1000 * 5, Throttle: -1}
	}
	short, long := Analyze(in(spikes), p), Analyze(in(stretch), p)
	require.NotNil(t, short.Sustained)
	// the raw P99 of both is 4 cores; held for 30 minutes the single moments are about 2.3, the stretch is 4
	assert.Equal(t, 4.0, short.Usage.P99)
	assert.Equal(t, 4.0, long.Usage.P99)
	assert.Less(t, short.Sustained.P99, 2.7)
	assert.Equal(t, 4.0, long.Sustained.P99)
	// the request follows what lasted: the stretch asks for more than the same amount of peak in single moments
	assert.Less(t, short.Candidates[2].Needed, long.Candidates[2].Needed)
	assert.InDelta(t, long.Sustained.P99/1.1, long.Candidates[2].Needed, 1e-9)
	// the limit is sized for the peak: both keep the raw P99
	assert.InDelta(t, 4.0*1.2, short.SuggestedLimit, 0.5)
	assert.GreaterOrEqual(t, short.SuggestedLimit, short.Suggested)

	// nothing is held with a window of 0: the percentiles are the raw ones, as they were
	p.SustainMinutes = 0
	off := Analyze(in(spikes), p)
	assert.Nil(t, off.Sustained)
	assert.InDelta(t, 4.0/1.1, off.Candidates[2].Needed, 1e-9)
}

func TestHowLongTheUseWasAboveALevel(t *testing.T) {
	// 100 points of 5 minutes: two stretches above 3 cores, of 4 and 10 points
	points := pattern(100, func(i int) float64 {
		if (i >= 10 && i < 14) || (i >= 50 && i < 60) {
			return 4
		}
		return 2
	})
	e := ExceedOf(points, 3, 300)
	require.NotNil(t, e)
	assert.Equal(t, 2, e.Events)
	assert.Equal(t, 50, e.LongestMinutes)
	assert.InDelta(t, 0.14, e.Share, 1e-9)
	assert.Nil(t, ExceedOf(nil, 3, 300))
	assert.Nil(t, ExceedOf(points, 0, 300))
}

func TestTheSustainedSeriesNeedsAWindowTheStepCanFill(t *testing.T) {
	points := pattern(20, func(i int) float64 { return float64(i % 3) })
	// a window shorter than two steps holds nothing
	same := Sustained(points, 5)
	assert.Equal(t, points, same)
	held := Sustained(points, 15)
	require.Len(t, held, 20)
	// the mean of the three newest points (a 15 minute window at a 5 minute step reaches back to the point 15 minutes before, not including it)
	assert.InDelta(t, 1.0, held[5].V, 1e-9)
}
