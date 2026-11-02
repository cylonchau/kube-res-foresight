package forecastsvc

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

// previewFixture is a module with three days of a daily load in requests per second and the naive algorithm in use.
func previewFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(context.Background())
	require.NoError(t, err)
	return f
}

type tableCounts map[string]int64

func (f *fixture) counts(t *testing.T) tableCounts {
	t.Helper()
	out := tableCounts{}
	for name, row := range map[string]any{"prediction": &model.Prediction{}, "recommendation": &model.Recommendation{}, "training": &model.TrainingRun{},
		"evaluation": &model.PredictionEvaluation{}, "score": &model.AlgorithmScore{}, "outcome": &model.ScaleOutcome{}, "backtest": &model.BacktestRun{}} {
		var n int64
		require.NoError(t, f.db.Model(row).Count(&n).Error)
		out[name] = n
	}
	return out
}

// More safety margin asks for at least as many replicas in every window, holds more replica-hours, and cannot be short of the need more
// often; the replay writes nothing and gives the same answer twice.
func TestAPreviewOfMoreHeadroomAsksForMoreReplicasAndWritesNothing(t *testing.T) {
	f := previewFixture(t)
	ctx := context.Background()
	before := f.counts(t)

	rep, err := f.svc.PreviewChange(ctx, f.module.ID, 24, PreviewChange{HeadroomRatio: f64(1.6)})
	require.NoError(t, err)
	require.Positive(t, rep.Windows)
	assert.Equal(t, "naive", rep.Current.Algorithm)
	assert.Equal(t, 15, rep.WindowMinutes)
	for _, p := range rep.Points {
		assert.GreaterOrEqual(t, p.Proposed, p.Current, "a larger margin never asks for fewer replicas (%s)", p.At)
	}
	assert.Greater(t, rep.Proposed.ReplicaHours, rep.Current.ReplicaHours)
	assert.LessOrEqual(t, rep.Proposed.Under, rep.Current.Under)
	assert.GreaterOrEqual(t, rep.Proposed.Over, rep.Current.Over)
	assert.NotEmpty(t, rep.Notes)

	assert.Equal(t, before, f.counts(t), "a preview writes no prediction, recommendation, training record, evaluation or score")

	again, err := f.svc.PreviewChange(ctx, f.module.ID, 24, PreviewChange{HeadroomRatio: f64(1.6)})
	require.NoError(t, err)
	rep.ElapsedMs, again.ElapsedMs = 0, 0
	assert.Equal(t, rep, again, "the same input gives the same answer")

	var saved model.ForecastModule
	require.NoError(t, f.db.First(&saved, f.module.ID).Error)
	assert.Equal(t, f.module.HeadroomRatio, saved.HeadroomRatio, "the saved module is not changed")
}

// A different algorithm is tried on the same history; the saved algorithm is left as it was.
func TestAPreviewCanTryAnotherAlgorithmWithItsOwnParameters(t *testing.T) {
	f := previewFixture(t)
	ctx := context.Background()
	rep, err := f.svc.PreviewChange(ctx, f.module.ID, 24, PreviewChange{Algorithm: "ewma", Params: map[string]any{"alpha": 0.9}})
	require.NoError(t, err)
	assert.Equal(t, "naive", rep.Current.Algorithm)
	assert.Equal(t, "ewma", rep.Proposed.Algorithm)

	cfg, err := f.svc.Algorithms.Get(ctx, "ewma")
	require.NoError(t, err)
	assert.False(t, cfg.Enabled, "trying it did not enable it")
}

// What cannot be replayed is refused before anything is replayed, with the reason.
func TestAPreviewRefusesWhatItCannotReplay(t *testing.T) {
	f := previewFixture(t)
	ctx := context.Background()
	cases := map[string]PreviewChange{
		"empty":             {},
		"parameter range":   {Algorithm: "ewma", Params: map[string]any{"alpha": 5}},
		"unknown parameter": {Algorithm: "ewma", Params: map[string]any{"nope": 1}},
		"unknown algorithm": {Algorithm: "no_such"},
		"bad headroom":      {HeadroomRatio: f64(0.2)},
		"negative short":    {ShortTermRecentMinutes: f64(-1)},
	}
	for name, ch := range cases {
		_, err := f.svc.PreviewChange(ctx, f.module.ID, 24, ch)
		assert.ErrorIs(t, err, ErrPreviewInvalid, name)
	}
	_, err := f.svc.PreviewChange(ctx, f.module.ID, 24*30, PreviewChange{HeadroomRatio: f64(1.2)})
	assert.ErrorIs(t, err, ErrPreviewInvalid, "longer than the most that can be replayed")
	_, err = f.svc.PreviewChange(ctx, f.module.ID, 24*14, PreviewChange{HeadroomRatio: f64(1.2)})
	assert.ErrorIs(t, err, ErrPreviewTooLong, "1344 windows of 15 minutes is more than are replayed")
	_, err = f.svc.PreviewChange(ctx, 9999, 24, PreviewChange{HeadroomRatio: f64(1.2)})
	assert.Error(t, err)
}
