package migration

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The form wrote 0.7 into the capacity reserve coefficient of every module it saved, though it showed the field for the dynamic replicas only. The recommendation is
// now divided by the coefficient, so a figure nobody saw is cleared; a module with dynamic replicas chose its own and keeps it, and so does one with another figure.
func TestAHiddenCapacityReserveIsClearedAndAChosenOneIsKept(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{PredictEnabled: true}))
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	for _, m := range []*model.ForecastModule{
		{Name: "hidden", Namespace: "p", Workload: "a", ReplicaMode: model.ReplicaModeFixed, TargetUtilization: 0.7},
		{Name: "chosen-dynamic", Namespace: "p", Workload: "b", ReplicaMode: model.ReplicaModeDynamic, TargetUtilization: 0.7},
		{Name: "chosen-other", Namespace: "p", Workload: "c", ReplicaMode: model.ReplicaModeFixed, TargetUtilization: 0.8},
	} {
		require.NoError(t, db.Omit("ID").Create(m).Error)
	}
	require.NoError(t, resetHiddenCapacityReserve())
	got := map[string]float64{}
	var rows []model.ForecastModule
	require.NoError(t, db.Find(&rows).Error)
	for _, m := range rows {
		got[m.Name] = m.TargetUtilization
	}
	assert.Equal(t, map[string]float64{"hidden": 0, "chosen-dynamic": 0.7, "chosen-other": 0.8}, got)
	require.NoError(t, resetHiddenCapacityReserve(), "it can run again")
}

// The one Limits margin of a module becomes the two it has now (CPU and memory); the old column is emptied so the move runs once, and a module that set none keeps none.
func TestTheLimitsMarginMovesToTheCPUAndTheMemory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{PredictEnabled: true}))
	require.NoError(t, db.Exec("ALTER TABLE forecast_module ADD COLUMN rec_limit_headroom real NOT NULL DEFAULT 0").Error)
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	for _, m := range []*model.ForecastModule{
		{Name: "set", Namespace: "p", Workload: "a"},
		{Name: "set-memory-too", Namespace: "p", Workload: "b", RecMemoryLimitHeadroom: 0.5},
		{Name: "none", Namespace: "p", Workload: "c"},
	} {
		require.NoError(t, db.Omit("ID").Create(m).Error)
	}
	require.NoError(t, db.Exec("UPDATE forecast_module SET rec_limit_headroom = 0.3 WHERE name IN ('set', 'set-memory-too')").Error)
	require.NoError(t, splitLimitHeadroom())
	require.NoError(t, splitLimitHeadroom(), "it can run again")
	got := map[string][2]float64{}
	var rows []model.ForecastModule
	require.NoError(t, db.Find(&rows).Error)
	for _, m := range rows {
		got[m.Name] = [2]float64{m.RecCPULimitHeadroom, m.RecMemoryLimitHeadroom}
	}
	assert.Equal(t, map[string][2]float64{"set": {0.3, 0.3}, "set-memory-too": {0.3, 0.5}, "none": {0, 0}}, got)
	var left float64
	require.NoError(t, db.Raw("SELECT MAX(rec_limit_headroom) FROM forecast_module").Scan(&left).Error)
	assert.Zero(t, left)
}
