package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// An evaluation scores every algorithm at one time: the cap counts evaluations, so no evaluation is cut in half.
func TestAModuleKeepsOnlyItsNewestScoreEvaluations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:scoreprune?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlgorithmScore{}))
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for module := uint(1); module <= 2; module++ {
		for i := 0; i < 10; i++ {
			for _, algorithm := range []string{"naive", "ewma"} {
				require.NoError(t, db.Create(&model.AlgorithmScore{ModuleID: module, Algorithm: algorithm, AlgorithmID: algorithm, Score: 50,
					Windows: 3, CreatedAt: start.Add(time.Duration(i) * time.Hour)}).Error)
			}
		}
	}
	dao := NewPredictionDAO(db)
	removed, err := dao.PruneScores(context.Background(), 1, 4)
	require.NoError(t, err)
	assert.EqualValues(t, 12, removed, "6 old evaluations of 2 algorithms")

	var left []model.AlgorithmScore
	require.NoError(t, db.Where("module_id = 1").Find(&left).Error)
	assert.Len(t, left, 8, "4 evaluations of 2 algorithms")
	oldest := left[0].CreatedAt
	for _, r := range left {
		if r.CreatedAt.Before(oldest) {
			oldest = r.CreatedAt
		}
	}
	assert.True(t, oldest.Equal(start.Add(6*time.Hour)), "the newest four are 6h..9h")
	var other int64
	require.NoError(t, db.Model(&model.AlgorithmScore{}).Where("module_id = 2").Count(&other).Error)
	assert.EqualValues(t, 20, other, "another module is not touched")
}

// The module list shows the configuration a workload runs with now: what the newest snapshot sets, a resource it leaves out stays
// empty (null, not 0), and the replica count the operator reported last.
func TestTheModuleSummaryCarriesTheCurrentConfiguration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:modulesummary?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Recommendation{}, &model.ModuleRunState{}, &model.ScalingExecution{}))
	now := time.Now()
	require.NoError(t, db.Create(&model.Recommendation{ModuleID: 1, Version: 1, Mode: "recommend", CPUSet: true, CPUMilli: 100, CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.Recommendation{ModuleID: 1, Version: 2, Mode: "recommend", CPUSet: true, CPUMilli: 500, MemorySet: true,
		MemoryBytes: 256 << 20, ReplicasSet: true, Replicas: 4, CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.ScalingExecution{ModuleID: 1, Namespace: "n", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Current: 2, Ready: 2, Desired: 4, Action: "up", Result: "applied"}).Error)
	require.NoError(t, db.Create(&model.ScalingExecution{ModuleID: 1, Namespace: "n", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Current: 3, Ready: 2, Desired: 4, Action: "up", Result: "applied"}).Error)

	got, err := NewPredictionDAO(db).ModuleSummaries(context.Background(), []*model.ForecastModule{{ID: 1}, {ID: 2}})
	require.NoError(t, err)
	s := got[1]
	require.NotNil(t, s.CPUMilli)
	assert.EqualValues(t, 500, *s.CPUMilli, "the newest snapshot, not the first")
	require.NotNil(t, s.MemoryBytes)
	assert.EqualValues(t, 256<<20, *s.MemoryBytes)
	require.NotNil(t, s.Replicas)
	assert.Equal(t, 4, *s.Replicas)
	assert.Nil(t, s.GPU, "a resource the snapshot does not set is empty, not 0")
	require.NotNil(t, s.ActualReplicas)
	assert.Equal(t, 3, *s.ActualReplicas, "the operator's last report")
	assert.Equal(t, 2, *s.ReadyReplicas)

	none := got[2]
	assert.Nil(t, none.CPUMilli)
	assert.Nil(t, none.ActualReplicas)
}

func TestMeasuredEvaluationsAreCappedPerModuleKeepingTheNewest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:evalprune?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PredictionEvaluation{}))
	dao := NewPredictionDAO(db)
	for module := uint(1); module <= 2; module++ {
		for i := 0; i < 130; i++ {
			require.NoError(t, db.Create(&model.PredictionEvaluation{ModuleID: module, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v",
				PredictionTime: time.Unix(int64(1000+i), 0).UTC(), QuantilePermille: 500, Points: 1, Expected: 1}).Error)
		}
	}
	removed, err := dao.Prune(context.Background(), time.Now().UTC(), Retention{EvaluationsPerModule: 100})
	require.NoError(t, err)
	assert.EqualValues(t, 60, removed["evaluations"])
	var left []model.PredictionEvaluation
	require.NoError(t, db.Where("module_id = ?", 1).Order("id ASC").Find(&left).Error)
	require.Len(t, left, 100)
	assert.Equal(t, time.Unix(1030, 0).UTC(), left[0].PredictionTime.UTC(), "the oldest 30 went")
}
