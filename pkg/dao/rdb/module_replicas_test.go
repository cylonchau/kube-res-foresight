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

// A module that is only recommended is never scaled, so no scaling report ever names its replicas; the workload the operator read from the Kubernetes API does. The list shows the
// newer of the two, and nothing for a workload the operator did not find or could not read.
func TestTheReplicasOfAModuleComeFromTheNewerOfTheScalingReportAndTheWorkloadReport(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:modreplicas?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Recommendation{}, &model.ScalingExecution{}, &model.ModuleRunState{}, &model.WorkloadConfig{}))
	now := time.Now().UTC()
	exec := func(module uint, current, ready int, at time.Time) {
		require.NoError(t, db.Create(&model.ScalingExecution{ModuleID: module, Namespace: "n", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Current: current, Ready: ready,
			Desired: current, Action: "up", Result: "applied", CreatedAt: at}).Error)
	}
	workload := func(module uint, desired, ready int, at time.Time, missing bool, failure string) {
		require.NoError(t, db.Create(&model.WorkloadConfig{ModuleID: module, Namespace: "n", Kind: "Deployment", Name: "w", Desired: desired, Ready: ready, ObservedAt: at,
			Missing: missing, Error: failure}).Error)
	}
	exec(1, 3, 3, now.Add(-time.Hour)) // an old scaling report, a newer workload report
	workload(1, 12, 11, now.Add(-time.Minute), false, "")
	workload(2, 5, 5, now.Add(-time.Minute), false, "") // only recommended: no scaling report
	workload(3, 7, 0, now.Add(-time.Minute), true, "")  // the workload was not found
	workload(4, 0, 0, now.Add(-time.Minute), false, "forbidden")
	exec(5, 8, 8, now.Add(-time.Minute)) // a newer scaling report than the workload report
	workload(5, 2, 2, now.Add(-time.Hour), false, "")

	var modules []*model.ForecastModule
	for id := uint(1); id <= 6; id++ {
		modules = append(modules, &model.ForecastModule{ID: id})
	}
	got, err := NewPredictionDAO(db).ModuleSummaries(context.Background(), modules)
	require.NoError(t, err)

	at := func(id uint) (int, int, string) {
		s := got[id]
		if s.ActualReplicas == nil {
			return -1, -1, ""
		}
		return *s.ActualReplicas, *s.ReadyReplicas, s.ActualFrom
	}
	a, r, from := at(1)
	assert.Equal(t, []any{12, 11, "inventory"}, []any{a, r, from}, "the workload report is newer")
	a, r, from = at(2)
	assert.Equal(t, []any{5, 5, "inventory"}, []any{a, r, from}, "a module that is never scaled has the replicas the operator read")
	a, _, _ = at(3)
	assert.Equal(t, -1, a, "a workload that was not found has no replicas to show")
	a, _, _ = at(4)
	assert.Equal(t, -1, a, "a workload that could not be read has none either")
	a, r, from = at(5)
	assert.Equal(t, []any{8, 8, "execution"}, []any{a, r, from}, "the scaling report is newer")
	a, _, _ = at(6)
	assert.Equal(t, -1, a, "no report at all")
}
