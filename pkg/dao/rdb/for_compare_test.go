package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The chart reads, per algorithm and target time, the newest point forecast made at or before it. The database picks it, so
// overlapping runs do not multiply the rows read, and a forecast made after its own target (hindsight) or a quantile line never
// stands in for the point forecast.
func TestForCompareReturnsTheNewestPointForecastOfEveryTarget(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:forcompare?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Prediction{}))
	dao := NewPredictionDAO(db)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	add := func(algo string, madeMin, targetMin int, value float64, quantile int) {
		require.NoError(t, db.Create(&model.Prediction{ModuleID: 1, Metric: "cpu", AlgorithmID: algo, ModelVersion: "v1", Unit: "cores", Value: value,
			PredictionTime: t0.Add(time.Duration(madeMin) * time.Minute), TargetTime: t0.Add(time.Duration(targetMin) * time.Minute), QuantilePermille: quantile}).Error)
	}
	// forecasts every 5 minutes with a 15 minute horizon: three of them cover every target
	for made := 0; made <= 60; made += 5 {
		for target := made + 1; target <= made+15; target++ {
			add("ewma", made, target, float64(made), 500)
		}
	}
	// a forecast made after its target is hindsight; a quantile line is not the point forecast
	add("ewma", 40, 30, 999, 500)
	add("ewma", 59, 58, 888, 500)
	add("ewma", 55, 56, 777, 900)

	rows, err := dao.ForCompare(ctx, 1, "cpu", t0, t0.Add(80*time.Minute), nil)
	require.NoError(t, err)
	perTarget := map[time.Time]model.Prediction{}
	for _, r := range rows {
		_, dup := perTarget[r.TargetTime.UTC()]
		assert.False(t, dup, "one row per target: %v", r.TargetTime)
		perTarget[r.TargetTime.UTC()] = r
	}
	assert.Len(t, perTarget, 75, "targets 1..75 minutes, every one has a forecast")
	assert.Less(t, len(rows), 100, "not the ~600 rows of all the overlapping runs")
	for target, r := range perTarget {
		assert.False(t, r.PredictionTime.After(target), "never made after its target: %v", target)
		assert.EqualValues(t, 500, r.QuantilePermille)
		assert.NotEqual(t, 999.0, r.Value)
		assert.NotEqual(t, 888.0, r.Value)
		assert.NotEqual(t, 777.0, r.Value)
	}
	// the target of minute 58 was last forecast by the run made at minute 55 (the run of minute 60 comes after it)
	assert.EqualValues(t, 55, perTarget[t0.Add(58*time.Minute)].Value)

	only, err := dao.ForCompare(ctx, 1, "cpu", t0.Add(20*time.Minute), t0.Add(30*time.Minute), []string{"ewma"})
	require.NoError(t, err)
	assert.Len(t, only, 11)
	none, err := dao.ForCompare(ctx, 1, "cpu", t0, t0.Add(80*time.Minute), []string{"other"})
	require.NoError(t, err)
	assert.Empty(t, none)
}
