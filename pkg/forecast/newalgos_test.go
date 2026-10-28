package forecast

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func periodic(n, period int) Input {
	return series(n, func(i int) float64 { return 10 + 4*math.Sin(2*math.Pi*float64(i)/float64(period)) })
}

// mae against the true continuation of a periodic series
func continuationError(t *testing.T, m Model, period int) float64 {
	t.Helper()
	const horizon = 12
	full := periodic(600+horizon, period)
	in := Input{Values: full.Values[:600], Step: time.Minute, End: t0.Add(599 * time.Minute)}
	got, err := m.Forecast(context.Background(), in, horizon)
	require.NoError(t, err)
	var e float64
	for h := range got {
		e += math.Abs(got[h] - full.Values[600+h])
	}
	return e / horizon
}

func TestHistPercentileIsAWeightedPercentileWithDecay(t *testing.T) {
	// 0..9 with no decay: the 50th percentile is the 5th smallest value, 4
	in := series(10, func(i int) float64 { return float64(i) })
	got, err := HistPercentile{Percentile: 50}.Forecast(context.Background(), in, 3)
	require.NoError(t, err)
	assert.Equal(t, []float64{4, 4, 4}, got)

	// an old spike is forgotten with a short half life and kept without decay
	spike := series(400, func(i int) float64 {
		if i < 20 {
			return 100
		}
		return 1
	})
	keep, err := HistPercentile{Percentile: 99}.Forecast(context.Background(), spike, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 100, keep[0])
	fade, err := HistPercentile{Percentile: 99, HalfLifeHours: 1}.Forecast(context.Background(), spike, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fade[0], "380 minutes after the spike its weight is below 2%% of the total")

	margin, err := HistPercentile{Percentile: 50, SafetyMargin: 0.5}.Forecast(context.Background(), in, 1)
	require.NoError(t, err)
	assert.InDelta(t, 6, margin[0], 1e-9)

	for _, bad := range []HistPercentile{{Percentile: 0}, {Percentile: 101}, {Percentile: 50, HalfLifeHours: -1}, {Percentile: 50, SafetyMargin: -1}} {
		_, err = bad.Forecast(context.Background(), in, 1)
		assert.Error(t, err, "%+v", bad)
	}
}

func TestRandomForestIsDeterministicAndLearnsAPeriodicShape(t *testing.T) {
	m := RandomForest{Lags: 12, Trees: 30, MaxDepth: 6, MinLeaf: 3, FeatureRatio: 0.7, SampleRatio: 0.8, Seed: 7}
	in := periodic(600, 60)
	a, err := m.Forecast(context.Background(), in, 8)
	require.NoError(t, err)
	b, err := m.Forecast(context.Background(), in, 8)
	require.NoError(t, err)
	assert.Equal(t, a, b, "the same seed gives the same forecast")
	m2 := m
	m2.Seed = 8
	c, err := m2.Forecast(context.Background(), in, 8)
	require.NoError(t, err)
	assert.NotEqual(t, a, c, "another seed grows other trees")

	assert.Less(t, continuationError(t, m, 60), 1.0, "close to the true continuation of a sine of amplitude 4")
	assert.Less(t, continuationError(t, m, 60), continuationError(t, Naive{}, 60)+4)

	for _, bad := range []RandomForest{{Lags: 0}, {Lags: 3, Trees: 0}, {Lags: 3, Trees: 1, MaxDepth: 0}, {Lags: 3, Trees: 1, MaxDepth: 2, MinLeaf: 1, FeatureRatio: 0}, {Lags: 3, Trees: 1, MaxDepth: 2, MinLeaf: 1, FeatureRatio: 1, SampleRatio: 2}} {
		_, err = bad.Forecast(context.Background(), in, 1)
		assert.Error(t, err, "%+v", bad)
	}
	_, err = m.Forecast(context.Background(), series(10, func(i int) float64 { return float64(i) }), 2)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
}

func TestRandomForestUsesCovariatesOnlyThroughTheirPast(t *testing.T) {
	n := 500
	rng := rand.New(rand.NewSource(1))
	driver := make([]float64, n)
	target := make([]float64, n)
	for i := range driver {
		driver[i] = rng.Float64() * 10
	}
	for i := 1; i < n; i++ {
		target[i] = 2 * driver[i-1] // the target follows the driver one step later
	}
	m := RandomForest{Lags: 2, Trees: 40, MaxDepth: 6, MinLeaf: 3, FeatureRatio: 1, SampleRatio: 1, CovLags: 2, Seed: 3}
	without := Input{Values: target, Step: time.Minute, End: t0.Add(time.Duration(n-1) * time.Minute)}
	with := without
	with.Covariates = map[string][]float64{"driver": driver}
	a, err := m.Forecast(context.Background(), without, 1)
	require.NoError(t, err)
	b, err := m.Forecast(context.Background(), with, 1)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	// the next value is 2 x the last driver value
	assert.InDelta(t, 2*driver[n-1], b[0], 3, "the covariate predicts the next target")
}

func TestKNNFindsTheMatchingEarlierWindow(t *testing.T) {
	assert.Less(t, continuationError(t, KNN{Window: 20, K: 3, Normalize: "relative"}, 50), 0.2)
	// a pattern seen at half the load still matches with relative windows and not with raw ones
	shape := []float64{1, 3, 2, 5, 4, 6, 3, 2}
	tail := []float64{7, 9}
	var values []float64
	for _, v := range shape {
		values = append(values, 2*v) // the shape ran once at twice the level
	}
	for _, v := range tail {
		values = append(values, 2*v)
	}
	for i := 0; i < 12; i++ {
		values = append(values, 2) // a flat stretch
	}
	values = append(values, shape...) // and now at the base level
	in := Input{Values: values, Step: time.Minute, End: t0.Add(time.Duration(len(values)-1) * time.Minute)}
	rel, err := KNN{Window: 8, K: 1, Normalize: "relative"}.Forecast(context.Background(), in, 2)
	require.NoError(t, err)
	raw, err := KNN{Window: 8, K: 1, Normalize: "none"}.Forecast(context.Background(), in, 2)
	require.NoError(t, err)
	assert.InDeltaSlice(t, tail, rel, 1e-9, "the relative match finds the doubled copy and scales its continuation back")
	assert.NotEqual(t, tail, raw, "raw matching prefers the flat stretch")

	_, err = KNN{Window: 1, K: 1}.Forecast(context.Background(), in, 1)
	assert.Error(t, err)
	_, err = KNN{Window: 8, K: 1, Normalize: "x"}.Forecast(context.Background(), in, 1)
	assert.Error(t, err)
	_, err = KNN{Window: 8, K: 500}.Forecast(context.Background(), series(40, func(i int) float64 { return float64(i) }), 1)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
}

func TestNewAlgorithmsAreRegisteredWithSchemasAndBuild(t *testing.T) {
	covariate := map[string]bool{"random_forest": true, "knn": false, "hist_percentile": false}
	for id, wantCov := range covariate {
		def, ok := Lookup(id)
		require.True(t, ok, id)
		assert.Equal(t, wantCov, def.Covariates, id)
		assert.NotEmpty(t, def.Description, id)
		assert.NotEmpty(t, def.Complexity, id)
		params := map[string]any{}
		if id == "random_forest" {
			params["lags"] = 6
		}
		m, err := Build(id, params)
		require.NoError(t, err, id)
		assert.Equal(t, id, m.ID())
	}
	_, err := Build("random_forest", map[string]any{})
	assert.Error(t, err, "lags is required")
	_, err = Build("knn", map[string]any{"normalize": "sideways"})
	assert.Error(t, err)
}
