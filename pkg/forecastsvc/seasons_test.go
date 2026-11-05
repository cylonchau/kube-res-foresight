package forecastsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

func selectionOf(rows []SelectionRow, algorithm string) SelectionRow {
	for _, r := range rows {
		if r.Algorithm == algorithm {
			return r
		}
	}
	return SelectionRow{}
}

// An algorithm that learns a cycle may decide only on a history that holds enough repeats of it: a daily shape learned from three days is
// not one learned from one, and a peak it foresees may be that one day's incident. The algorithm that learns none is not asked.
func TestAnAlgorithmThatLearnsACycleNeedsEnoughRepeatsOfItInTheHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 1) // one day at the 60 s step of the fixture
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	// the fixture holds three days of history
	f.setParameter(t, timing.MinSeasons.Key, "2")
	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.True(t, selectionOf(detail.Selection, "seasonal_naive").Eligible, "two days are asked of three: %s", selectionOf(detail.Selection, "seasonal_naive").Reason)

	f.setParameter(t, timing.MinSeasons.Key, "10")
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	seasonal, naive := selectionOf(detail.Selection, "seasonal_naive"), selectionOf(detail.Selection, "naive")
	assert.False(t, seasonal.Eligible)
	assert.Contains(t, seasonal.Reason, "not eligible: its cycle is 24h0m0s and the history holds")
	assert.Contains(t, seasonal.Reason, "10 are needed")
	assert.True(t, naive.Eligible, "an algorithm that learns no cycle is not asked for repeats of one")
	assert.Equal(t, "naive", rec.Algorithm, "what is left decides")
}

// The administrator's own choice is exempt: a module fixed to a seasonal algorithm follows it whatever the history holds.
func TestAFixedSeasonalAlgorithmIsExemptFromTheRepeatsOfItsCycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	f.setParameter(t, timing.MinSeasons.Key, "30")
	f.setPredict(t, true, model.PredictFixed, "seasonal_naive")
	_, err = f.predict(ctx)
	require.NoError(t, err)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "seasonal_naive", rec.Algorithm)
	assert.Empty(t, detail.Degraded)
}
