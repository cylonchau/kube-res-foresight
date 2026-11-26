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

// a module that kept a fixed count before the switch existed keeps it switched on, once; a switch turned off afterwards stays off
func TestSwitchOnExistingFixedReplicasRunsOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{PredictEnabled: true}, &model.SysConfig{}))
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })

	pinned := &model.ForecastModule{Name: "pinned", Namespace: "p", Workload: "a", FixedReplicas: 3}
	free := &model.ForecastModule{Name: "free", Namespace: "p", Workload: "b"}
	require.NoError(t, db.Omit("ID").Create(pinned).Error)
	require.NoError(t, db.Omit("ID").Create(free).Error)

	require.NoError(t, switchOnExistingFixedReplicas())
	byName := func(name string) model.ForecastModule {
		var m model.ForecastModule // a fresh value each time: a filled primary key would join the condition
		require.NoError(t, db.Where("name = ?", name).First(&m).Error)
		return m
	}
	got := byName("pinned")
	assert.True(t, got.FixedReplicasEnabled, "the fixed count it had keeps holding its replicas")
	assert.Equal(t, 3, got.ActiveFixedReplicas())
	got = byName("free")
	assert.False(t, got.FixedReplicasEnabled)
	assert.Equal(t, 0, got.ActiveFixedReplicas())

	// the administrator turns the switch off; the next start must not turn it on again
	require.NoError(t, db.Model(&model.ForecastModule{}).Where("name = ?", "pinned").Update("fixed_replicas_enabled", false).Error)
	require.NoError(t, switchOnExistingFixedReplicas(), "it can run again")
	got = byName("pinned")
	assert.False(t, got.FixedReplicasEnabled)
	assert.Equal(t, 3, got.FixedReplicas, "the count is kept")
	assert.Equal(t, 0, got.ActiveFixedReplicas(), "and takes no part while the switch is off")
}
