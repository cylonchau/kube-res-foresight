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

var thinSeq int32

func thinDB(t *testing.T) (*PredictionDAO, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:thin%d?mode=memory&cache=shared", atomic.AddInt32(&thinSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Prediction{}, &model.SysConfig{}))
	return NewPredictionDAO(db), db
}

// run writes the forecast of one run: a step of a minute for "steps" steps from the minute after the run.
func run(t *testing.T, db *gorm.DB, algorithm string, at time.Time, steps int) {
	t.Helper()
	rows := make([]model.Prediction, 0, steps)
	for i := 1; i <= steps; i++ {
		rows = append(rows, model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: at, TargetTime: at.Add(time.Duration(i) * time.Minute), AlgorithmID: algorithm,
			ModelVersion: "v", QuantilePermille: 500, Value: float64(i), Unit: "cores", CreatedAt: at})
	}
	require.NoError(t, db.Create(&rows).Error)
}

func countRows(t *testing.T, db *gorm.DB, where string, args ...any) int64 {
	var n int64
	require.NoError(t, db.Model(&model.Prediction{}).Where(where, args...).Count(&n).Error)
	return n
}

// A run every 5 minutes of a 15 minute horizon puts three forecasts on every target; once a forecast is old enough only the newest made at or before
// its target stays, which is the one every read takes.
func TestTheForecastsANewerRunReplacedAreThinnedOnceTheyAreOldEnough(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 24; i++ { // two hours of runs
		run(t, db, "naive", t0.Add(time.Duration(i)*5*time.Minute), 15)
		run(t, db, "holt", t0.Add(time.Duration(i)*5*time.Minute), 15)
	}
	total := countRows(t, db, "1 = 1")
	require.EqualValues(t, 24*15*2, total)

	// nothing is as old as the cutoff: nothing goes
	n, err := dao.ThinPredictions(ctx, t0.Add(-time.Hour), time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n)

	// the first hour of runs is old enough
	cutoff := t0.Add(time.Hour)
	n, err = dao.ThinPredictions(ctx, cutoff, time.Minute)
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
	assert.EqualValues(t, total-n, countRows(t, db, "1 = 1"))

	// every target of the thinned hour keeps exactly the newest forecast made at or before it, for each algorithm
	for _, algo := range []string{"naive", "holt"} {
		var rows []model.Prediction
		require.NoError(t, db.Where("algorithm_id = ? AND created_at <= ?", algo, cutoff).Find(&rows).Error)
		seen := map[time.Time]time.Time{}
		for _, r := range rows {
			_, dup := seen[r.TargetTime]
			assert.False(t, dup, "one forecast a target")
			seen[r.TargetTime] = r.PredictionTime
		}
		// a target 7 minutes after a run: the runs at +0 (this), none newer at or before... the newest made at or before the target is the run at +5
		target := t0.Add(7 * time.Minute)
		assert.Equal(t, t0.Add(5*time.Minute), seen[target])
	}
	// what is newer than the cutoff is untouched
	assert.EqualValues(t, 11*15*2, countRows(t, db, "created_at > ?", cutoff))

	// the second pass goes on from the watermark and finds nothing more to do for the same cutoff
	n, err = dao.ThinPredictions(ctx, cutoff, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n)
	// and a later cutoff thins the rest
	n, err = dao.ThinPredictions(ctx, t0.Add(3*time.Hour), time.Minute)
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
}

// The newest run is not replaced by anything, a forecast made after its target is never replaced, and different algorithms do not replace each other.
func TestThinningKeepsTheNewestHindsightAndOtherAlgorithms(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	run(t, db, "naive", t0, 15)
	run(t, db, "holt", t0.Add(time.Minute), 15) // another algorithm, newer: replaces nothing of naive
	// a forecast made after its target (the fitted value of the past): a newer run cannot replace it, it is not "at or before"
	require.NoError(t, db.Create(&model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: t0.Add(30 * time.Minute), TargetTime: t0.Add(10 * time.Minute),
		AlgorithmID: "naive", ModelVersion: "v", QuantilePermille: 500, Value: 9, Unit: "cores", CreatedAt: t0.Add(30 * time.Minute)}).Error)
	n, err := dao.ThinPredictions(ctx, t0.Add(48*time.Hour), time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.EqualValues(t, 31, countRows(t, db, "1 = 1"))

	// now naive makes a newer run: the first one loses the targets they share (the watermark is back at the start: this run is older than the one before)
	require.NoError(t, db.Unscoped().Where("config_key = ?", thinWatermarkKey).Delete(&model.SysConfig{}).Error)
	run(t, db, "naive", t0.Add(5*time.Minute), 15)
	n, err = dao.ThinPredictions(ctx, t0.Add(48*time.Hour), time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 10, n, "targets +6 to +15 of the first run are replaced by the run at +5")
}

// A run that stops at its budget leaves a watermark and the next run carries on.
func TestThinningStopsAtItsBudgetAndCarriesOn(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 48; i++ {
		run(t, db, "naive", t0.Add(time.Duration(i)*5*time.Minute), 15)
	}
	before := countRows(t, db, "1 = 1")
	// a budget that is spent at once: one slice at most
	n1, err := dao.ThinPredictions(ctx, t0.Add(48*time.Hour), time.Nanosecond)
	require.NoError(t, err)
	mid, ok := dao.thinWatermark(ctx)
	require.True(t, ok)
	assert.True(t, mid.Before(t0.Add(48*time.Hour)), "it stopped before the cutoff")
	n2, err := dao.ThinPredictions(ctx, t0.Add(48*time.Hour), time.Minute)
	require.NoError(t, err)
	assert.Greater(t, n2, int64(0))
	assert.EqualValues(t, before-n1-n2, countRows(t, db, "1 = 1"))
	// every target has one forecast
	var dup int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM (SELECT target_time FROM forecast_prediction GROUP BY target_time HAVING COUNT(*) > 1) d").Scan(&dup).Error)
	assert.Zero(t, dup)
}

// What the history chart reads is the same before and after the thinning: the newest forecast made at or before each target.
func TestTheChartReadsTheSameForecastsBeforeAndAfterThinning(t *testing.T) {
	dao, db := thinDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 36; i++ {
		run(t, db, "naive", t0.Add(time.Duration(i)*5*time.Minute), 15)
	}
	read := func() map[time.Time]time.Time {
		rows, err := dao.ForCompare(ctx, 1, "cpu", t0, t0.Add(4*time.Hour), []string{"naive"})
		require.NoError(t, err)
		out := map[time.Time]time.Time{}
		for _, r := range rows {
			out[r.TargetTime] = r.PredictionTime
		}
		return out
	}
	before := read()
	n, err := dao.ThinPredictions(ctx, t0.Add(48*time.Hour), time.Minute)
	require.NoError(t, err)
	require.Greater(t, n, int64(0))
	after := read()
	assert.Equal(t, before, after)
	assert.Greater(t, len(after), 100)
}
