package migration

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// instanceShapeDB builds the tables as the version with stand-alone algorithm instances had them: the metric sources
// belong to an instance, a binding ties an instance to a module, and the history is filed under the instance.
func instanceShapeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	// the module table of that version: the current one without what moved onto the module since
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}))
	for _, column := range []string{"service", "container", "lookback_hours", "horizon_minutes", "max_fill_steps", "fallback_min_history_ratio", "max_missing_ratio"} {
		require.NoError(t, db.Exec("ALTER TABLE forecast_module DROP COLUMN "+column).Error, column)
	}
	for _, ddl := range []string{
		"CREATE TABLE algorithm_instance (id integer PRIMARY KEY AUTOINCREMENT, created_at datetime, updated_at datetime, name varchar(40) NOT NULL, description varchar(255), enabled numeric NOT NULL, algorithm_id varchar(40) NOT NULL, params text, namespace varchar(253), workload varchar(253), service varchar(253), container varchar(253), lookback_hours integer NOT NULL DEFAULT 720, horizon_minutes integer NOT NULL DEFAULT 15, max_fill_steps integer NOT NULL DEFAULT 3, train_budget_seconds integer NOT NULL DEFAULT 0, fallback_min_history_ratio real NOT NULL DEFAULT 0.8, max_missing_ratio real NOT NULL DEFAULT 0.3, promote_min_points integer NOT NULL DEFAULT 0, promote_min_improvement real NOT NULL DEFAULT 0, promote_peak_tolerance real NOT NULL DEFAULT 0)",
		"CREATE TABLE module_binding (id integer PRIMARY KEY AUTOINCREMENT, created_at datetime, updated_at datetime, module_id integer NOT NULL, instance_id integer NOT NULL, priority integer NOT NULL DEFAULT 0, weight real NOT NULL DEFAULT 1, enabled numeric NOT NULL)",
		"CREATE TABLE metric_source (id integer PRIMARY KEY AUTOINCREMENT, created_at datetime, updated_at datetime, instance_id integer NOT NULL, key varchar(20) NOT NULL, name varchar(100) NOT NULL, kind varchar(24) NOT NULL, resource varchar(16) NOT NULL, mapping_id integer NOT NULL DEFAULT 0, priority integer NOT NULL DEFAULT 0, weight real NOT NULL DEFAULT 1, stat varchar(8) NOT NULL DEFAULT 'p95', basis varchar(10) NOT NULL DEFAULT 'forecast', window_hours integer NOT NULL DEFAULT 0, threshold real NOT NULL DEFAULT 0, enabled numeric NOT NULL, remark varchar(255))",
		"CREATE UNIQUE INDEX idx_metric_source_key ON metric_source(instance_id, key)",
		"CREATE UNIQUE INDEX idx_metric_source_name ON metric_source(instance_id, name)",
		"CREATE TABLE metric_series (id integer PRIMARY KEY AUTOINCREMENT, created_at datetime, instance_id integer NOT NULL, metric varchar(20) NOT NULL, mapping_id integer NOT NULL, step_seconds integer NOT NULL)",
		"CREATE TABLE metric_sample (series_id integer, ts integer, value real, PRIMARY KEY (series_id, ts))",
		"INSERT INTO metric_series (instance_id, metric, mapping_id, step_seconds) VALUES (1, 'cpu', 4, 60)",
		"INSERT INTO metric_sample (series_id, ts, value) VALUES (1, 100, 1.5)",
		// two modules
		// instance 1 (ewma) reads cpu and rps for shop; instance 2 (naive) reads cpu for web; instance 3 (holt_winters) is disabled
		"INSERT INTO algorithm_instance (name, enabled, algorithm_id, params, namespace, workload, lookback_hours, horizon_minutes) VALUES ('shop-ewma', 1, 'ewma', '{\"alpha\":0.3}', 'prod', 'shop', 72, 30)",
		"INSERT INTO algorithm_instance (name, enabled, algorithm_id, params, namespace, workload) VALUES ('web-naive', 1, 'naive', '', 'prod', 'web')",
		"INSERT INTO algorithm_instance (name, enabled, algorithm_id, params, namespace, workload) VALUES ('shop-hw', 0, 'holt_winters', '', 'prod', 'shop')",
		"INSERT INTO module_binding (module_id, instance_id, priority, weight, enabled) VALUES (1, 1, 0, 1, 1)",
		"INSERT INTO module_binding (module_id, instance_id, priority, weight, enabled) VALUES (2, 2, 0, 1, 1)",
		"INSERT INTO module_binding (module_id, instance_id, priority, weight, enabled) VALUES (1, 3, 1, 1, 1)",
		"INSERT INTO metric_source (instance_id, key, name, kind, resource, mapping_id, priority, enabled) VALUES (1, 'cpu', 'cpu', 'cpu', 'cpu', 4, 0, 1)",
		"INSERT INTO metric_source (instance_id, key, name, kind, resource, mapping_id, priority, enabled) VALUES (1, 'rps', 'rps', 'rps', 'replicas', 5, 1, 1)",
		"INSERT INTO metric_source (instance_id, key, name, kind, resource, mapping_id, priority, enabled) VALUES (2, 'cpu', 'cpu', 'cpu', 'cpu', 4, 0, 1)",
	} {
		require.NoError(t, db.Exec(ddl).Error, ddl)
	}
	for _, name := range []string{"shop", "web"} {
		m := &model.ForecastModule{Name: name, Enabled: true, Mode: model.ModeRecommend, Namespace: "prod", WorkloadKind: model.KindDeployment, Workload: name}
		require.NoError(t, m.Validate())
		require.NoError(t, db.Omit("Sources", "Service", "Container", "LookbackHours", "HorizonMinutes", "MaxFillSteps", "FallbackMinHistoryRatio", "MaxMissingRatio").Create(m).Error)
	}
	return db
}

func TestInstanceShapeBecomesModuleSourcesAndPlatformAlgorithms(t *testing.T) {
	db := instanceShapeDB(t)
	require.NoError(t, prepareForecastSchema(db))
	assert.False(t, db.Migrator().HasTable("metric_series"), "history filed under an instance is dropped; it is collected again per module")
	assert.False(t, db.Migrator().HasTable("metric_source"), "the old sources are set aside while the new table is built")
	assert.True(t, db.Migrator().HasTable(legacySourceTable))
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}, &model.AlgorithmConfig{}, &model.MetricSource{}, &model.MetricSeries{},
		&model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.PredictionEvaluation{}, &model.AlgorithmScore{}), "the new unique indexes are creatable on the old rows")
	require.NoError(t, convertLegacyForecast(db))

	var modules []model.ForecastModule
	require.NoError(t, db.Order("id").Find(&modules).Error)
	require.Len(t, modules, 2)
	assert.Equal(t, 72, modules[0].LookbackHours, "the module takes the history window of its instance")
	assert.Equal(t, 30, modules[0].HorizonMinutes)

	var sources []model.MetricSource
	require.NoError(t, db.Where("module_id = ?", modules[0].ID).Order("priority").Find(&sources).Error)
	var keys []string
	for _, s := range sources {
		keys = append(keys, s.Key)
	}
	assert.Equal(t, []string{"cpu", "rps"}, keys)
	require.NoError(t, db.Where("module_id = ?", modules[1].ID).Find(&sources).Error)
	assert.Len(t, sources, 1)

	byAlgorithm := map[string]model.AlgorithmConfig{}
	var configs []model.AlgorithmConfig
	require.NoError(t, db.Find(&configs).Error)
	for _, c := range configs {
		byAlgorithm[c.AlgorithmID] = c
	}
	assert.True(t, byAlgorithm["ewma"].Enabled, "an enabled instance enables its algorithm for every module")
	assert.JSONEq(t, `{"alpha":0.3}`, byAlgorithm["ewma"].Params)
	assert.True(t, byAlgorithm["naive"].Enabled)
	assert.False(t, byAlgorithm["holt_winters"].Enabled, "a disabled instance does not")
	assert.Greater(t, len(byAlgorithm), 5, "every catalog algorithm has its setting")

	// running the conversion again changes nothing, and the old tables stay until the cleanup retires them
	assert.False(t, db.Migrator().HasTable(legacySourceTable))
	require.NoError(t, convertLegacyForecast(db))
	var again int64
	require.NoError(t, db.Model(&model.MetricSource{}).Count(&again).Error)
	assert.EqualValues(t, 3, again)
	assert.True(t, db.Migrator().HasTable("algorithm_instance"))
	for _, table := range retiredForecastTables {
		if db.Migrator().HasTable(table) {
			require.NoError(t, db.Migrator().DropTable(table))
		}
	}
}

func TestAFreshDatabaseNeedsNoConversion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, prepareForecastSchema(db))
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}, &model.AlgorithmConfig{}, &model.MetricSource{}))
	require.NoError(t, convertLegacyForecast(db))
	require.NoError(t, ensureAlgorithmConfigs(db))
	var baseline model.AlgorithmConfig
	require.NoError(t, db.Where("algorithm_id = ?", "naive").First(&baseline).Error)
	assert.True(t, baseline.Enabled, "the baselines start enabled")
	var heavy model.AlgorithmConfig
	require.NoError(t, db.Where("algorithm_id = ?", "lstm").First(&heavy).Error)
	assert.False(t, heavy.Enabled, "the heavy algorithms wait to be enabled")
}

func TestARunThatStoppedHalfWayCanBeRepeated(t *testing.T) {
	db := instanceShapeDB(t)
	require.NoError(t, prepareForecastSchema(db))
	// the schema migration failed here the first time: the new table could not take its index names
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_metric_source_key ON "+legacySourceTable+"(instance_id, key)").Error)
	require.NoError(t, prepareForecastSchema(db), "the leftover indexes are dropped again")
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}, &model.AlgorithmConfig{}, &model.MetricSource{}, &model.MetricSeries{},
		&model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.PredictionEvaluation{}, &model.AlgorithmScore{}))
	require.NoError(t, convertLegacyForecast(db))
	var n int64
	require.NoError(t, db.Model(&model.MetricSource{}).Count(&n).Error)
	assert.EqualValues(t, 3, n)
}

// The whole upgrade, as the previous release left the database: the instance-shaped tables, its menus (one of the
// codes the new release uses again was soft-deleted by that cleanup, and Authorization hangs under System) and a
// custom role that holds one of the retired pages.
func TestUpgradeFromThePreviousReleaseRunsEndToEnd(t *testing.T) {
	db := instanceShapeDB(t)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, db.AutoMigrate(&model.Permission{}, &model.Role{}, &model.User{}))

	system := model.Permission{Name: "System", Code: "system", Type: "DIRECTORY", Status: true}
	require.NoError(t, db.Create(&system).Error)
	auth := model.Permission{Name: "Authorization", Code: "authorization", Type: "DIRECTORY", ParentID: system.ID, Status: true}
	require.NoError(t, db.Create(&auth).Error)
	forecast := model.Permission{Name: "Forecast", Code: "forecast", Type: "DIRECTORY", Status: true}
	require.NoError(t, db.Create(&forecast).Error)
	catalog := model.Permission{Name: "Catalog", Code: "forecast_catalog", Type: "MENU", ParentID: forecast.ID, Status: true}
	require.NoError(t, db.Create(&catalog).Error)
	gone := model.Permission{Name: "Datasets", Code: "monitoring_datasets", Type: "MENU", Status: true}
	require.NoError(t, db.Create(&gone).Error)
	require.NoError(t, db.Delete(&gone).Error) // soft-deleted by the earlier release
	role := model.Role{Name: "Analyst", Code: "ANALYST", Status: true}
	require.NoError(t, db.Create(&role).Error)
	scores := model.Permission{Name: "Scores", Code: "forecast_scores", Type: "MENU", ParentID: forecast.ID, Status: true}
	require.NoError(t, db.Create(&scores).Error)
	sourcesPage := model.Permission{Name: "Sources", Code: "forecast-modules_sources", Type: "MENU", Status: true}
	require.NoError(t, db.Create(&sourcesPage).Error)
	require.NoError(t, db.Model(&role).Association("Permissions").Replace([]model.Permission{catalog, scores, sourcesPage}))

	require.NoError(t, Migrate("sqlite"))
	require.NoError(t, Migrate("sqlite"), "an upgrade can be run again")

	var modules []model.ForecastModule
	require.NoError(t, db.Order("id").Find(&modules).Error)
	require.Len(t, modules, 2)
	var sources int64
	require.NoError(t, db.Model(&model.MetricSource{}).Where("module_id = ?", modules[0].ID).Count(&sources).Error)
	assert.EqualValues(t, 2, sources)
	// the sources and the modules of the previous release get the defaults of the dataset settings, which keep them as they were
	var shaped []model.MetricSource
	require.NoError(t, db.Where("module_id = ?", modules[0].ID).Find(&shaped).Error)
	for _, src := range shaped {
		assert.EqualValues(t, 1, src.Scale, src.Key)
		assert.Equal(t, 6, src.Precision, src.Key)
		assert.Equal(t, "avg", src.Aggregation, src.Key)
		assert.Equal(t, "interpolate", src.Missing, src.Key)
	}
	assert.Zero(t, modules[0].StepSeconds, "a module keeps the step of each source until one is set")
	assert.Zero(t, modules[0].RunIntervalMinutes)
	assert.Zero(t, modules[0].WindowSteps, "the whole history range stays the training input")
	assert.True(t, db.Migrator().HasTable(&model.TrainingRun{}), "the training records have their table")
	assert.True(t, db.Migrator().HasColumn(&model.AlgorithmConfig{}, "formula"), "custom algorithms can be stored")
	var ewma model.AlgorithmConfig
	require.NoError(t, db.Where("algorithm_id = ?", "ewma").First(&ewma).Error)
	assert.True(t, ewma.Enabled)

	var authorization model.Permission
	require.NoError(t, db.Where("code = ?", "authorization").First(&authorization).Error)
	assert.Zero(t, authorization.ParentID, "Authorization is a top-level menu")
	var sourcesMenu model.Permission
	require.NoError(t, db.Where("code = ?", "monitoring_datasets").First(&sourcesMenu).Error, "the code a later release uses again is live")
	var catalogRows int64
	require.NoError(t, db.Unscoped().Model(&model.Permission{}).Where("code = ?", "forecast_catalog").Count(&catalogRows).Error)
	assert.Zero(t, catalogRows, "a retired menu is removed for good, so its code can be used again")
	var granted []string
	require.NoError(t, db.Table("sys_role_permissions").Select("permissions.code").
		Joins("JOIN permissions ON permissions.id = sys_role_permissions.permission_id").Where("role_id = ?", role.ID).Scan(&granted).Error)
	assert.Contains(t, granted, "forecast_algorithms", "the role keeps a page for what it could open")
	// the page of scores was split into the records, the current ranking and the history of the ranking; the page of
	// the module sources became the Prometheus datasets
	for _, code := range []string{"forecast_score-records", "forecast_ranking", "monitoring_datasets"} {
		assert.Contains(t, granted, code, "the role that opened the old page opens "+code)
	}
	for _, code := range []string{"forecast_scores", "forecast-modules_sources"} {
		var n int64
		require.NoError(t, db.Unscoped().Model(&model.Permission{}).Where("code = ?", code).Count(&n).Error)
		assert.Zero(t, n, code+" is retired and gone")
	}
}
