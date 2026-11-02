package migration

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var predictSwitchDB int32

// A module made before the prediction switch existed keeps predicting after the upgrade; one made after it starts with the prediction off.
func TestExistingModulesKeepPredictingWhenThePredictionSwitchIsAdded(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:predict_switch_%d?mode=memory&cache=shared", atomic.AddInt32(&predictSwitchDB, 1))),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// the table as it was: every column but the prediction ones
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}))
	for _, m := range []*model.ForecastModule{{Name: "on", Namespace: "n", Workload: "a", Enabled: true}, {Name: "off", Namespace: "n", Workload: "b"}} {
		require.NoError(t, m.Validate())
		require.NoError(t, db.Create(m).Error)
	}
	for _, column := range []string{"predict_enabled", "predict_mode", "predict_algorithm"} {
		require.NoError(t, db.Exec("ALTER TABLE forecast_module DROP COLUMN "+column).Error)
	}
	require.True(t, predictSwitchMissing(db))

	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}))
	require.NoError(t, keepPredictionOfExistingModules(db))
	require.False(t, predictSwitchMissing(db), "the next upgrade does not do it again")

	var modules []model.ForecastModule
	require.NoError(t, db.Order("id").Find(&modules).Error)
	require.Len(t, modules, 2)
	assert.True(t, modules[0].PredictEnabled, "an enabled module keeps predicting")
	assert.Equal(t, model.PredictDynamic, modules[0].PredictMode)
	assert.False(t, modules[1].PredictEnabled, "a disabled one stays off")

	fresh := &model.ForecastModule{Name: "new", Namespace: "n", Workload: "c", Enabled: true}
	require.NoError(t, fresh.Validate())
	require.NoError(t, db.Create(fresh).Error)
	var stored model.ForecastModule
	require.NoError(t, db.First(&stored, fresh.ID).Error)
	assert.False(t, stored.PredictEnabled, "a module made after the switch starts with the prediction off")
	assert.False(t, stored.OOMWeightEnabled, "and with the OOM weight off")
}
