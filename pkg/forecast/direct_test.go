package forecast

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectStepsAreSpreadFromTheFirstToTheLastAndInterpolated(t *testing.T) {
	assert.Equal(t, []int{1, 2, 3}, directSteps(3, 8), "a horizon within the anchors gives every step its own model")
	steps := directSteps(60, 8)
	require.Len(t, steps, 8)
	assert.Equal(t, 1, steps[0])
	assert.Equal(t, 60, steps[len(steps)-1])
	for i := 1; i < len(steps); i++ {
		assert.Greater(t, steps[i], steps[i-1])
	}
	assert.Len(t, directSteps(60, 0), DefaultDirectAnchors, "0 means the default")

	// between two anchors the value is linear; outside them it is the nearest
	out := interpolateSteps([]int{1, 5}, []float64{10, 18}, 7)
	assert.Equal(t, []float64{10, 12, 14, 16, 18, 18, 18}, out)
}

func TestDirectTrainingAtStepOneIsTheOrdinaryMatrixAndLaterStepsLookAhead(t *testing.T) {
	y := gen(120, func(i int) float64 { return float64(i*i%17) + float64(i) })
	spec := featureSpec{Lags: 3, Calendar: true}
	x1, y1 := spec.training(inputOf(y), 0)
	xd, yd := spec.trainingDirect(inputOf(y), 0, 1)
	assert.Equal(t, x1, xd)
	assert.Equal(t, y1, yd)

	in := inputOf(y)
	x5, y5 := spec.trainingDirect(in, 0, 5)
	require.Len(t, x5, len(y)-5-3+1)
	// the first row: lags are the three values before origin 3, the target is the value 4 steps after the origin
	assert.Equal(t, y[2], x5[0][0])
	assert.Equal(t, y[0], x5[0][2])
	assert.Equal(t, y[3+5-1], y5[0])
	assert.Equal(t, calendarFeatures(in.TimeAt(3 + 5 - 1))[0], x5[0][3], "the calendar is the one of the target time")
	assert.Equal(t, y[len(y)-1], y5[len(y5)-1], "the last row ends on the newest value: nothing is looked up beyond the history")

	_, ylimit := spec.trainingDirect(in, 10, 5)
	assert.Len(t, ylimit, 10, "only the latest rows when the window is limited")
	assert.Equal(t, y[len(y)-1], ylimit[len(ylimit)-1])
}

// A noisy sine seen through three lags: the one-step model is shrunk by the noise at every step it is rolled forward, so the
// recursive forecast flattens toward the mean after a few steps; a model fitted for the step ahead itself keeps the shape.
func TestDirectForecastDoesNotFlattenWhereRecursiveDoes(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	n := 1500
	truth := func(i int) float64 { return 100 + 40*math.Sin(2*math.Pi*float64(i)/50) }
	y := gen(n, func(i int) float64 { return truth(i) + 12*rng.NormFloat64() })
	horizon := 40
	meanError := func(m Model) float64 {
		var sum float64
		var count int
		for c := 600; c+horizon <= n; c += 50 {
			out, err := m.Forecast(context.Background(), inputOf(y[:c]), horizon)
			require.NoError(t, err)
			for j, v := range out {
				sum += math.Abs(v - truth(c+j))
				count++
			}
		}
		return sum / float64(count)
	}
	gbdt := GBDT{Lags: 3, Trees: 60, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 5}
	ridge := RidgeAR{Lags: 3, L2Lambda: 0.01}
	gbdtDirect, ridgeDirect := gbdt, ridge
	gbdtDirect.Direct, ridgeDirect.Direct = true, true

	assert.Less(t, meanError(ridgeDirect), meanError(ridge)*0.9, "ridge: the direct forecast is closer to the real signal over 40 steps")
	assert.Less(t, meanError(gbdtDirect), meanError(gbdt)*0.9, "gbdt: likewise")
}

func TestDirectFirstStepIsTheRecursiveFirstStepAndTheForecastIsDeterministic(t *testing.T) {
	y := gen(400, func(i int) float64 { return 50 + 10*math.Sin(float64(i)/7) + float64(i%5) })
	for _, pair := range [][2]Model{
		{RidgeAR{Lags: 4, L2Lambda: 0.01}, RidgeAR{Lags: 4, L2Lambda: 0.01, Direct: true}},
		{GBDT{Lags: 4, Trees: 30, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 5}, GBDT{Lags: 4, Trees: 30, LearningRate: 0.1, MaxDepth: 3, MinLeaf: 5, Direct: true}},
	} {
		rec, err := pair[0].Forecast(context.Background(), inputOf(y), 12)
		require.NoError(t, err)
		dir, err := pair[1].Forecast(context.Background(), inputOf(y), 12)
		require.NoError(t, err)
		again, err := pair[1].Forecast(context.Background(), inputOf(y), 12)
		require.NoError(t, err)
		assert.InDelta(t, rec[0], dir[0], 1e-9, "step one is the same fit either way")
		assert.Equal(t, dir, again)
		assert.Len(t, dir, 12)
	}
}

func TestDirectNeedsTheHorizonOnTopOfTheHistoryTheOtherWayNeeds(t *testing.T) {
	ridge := RidgeAR{Lags: 4, L2Lambda: 0.01}
	direct := ridge
	direct.Direct = true
	assert.Equal(t, ridge.MinSamples(), MinSamplesFor(ridge, 30))
	assert.Equal(t, ridge.MinSamples()+29, MinSamplesFor(direct, 30))
	g := GBDT{Lags: 4, MinLeaf: 5}
	gd := g
	gd.Direct = true
	assert.Equal(t, g.MinSamples()+19, MinSamplesFor(gd, 20))

	_, err := direct.Forecast(context.Background(), inputOf(gen(direct.MinSamples()+5, func(i int) float64 { return float64(i % 9) })), 30)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
	_, err = RidgeAR{Lags: 4, Direct: true, DirectAnchors: 1}.Forecast(context.Background(), inputOf(gen(200, func(i int) float64 { return float64(i % 9) })), 10)
	assert.Error(t, err)
}

func TestDirectIsAnOptionOfRidgeAndGBDTAndInvisibleUntilUsed(t *testing.T) {
	for _, id := range []string{"ridge_ar", "gbdt"} {
		raw := map[string]any{"lags": 6}
		normalized, err := Normalize(id, raw)
		require.NoError(t, err)
		canonical := Canonical(id, normalized)
		assert.NotContains(t, canonical, "multi_step", "the stored parameters of an existing setting are unchanged: %s", id)
		assert.NotContains(t, canonical, "direct_anchors")

		m, err := Build(id, normalized)
		require.NoError(t, err)
		switch v := m.(type) {
		case RidgeAR:
			assert.False(t, v.Direct)
		case GBDT:
			assert.False(t, v.Direct)
		}
		raw["multi_step"] = "direct"
		raw["direct_anchors"] = 4
		normalized, err = Normalize(id, raw)
		require.NoError(t, err)
		assert.Equal(t, "direct", Canonical(id, normalized)["multi_step"])
		m, err = Build(id, normalized)
		require.NoError(t, err)
		switch v := m.(type) {
		case RidgeAR:
			assert.True(t, v.Direct)
			assert.Equal(t, 4, v.DirectAnchors)
		case GBDT:
			assert.True(t, v.Direct)
			assert.Equal(t, 4, v.DirectAnchors)
		}
		_, err = Normalize(id, map[string]any{"lags": 6, "multi_step": "sideways"})
		assert.Error(t, err)
	}
}
