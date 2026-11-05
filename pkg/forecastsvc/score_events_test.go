package forecastsvc

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/scoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A load that is flat except for a peak at 09:00 every day: the newest windows (the calm after the peak) have no rise, the windows of the
// morning ramps do. Those are what the scoring grades besides the newest.
func TestEventWindowsAreTheStretchesWhereTheLoadRose(t *testing.T) {
	step := time.Minute
	days := 5
	end := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	n := days * 24 * 60
	values := make([]float64, n)
	in := forecast.Input{End: end, Step: step, Values: values}
	for i := range values {
		at := in.TimeAt(i)
		values[i] = 10
		if h := at.Hour(); h == 9 { // an hour of peak
			values[i] = 25
		}
	}
	horizon := 15 * time.Minute
	got := eventWindows(in, end, horizon, 6, 6, 0.15, in.TimeAt(0).Add(24*time.Hour))
	require.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), 6)
	for _, w := range got {
		assert.True(t, w.event)
		assert.GreaterOrEqual(t, w.rise, 0.15)
		assert.False(t, w.start.Before(in.TimeAt(0).Add(24*time.Hour)), "no window the algorithm has too little history for")
		assert.Equal(t, 9, w.end.Hour(), "the rise is where the peak starts: the window ends inside the 09:00 hour")
	}
	for i := 1; i < len(got); i++ {
		assert.True(t, got[i-1].start.Before(got[i].start), "in time order")
	}
}

func TestEventWindowsOfAFlatLoadAreNone(t *testing.T) {
	end := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	values := make([]float64, 3*24*60)
	for i := range values {
		values[i] = 10
	}
	in := forecast.Input{End: end, Step: time.Minute, Values: values}
	assert.Empty(t, eventWindows(in, end, 15*time.Minute, 6, 6, 0.15, in.TimeAt(0)))
}

// The reason for the event windows, shown on a load with a daily peak: graded on the calm newest windows alone, repeating the last value
// is as good as an algorithm that knows the day; graded with the windows in which the load rose, it is not.
func TestGradingTheRisesLetsAnAlgorithmThatSeesThePeakOutscoreRepeatingTheLastValue(t *testing.T) {
	ctx := context.Background()
	step := time.Minute
	end := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	values := make([]float64, 6*24*60)
	in := forecast.Input{End: end, Step: step, Values: values}
	for i := range values {
		values[i] = 10
		if in.TimeAt(i).Hour() == 9 {
			values[i] = 25
		}
	}
	horizon := 15 * time.Minute
	steps := int(horizon / step)
	naive, err := forecast.Build("naive", nil)
	require.NoError(t, err)
	seasonal, err := forecast.Build("seasonal_naive", map[string]any{"season_length_steps": 24 * 60})
	require.NoError(t, err)

	grade := func(m forecast.Model, specs []windowSpec) float64 {
		var ws []scoring.Window
		for _, sp := range specs {
			i0, _ := gridIndex(in, sp.start)
			i1, _ := gridIndex(in, sp.end)
			actual := 0.0
			for _, v := range values[i0+1 : i1+1] {
				actual = math.Max(actual, v)
			}
			got, err := m.Forecast(ctx, forecast.Input{Values: values[:i0+1], End: sp.start, Step: step}, steps)
			require.NoError(t, err)
			predicted := 0.0
			for _, v := range got {
				predicted = math.Max(predicted, v)
			}
			ws = append(ws, scoring.Window{Start: sp.start, End: sp.end, Complete: true,
				Predicted: scoring.Outcome{HasCPU: true, CPU: predicted}, Actual: scoring.Outcome{HasCPU: true, CPU: actual}})
		}
		return scoring.Score(ws, scoring.DefaultWeights()).Score
	}
	var recent []windowSpec
	for k := 6; k >= 1; k-- {
		recent = append(recent, windowSpec{start: end.Add(-time.Duration(k) * horizon), end: end.Add(-time.Duration(k-1) * horizon)})
	}
	events := eventWindows(in, end, horizon, 6, 6, 0.15, in.TimeAt(0).Add(24*time.Hour+time.Hour))
	require.NotEmpty(t, events)
	both := append(append([]windowSpec{}, recent...), events...)

	assert.GreaterOrEqual(t, grade(naive, recent), grade(seasonal, recent)-1e-9, "in the calm, repeating the last value is as good as any")
	assert.Greater(t, grade(seasonal, both), grade(naive, both), "with the rises graded, the algorithm that sees the daily peak coming wins")
}
