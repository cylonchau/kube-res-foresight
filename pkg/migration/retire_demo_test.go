package migration

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func count(t *testing.T, db *gorm.DB, table any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(table).Where(where, args...).Count(&n).Error)
	return n
}

// The upgrade removes the demo module with its data, its mappings and its datasource, and only those: a real module that shares the
// name, a real datasource and the algorithm settings stay.
func TestRetireDemoRemovesOnlyWhatTheDemoMade(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))

	demoDS := &model.Datasource{Name: retiredDemoDatasource, Type: model.DatasourceTypeDemo, BaseURL: "demo://synthetic", AuthType: "none", Enabled: true}
	realDS := &model.Datasource{Name: "prod-prom", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", Enabled: true}
	require.NoError(t, db.Create(demoDS).Error)
	require.NoError(t, db.Create(realDS).Error)
	require.NoError(t, db.Create(&model.MetricMapping{DatasourceID: demoDS.ID, Name: "cpu", Metric: "cpu", Level: "workload", Query: "q"}).Error)
	require.NoError(t, db.Create(&model.MetricMapping{DatasourceID: realDS.ID, Name: "cpu", Metric: "cpu", Level: "workload", Query: "q"}).Error)

	demo := &model.ForecastModule{Name: retiredDemoModule, Namespace: retiredDemoNamespace, WorkloadKind: "Deployment", Workload: retiredDemoWorkload, Enabled: true, Mode: model.ModeRecommend}
	real := &model.ForecastModule{Name: retiredDemoModule + "-real", Namespace: "prod", WorkloadKind: "Deployment", Workload: "shop", Enabled: true, Mode: model.ModeRecommend}
	require.NoError(t, db.Create(demo).Error)
	require.NoError(t, db.Create(real).Error)
	for _, id := range []uint{demo.ID, real.ID} {
		require.NoError(t, db.Create(&model.Recommendation{ModuleID: id, Version: 1, Mode: model.ModeRecommend}).Error)
		require.NoError(t, db.Create(&model.ScalingExecution{ModuleID: id, Namespace: "n", Scaler: "s", TargetKind: "Deployment", TargetName: "t", Action: "ScaleUp", Result: "applied"}).Error)
	}

	removed, err := retireDemo(db)
	require.NoError(t, err)
	require.Len(t, removed, 2)

	require.Zero(t, count(t, db, &model.ForecastModule{}, "id = ?", demo.ID))
	require.EqualValues(t, 1, count(t, db, &model.ForecastModule{}, "id = ?", real.ID), "a module that only shares the name is not the demo")
	require.Zero(t, count(t, db, &model.Recommendation{}, "module_id = ?", demo.ID))
	require.EqualValues(t, 1, count(t, db, &model.Recommendation{}, "module_id = ?", real.ID))
	require.Zero(t, count(t, db, &model.ScalingExecution{}, "module_id = ?", demo.ID), "the operator reports of the demo go with it")
	require.EqualValues(t, 1, count(t, db, &model.ScalingExecution{}, "module_id = ?", real.ID))
	require.Zero(t, count(t, db, &model.Datasource{}, "id = ?", demoDS.ID))
	require.Zero(t, count(t, db, &model.MetricMapping{}, "datasource_id = ?", demoDS.ID))
	require.EqualValues(t, 1, count(t, db, &model.Datasource{}, "id = ?", realDS.ID))
	require.EqualValues(t, 1, count(t, db, &model.MetricMapping{}, "datasource_id = ?", realDS.ID))

	again, err := retireDemo(db)
	require.NoError(t, err)
	require.Empty(t, again, "a second run finds nothing")
}

// A datasource that only has the demo's name but is a real Prometheus is left alone.
func TestRetireDemoLeavesADatasourceThatIsNotOfTheDemoType(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Create(&model.Datasource{Name: retiredDemoDatasource, Type: "prometheus", BaseURL: "http://p:9090", AuthType: "none", Enabled: true}).Error)
	removed, err := retireDemo(db)
	require.NoError(t, err)
	require.Empty(t, removed)
	require.EqualValues(t, 1, count(t, db, &model.Datasource{}, "name = ?", retiredDemoDatasource))
}

// The records of scaling and replaced Pods were let in by a switch; neither is an admission. The upgrade removes the switch and those
// records and leaves every real admission.
func TestRetireScaleAdmissionsRemovesOnlyTheScaleRecordsAndTheirSwitch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: retiredAdmissionScaleKey, ConfigValue: "true", ConfigName: "x"}).Error)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "forecast.decision_max_age_seconds", ConfigValue: "60", ConfigName: "y"}).Error)
	for _, origin := range []string{"deploy", "rollout", "scale", "scale", ""} {
		require.NoError(t, db.Create(&model.AdmissionDecision{ModuleID: 1, Namespace: "prod", Workload: "api", Origin: origin}).Error)
	}

	n, err := retireScaleAdmissions(db)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	require.Zero(t, count(t, db, &model.SysConfig{}, "config_key = ?", retiredAdmissionScaleKey))
	require.EqualValues(t, 1, count(t, db, &model.SysConfig{}, "config_key = ?", "forecast.decision_max_age_seconds"), "other parameters stay")
	require.EqualValues(t, 3, count(t, db, &model.AdmissionDecision{}, "origin <> ?", "scale"), "the creation, the rollout and the record without an origin stay")

	n, err = retireScaleAdmissions(db)
	require.NoError(t, err)
	require.Zero(t, n, "a second run finds nothing")
}
