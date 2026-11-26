package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Past the age every step is kept for, one forecast in every fifteen minutes of target time stays for each algorithm and metric of a module; the newer forecasts are not touched,
// a second run has nothing left to do, and an algorithm never loses a stretch to another.
func TestOldForecastsAreReducedToOneInEveryStretchOfTargetTime(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 12*8; i++ { // eight hours of runs every five minutes, a fifteen minute horizon of one minute steps
		at := t0.Add(time.Duration(i) * 5 * time.Minute)
		run(t, db, "naive", at, 15)
		run(t, db, "holt", at, 15)
	}
	cutoff := t0.Add(6 * time.Hour)
	before := countRows(t, db, "1 = 1")
	newer := countRows(t, db, "created_at > ?", cutoff)
	require.EqualValues(t, 12*8*15*2, before)

	n, err := dao.CoarsenPredictions(ctx, cutoff, 15*time.Minute, time.Minute)
	require.NoError(t, err)
	assert.Positive(t, n)
	assert.EqualValues(t, before-n, countRows(t, db, "1 = 1"))
	assert.Equal(t, newer, countRows(t, db, "created_at > ?", cutoff), "what is newer than the cutoff is not touched")

	var old []model.Prediction
	require.NoError(t, db.Where("created_at <= ?", cutoff).Find(&old).Error)
	perStretch := map[string]int{}
	for _, r := range old {
		perStretch[r.AlgorithmID+"/"+r.TargetTime.Truncate(15*time.Minute).Format(time.RFC3339)]++
	}
	for key, count := range perStretch {
		assert.LessOrEqual(t, count, 2, "%s: one forecast of a stretch, two at most where a stretch lies on the edge of two slices", key)
	}
	// the six hours were 6 x 4 stretches for each of the two algorithms; the stretches of both are still there
	assert.GreaterOrEqual(t, len(perStretch), 2*6*4)
	assert.Less(t, int64(len(old)), before/2/6, "the rows of the old part fell to a small part of what they were")

	again, err := dao.CoarsenPredictions(ctx, cutoff, 15*time.Minute, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, again, "the watermark says it was done")
}

func TestCoarseningOffAndAnEmptyTableDoNothing(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	n, err := dao.CoarsenPredictions(ctx, time.Now(), 15*time.Minute, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "no forecast, nothing to do")
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	run(t, db, "naive", t0, 15)
	n, err = dao.CoarsenPredictions(ctx, t0.Add(24*time.Hour), 0, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "0 keeps every step")
	assert.EqualValues(t, 15, countRows(t, db, "1 = 1"))
}

// The clean-up that the platform runs hourly takes the coarsening along, and its policy reads the three parameters.
func TestPruneCoarsensAndKeepsOnlyTheRetentionOfForecasts(t *testing.T) {
	dao, db := thinDB(t)
	require.NoError(t, db.AutoMigrate(&model.BacktestRun{}, &model.PredictionEvaluation{}, &model.Recommendation{}, &model.TrainingRun{}, &model.AlgorithmScore{}))
	ctx := context.Background()
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	// a run 40 days ago (past the retention), one 4 days ago (coarsened), one an hour ago (kept as it is)
	run(t, db, "naive", now.Add(-40*24*time.Hour), 15)
	run(t, db, "naive", now.Add(-4*24*time.Hour), 15)
	run(t, db, "naive", now.Add(-time.Hour), 15)
	r := DefaultRetention
	r.PredictionThinAfter = 24 * time.Hour
	removed, err := dao.Prune(ctx, now, r)
	require.NoError(t, err)
	assert.EqualValues(t, 15, removed["predictions"], "the run of 40 days ago is past the 30 days")
	assert.Positive(t, removed["predictions_coarsened"], "the run of 4 days ago is reduced")
	assert.EqualValues(t, 15, countRows(t, db, "created_at > ?", now.Add(-2*time.Hour)), "the newest run keeps every step")
}
