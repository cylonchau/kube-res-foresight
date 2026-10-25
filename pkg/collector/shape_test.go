package collector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToInputFillsAGapTheWayTheSourceSays(t *testing.T) {
	step := time.Minute
	pts := grid(10, step, map[int]bool{4: true, 5: true}, func(i int) float64 { return float64(i * 10) })
	cases := map[string][]float64{
		FillInterpolate: {0, 10, 20, 30, 40, 50, 60, 70, 80, 90},
		FillForward:     {0, 10, 20, 30, 30, 30, 60, 70, 80, 90},
		FillZero:        {0, 10, 20, 30, 0, 0, 60, 70, 80, 90},
		FillSkip:        {60, 70, 80, 90},
	}
	for fill, want := range cases {
		in, _, ok := ToInput(pts, t0, t0.Add(9*step), step, 3, fill)
		require.True(t, ok, fill)
		assert.Equal(t, want, in.Values, fill)
	}
}

func TestShapeAggregatesToTheDatasetStepWithoutLookingAhead(t *testing.T) {
	// 1-minute samples 1..12 from minute 1 to minute 12; the dataset step is 5 minutes
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var pts []Point
	for i := 1; i <= 12; i++ {
		pts = append(pts, Point{Time: base.Add(time.Duration(i) * time.Minute), Value: float64(i)})
	}
	cases := map[string][]float64{
		"avg":  {3, 8}, // (1..5)/5, (6..10)/5; the bucket labelled 00:15 is not complete
		"max":  {5, 10},
		"min":  {1, 6},
		"last": {5, 10},
		"sum":  {15, 40},
	}
	for agg, want := range cases {
		out, err := Shape(pts, Shaping{Step: 5 * time.Minute, QueryStep: time.Minute, Aggregation: agg, Scale: 1, Precision: 6})
		require.NoError(t, err, agg)
		require.Len(t, out, 2, agg)
		assert.Equal(t, base.Add(5*time.Minute), out[0].Time, "the label is the end of the bucket")
		assert.Equal(t, want, []float64{out[0].Value, out[1].Value}, agg)
	}
}

func TestShapeScalesAndRounds(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pts := []Point{{Time: base, Value: 1234}, {Time: base.Add(time.Minute), Value: 2500}}
	out, err := Shape(pts, Shaping{Step: time.Minute, QueryStep: time.Minute, Scale: 0.001, Precision: 2})
	require.NoError(t, err)
	assert.Equal(t, []float64{1.23, 2.5}, []float64{out[0].Value, out[1].Value}, "millicores to cores, two decimals")
}

func TestShapeRefusesAGridThatCannotBeMade(t *testing.T) {
	_, err := Shape(nil, Shaping{Step: time.Minute, QueryStep: 5 * time.Minute})
	assert.ErrorContains(t, err, "finer")
	_, err = Shape(nil, Shaping{Step: 7 * time.Minute, QueryStep: 5 * time.Minute})
	assert.ErrorContains(t, err, "whole multiple")
}
