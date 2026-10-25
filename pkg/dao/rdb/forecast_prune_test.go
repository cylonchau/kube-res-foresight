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

func TestPruneDropsOldRecordsButKeepsTheLatestRecommendationOfEveryModule(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:prune?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Prediction{}, &model.BacktestRun{}, &model.PredictionEvaluation{}, &model.Recommendation{}, &model.TrainingRun{}))
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	old, recent := now.AddDate(0, 0, -40), now.AddDate(0, 0, -2)
	create := func(v any) { require.NoError(t, db.Create(v).Error) }

	create(&model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: old, TargetTime: old, AlgorithmID: "naive", ModelVersion: "v", QuantilePermille: 500, Unit: "cores", CreatedAt: old})
	create(&model.Prediction{ModuleID: 1, Metric: "cpu", PredictionTime: recent, TargetTime: recent, AlgorithmID: "naive", ModelVersion: "v", QuantilePermille: 500, Unit: "cores", CreatedAt: recent})
	// every module's newest recommendation survives even when it is older than the retention
	create(&model.Recommendation{ModuleID: 1, Version: 1, Mode: "recommend", ValidUntil: old, CreatedAt: now.AddDate(-1, 0, 0)})
	create(&model.Recommendation{ModuleID: 1, Version: 2, Mode: "recommend", ValidUntil: old, CreatedAt: now.AddDate(0, -8, 0)})
	create(&model.Recommendation{ModuleID: 2, Version: 1, Mode: "recommend", ValidUntil: old, CreatedAt: now.AddDate(-1, 0, 0)})
	create(&model.Recommendation{ModuleID: 3, Version: 1, Mode: "recommend", ValidUntil: recent, CreatedAt: recent})
	create(&model.PredictionEvaluation{ModuleID: 1, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v", PredictionTime: old, Points: 1, CreatedAt: now.AddDate(-1, 0, 0)})
	create(&model.BacktestRun{ModuleID: 1, Metric: "cpu", AlgorithmID: "naive", StepSeconds: 60, HorizonSteps: 1, Cutoffs: 1, CreatedAt: now.AddDate(-1, 0, 0)})

	create(&model.TrainingRun{ModuleID: 1, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v", Status: "ok", PredictionTime: old, CreatedAt: old})
	create(&model.TrainingRun{ModuleID: 1, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v", Status: "ok", PredictionTime: recent, CreatedAt: recent})

	removed, err := NewPredictionDAO(db).Prune(context.Background(), now, DefaultRetention)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed["predictions"])
	assert.EqualValues(t, 1, removed["trainings"])
	assert.EqualValues(t, 1, removed["evaluations"])
	assert.EqualValues(t, 1, removed["backtests"])
	assert.EqualValues(t, 1, removed["recommendations"], "only module 1's version 1 goes: versions 2, the lone one of module 2 and the recent one stay")

	var versions []int
	require.NoError(t, db.Model(&model.Recommendation{}).Order("module_id, version").Pluck("version", &versions).Error)
	assert.Equal(t, []int{2, 1, 1}, versions)
	var predictions int64
	require.NoError(t, db.Model(&model.Prediction{}).Count(&predictions).Error)
	assert.EqualValues(t, 1, predictions)

	// a zero retention disables that kind of pruning
	removed, err = NewPredictionDAO(db).Prune(context.Background(), now.AddDate(5, 0, 0), Retention{})
	require.NoError(t, err)
	assert.Zero(t, removed["predictions"]+removed["recommendations"])
}
