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
