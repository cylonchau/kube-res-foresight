package migration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func TestPartitionNamesAndDDLAreOfTheDayInUTC(t *testing.T) {
	at := time.Date(2026, 11, 26, 23, 59, 59, 0, time.FixedZone("x", 8*3600)) // 15:59:59 UTC on the 26th
	assert.Equal(t, "forecast_prediction_p20261126", rdb.PartitionName(at))
	assert.Equal(t, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), rdb.DayStart(at))
	day, ok := rdb.PartitionDay("forecast_prediction_p20261126")
	assert.True(t, ok)
	assert.Equal(t, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), day)
	for _, name := range []string{"forecast_prediction", "forecast_prediction_legacy", "forecast_prediction_p2026", "other_p20261126"} {
		_, ok := rdb.PartitionDay(name)
		assert.False(t, ok, name)
	}
	assert.Equal(t, "CREATE TABLE IF NOT EXISTS forecast_prediction_p20261126 PARTITION OF forecast_prediction FOR VALUES FROM ('2026-11-26 00:00:00+00') TO ('2026-11-27 00:00:00+00')", rdb.PartitionDDL(at))
}

// This test needs a PostgreSQL of its own: set TEST_POSTGRES_DSN to a database it may create and drop the schema "partition_test" in, for example
// TEST_POSTGRES_DSN="host=127.0.0.1 port=5432 user=postgres dbname=scratch sslmode=disable". Without it the test is skipped.
func TestTheForecastTableBecomesPartitionedByDayAndKeepsWorking(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set: no PostgreSQL to convert a table in")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // one connection, so the search path set below holds for every statement
	for _, q := range []string{"DROP SCHEMA IF EXISTS partition_test CASCADE", "CREATE SCHEMA partition_test", "SET search_path TO partition_test"} {
		require.NoError(t, db.Exec(q).Error, q)
	}
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA IF EXISTS partition_test CASCADE").Error
		rdb.ResetPartitionState()
	})
	rdb.ResetPartitionState()
	ctx := context.Background()
	require.NoError(t, db.AutoMigrate(&model.Prediction{}, &model.SysConfig{}))

	// an ordinary table with forecasts of 40 days ago (past the retention), of 5 days ago (steps of a minute) and of the last hour
	now := time.Now().UTC().Truncate(time.Minute)
	write := func(at time.Time, steps int) {
		rows := make([]model.Prediction, 0, steps)
		for i := 1; i <= steps; i++ {
			rows = append(rows, model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: at, TargetTime: at.Add(time.Duration(i) * time.Minute), AlgorithmID: "naive",
				ModelVersion: "v", QuantilePermille: 500, Value: float64(i), Unit: "cores", CreatedAt: at})
		}
		require.NoError(t, db.Create(&rows).Error)
	}
	write(now.Add(-40*24*time.Hour), 15)
	for i := 0; i < 24; i++ { // two hours of runs five days ago
		write(now.Add(-5*24*time.Hour).Add(time.Duration(i)*5*time.Minute), 15)
	}
	write(now.Add(-time.Hour), 15)
	var before, highest int64
	require.NoError(t, db.Model(&model.Prediction{}).Count(&before).Error)
	require.NoError(t, db.Model(&model.Prediction{}).Select("MAX(id)").Scan(&highest).Error)

	require.NoError(t, partitionPredictions(db))

	var kind string
	require.NoError(t, db.Raw(`SELECT relkind::text FROM pg_class WHERE oid = to_regclass('forecast_prediction')`).Scan(&kind).Error)
	assert.Equal(t, "p", kind, "the table is partitioned now")
	assert.True(t, db.Migrator().HasTable(legacyPredictionTable), "the table it was stays, with every row")
	var legacy, kept, old int64
	require.NoError(t, db.Table(legacyPredictionTable).Count(&legacy).Error)
	assert.Equal(t, before, legacy)
	require.NoError(t, db.Model(&model.Prediction{}).Count(&kept).Error)
	require.NoError(t, db.Model(&model.Prediction{}).Where("created_at < ?", now.Add(-30*24*time.Hour)).Count(&old).Error)
	assert.Zero(t, old, "the run past the retention was not copied")
	assert.EqualValues(t, 15, countCreatedAfter(t, db, now.Add(-2*time.Hour)), "the last hour keeps every step")
	assert.Less(t, kept, before, "the runs of five days ago were reduced to the grid")
	assert.Positive(t, kept)

	days, err := rdb.NewPredictionDAO(db).Partitions(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(days), 5+rdb.PartitionsAhead, "a partition for each day from the oldest copied to a few days ahead")

	// the model and its indexes are still the ones of the migration
	require.NoError(t, db.AutoMigrate(&model.Prediction{}), "the schema migration finds the partitioned table as it expects it")
	for _, index := range []string{"idx_prediction", "idx_prediction_target", "idx_forecast_prediction_created_at"} {
		assert.True(t, db.Migrator().HasIndex(rdb.PredictionTable, index), index)
	}
	require.NoError(t, partitionPredictions(db), "a table that is partitioned is left as it is")

	// a new forecast takes the next number of the sequence the table had, and a repeated one is ignored
	dao := rdb.NewPredictionDAO(db)
	fresh := []model.Prediction{{ModuleID: 1, Metric: "cpu", PredictionTime: now, TargetTime: now.Add(time.Minute), AlgorithmID: "naive", ModelVersion: "v", QuantilePermille: 500, Value: 1, Unit: "cores"}}
	require.NoError(t, dao.SavePredictions(ctx, fresh))
	require.NoError(t, dao.SavePredictions(ctx, fresh), "the same run again is ignored")
	var newest model.Prediction
	require.NoError(t, db.Where("prediction_time = ?", now).Order("id DESC").First(&newest).Error)
	assert.Greater(t, int64(newest.ID), highest)

	// a day nobody made a partition for gets one when its forecast comes
	far := now.AddDate(0, 0, 9)
	require.NoError(t, dao.SavePredictions(ctx, []model.Prediction{{ModuleID: 1, Metric: "cpu", PredictionTime: far, TargetTime: far.Add(time.Minute), AlgorithmID: "naive", ModelVersion: "v", QuantilePermille: 500, Value: 2, Unit: "cores"}}))
	var made bool
	require.NoError(t, db.Raw(`SELECT to_regclass(?) IS NOT NULL`, rdb.PartitionName(far)).Scan(&made).Error)
	assert.True(t, made)

	// the clean-up: it makes the days ahead, drops the days past the retention, and thins and coarsens inside the partitions
	removed, err := dao.Prune(ctx, now, rdb.DefaultRetention)
	require.NoError(t, err)
	t.Logf("removed: %v", removed)
	require.NoError(t, db.Model(&model.Prediction{}).Where("created_at > ?", now.Add(-2*time.Hour)).Count(&old).Error)
	assert.Positive(t, old, "the newest forecasts are still there")
	dropped, err := dao.DropExpiredPartitions(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Positive(t, dropped, "the days before yesterday go as whole partitions")
	require.NoError(t, db.Model(&model.Prediction{}).Where("created_at < ?", now.Add(-48*time.Hour)).Count(&old).Error)
	assert.Zero(t, old)

	// the legacy table frees its space when it is dropped
	require.NoError(t, db.Exec("DROP TABLE "+legacyPredictionTable).Error)
}

func countCreatedAfter(t *testing.T, db *gorm.DB, at time.Time) int64 {
	var n int64
	require.NoError(t, db.Model(&model.Prediction{}).Where("created_at > ?", at).Count(&n).Error)
	return n
}
