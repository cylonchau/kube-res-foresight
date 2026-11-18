package rdb

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var boundedSeq int32

func boundedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:bounded%d?mode=memory&cache=shared", atomic.AddInt32(&boundedSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Prediction{}, &model.ScalingExecution{}))
	return db
}

func predictions(t *testing.T, db *gorm.DB, n int, at time.Time) {
	t.Helper()
	rows := make([]model.Prediction, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: at, TargetTime: at.Add(time.Duration(i) * time.Minute), AlgorithmID: "naive",
			ModelVersion: "v", QuantilePermille: 500, Unit: "cores", CreatedAt: at})
	}
	require.NoError(t, db.CreateInBatches(rows, 100).Error)
}

// A count stops at its limit: the database is not asked to count a table of hundreds of millions of rows for a number the list shows as "at least".
func TestACountStopsAtItsLimit(t *testing.T) {
	db := boundedDB(t)
	predictions(t, db, 25, time.Now().UTC())
	q := db.Model(&model.Prediction{}).Where("module_id = ?", 1)

	n, err := boundedCountTo(q, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 10, n, "more rows than the limit: the limit, meaning at least that many")
	n, err = boundedCountTo(q, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 25, n, "fewer rows than the limit: the exact count")
	n, err = boundedCountTo(db.Model(&model.Prediction{}).Where("module_id = ?", 99), 10)
	require.NoError(t, err)
	assert.Zero(t, n)

	// the lists use it and still page correctly
	dao := NewPredictionDAO(db)
	list, total, err := dao.ListPredictions(context.Background(), PredictionFilter{ModuleID: 1}, 2, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 25, total)
	assert.Len(t, list, 10)
	_, total, err = dao.SearchExecutions(context.Background(), ExecutionFilter{}, 1, 10)
	require.NoError(t, err)
	assert.Zero(t, total)
}

// Old rows are deleted a batch at a time, and a budget that runs out leaves the rest for the next run instead of one long delete.
func TestOldForecastsAreDeletedInBatchesWithinABudget(t *testing.T) {
	db := boundedDB(t)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	predictions(t, db, 25, now.AddDate(0, 0, -40))
	predictions(t, db, 3, now.AddDate(0, 0, -1))
	where, args := "created_at < ?", []any{now.AddDate(0, 0, -30)}
	ctx := context.Background()

	// a clock that moves a second with every reading, and a budget of one: one batch is done and the rest stays
	tick := time.Unix(0, 0)
	batchClock = func() time.Time { tick = tick.Add(time.Second); return tick }
	defer func() { batchClock = time.Now }()
	n, err := deleteInBatches(ctx, db, &model.Prediction{}, where, args, 10, 500*time.Millisecond)
	require.NoError(t, err)
	assert.EqualValues(t, 10, n)
	var left int64
	require.NoError(t, db.Model(&model.Prediction{}).Count(&left).Error)
	assert.EqualValues(t, 18, left)

	// the next run, with room, takes the rest and nothing newer
	batchClock = time.Now
	n, err = deleteInBatches(ctx, db, &model.Prediction{}, where, args, 10, time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 15, n)
	require.NoError(t, db.Model(&model.Prediction{}).Count(&left).Error)
	assert.EqualValues(t, 3, left, "the forecasts inside the retention stay")

	n, err = deleteInBatches(ctx, db, &model.Prediction{}, where, args, 10, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to delete is not an error")
}
