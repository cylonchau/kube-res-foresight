package rdb

import (
	"context"
	"fmt"
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
	require.NoError(t, db.AutoMigrate(&model.Prediction{}, &model.BacktestRun{}, &model.PredictionEvaluation{}, &model.Recommendation{}, &model.TrainingRun{}, &model.AlgorithmScore{}))
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

func TestAModuleKeepsOnlyItsNewestRecommendationVersionsAndTheListCanShowTheCurrentOnes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:prunecap?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Recommendation{}, &model.ForecastModule{PredictEnabled: true}))
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// module 3 was deleted: what was recommended for it is not listed
	for module := uint(1); module <= 2; module++ {
		require.NoError(t, db.Create(&model.ForecastModule{PredictEnabled: true, Name: fmt.Sprintf("m%d", module), Namespace: "ns", WorkloadKind: "Deployment", Workload: fmt.Sprintf("w%d", module)}).Error)
	}
	require.NoError(t, db.Create(&model.Recommendation{ModuleID: 3, Version: 1, Mode: "recommend", ValidUntil: now, CreatedAt: now}).Error)
	for module := uint(1); module <= 2; module++ {
		for v := 1; v <= 30; v++ {
			require.NoError(t, db.Create(&model.Recommendation{ModuleID: module, Version: v, Mode: "recommend", ValidUntil: now, CreatedAt: now.Add(-time.Duration(31-v) * time.Minute)}).Error)
		}
	}
	dao := NewPredictionDAO(db)
	current, total, err := dao.SearchRecommendations(context.Background(), RecommendationFilter{LatestOnly: true}, 1, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "one current snapshot per existing module, none for a module that is gone")
	for _, r := range current {
		assert.Equal(t, 30, r.Version)
	}
	removed, err := dao.Prune(context.Background(), now, Retention{Recommendations: 365 * 24 * time.Hour, RecommendationVersions: 20})
	require.NoError(t, err)
	assert.EqualValues(t, 20, removed["recommendations"], "10 old versions of each module go")
	_, left, err := dao.SearchRecommendations(context.Background(), RecommendationFilter{ModuleID: 1}, 1, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 20, left)
	var oldest int
	require.NoError(t, db.Model(&model.Recommendation{}).Where("module_id = 1").Select("MIN(version)").Scan(&oldest).Error)
	assert.Equal(t, 11, oldest, "the newest 20 of 30 stay")
}
