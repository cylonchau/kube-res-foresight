package migration

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestPlatformCleanupPreservesGenericTasksAndAuthorization(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Exec("CREATE TABLE market_kline (id INTEGER PRIMARY KEY)").Error)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())
	parentPermission := model.Permission{}
	require.NoError(t, db.Where("code = ?", "system").First(&parentPermission).Error)
	custom := model.Permission{Code: "custom:report:run", Type: "BUTTON", ParentID: parentPermission.ID, Status: true}
	retired := model.Permission{Code: "market", Type: "DIRECTORY", Status: true}
	require.NoError(t, db.Create(&custom).Error)
	require.NoError(t, db.Create(&retired).Error)
	require.NoError(t, db.Create(&model.TaskDefinition{TaskType: "custom.report", CronExpr: "@every 1m"}).Error)
	require.NoError(t, db.Create(&model.TaskDefinition{TaskType: "signal.composite", CronExpr: "@every 1m"}).Error)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "custom.setting", ConfigValue: "kept"}).Error)
	for range 2 {
		require.NoError(t, cleanupRetiredModules(db))
		require.NoError(t, seedPlatformPermissions())
	}
	require.False(t, db.Migrator().HasTable("market_kline"))
	var roots []model.Permission
	require.NoError(t, db.Where("parent_id = 0 AND visible = ? AND type <> ?", true, "BUTTON").Find(&roots).Error)
	require.Len(t, roots, 9)
	for _, root := range roots {
		require.True(t, model.IsPlatformRoute(root.Code))
	}
	var count int64
	require.NoError(t, db.Model(&model.Permission{}).Where("id = ?", custom.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.TaskDefinition{}).Where("task_type = ?", "custom.report").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.TaskDefinition{}).Where("task_type = ?", "signal.composite").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", "custom.setting").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestSeedPlatformPermissionsIncludesDatasourceMenus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())
	var parent model.Permission
	require.NoError(t, db.Where("code = ?", "datasource").First(&parent).Error)
	for _, code := range []string{"datasource_connections", "datasource_metric-mappings"} {
		var menu model.Permission
		require.NoError(t, db.Where("code = ?", code).First(&menu).Error)
		require.Equal(t, parent.ID, menu.ParentID)
		require.True(t, model.IsPlatformRoute(code))
	}
}

func TestSeedPlatformPermissionsOrdersAuthorizationLastAndGivesEveryMenuAnIcon(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())

	var menus []model.Permission
	require.NoError(t, db.Where("type <> ? AND visible = ?", "BUTTON", true).Find(&menus).Error)
	require.NotEmpty(t, menus)
	for _, menu := range menus {
		require.NotEmpty(t, menu.Icon, "menu %s has no icon", menu.Code)
	}

	var roots []model.Permission
	require.NoError(t, db.Where("parent_id = 0 AND visible = ? AND type <> ?", true, "BUTTON").Order("sort asc").Find(&roots).Error)
	require.Equal(t, "authorization", roots[len(roots)-1].Code, "the authorization menu is listed last")
}

func TestEnsureForecastTasksIsIdempotentAndKeepsAdminChanges(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, ensureForecastTasks())
	require.NoError(t, db.Model(&model.TaskDefinition{}).Where("task_type = ?", "forecast.run").Updates(map[string]any{"cron_expr": "@every 10m", "enabled": false}).Error)
	require.NoError(t, ensureForecastTasks())

	var tasks []model.TaskDefinition
	require.NoError(t, db.Where("task_type LIKE ?", "forecast.%").Find(&tasks).Error)
	require.Len(t, tasks, 4) // forecast.*; ldap.sync is separate
	for _, task := range tasks {
		if task.TaskType == "forecast.run" {
			require.Equal(t, "@every 10m", task.CronExpr, "an administrator's schedule is not overwritten")
			require.False(t, task.Enabled)
		}
	}
}

func TestSeedPlatformPermissionsIncludesTheSplitForecastMenus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())
	for code, parent := range map[string]string{
		"forecast-modules_modules": "forecast-modules", "monitoring_datasets": "monitoring",
		"forecast_algorithms": "forecast", "forecast_score-records": "forecast", "forecast_ranking": "forecast",
		"forecast-results_algorithm-runs": "forecast-results", "forecast-actions_admissions": "forecast-actions",
		"forecast-results_history": "forecast-results", "forecast-results_evaluations": "forecast-results",
		"forecast-results_leaderboard":     "forecast-results",
		"forecast-results_effect":          "forecast-results",
		"forecast-actions_recommendations": "forecast-actions", "forecast-actions_executions": "forecast-actions",
		"forecast-actions_fallbacks": "forecast-actions",
		"monitoring_curves":          "monitoring",
	} {
		var menu, dir model.Permission
		require.NoError(t, db.Where("code = ?", code).First(&menu).Error, code)
		require.NoError(t, db.Where("code = ?", parent).First(&dir).Error)
		require.Equal(t, dir.ID, menu.ParentID, code)
		require.NotEmpty(t, menu.Icon, code)
		require.True(t, model.IsPlatformRoute(code), code)
	}
	for _, code := range []string{"forecast_history", "forecast_evaluation", "forecast_recommendations", "forecast_metrics", "authorization_ldap", "forecast_modules", "forecast_algorithm-templates", "forecast_input-sets", "forecast_catalog", "forecast_sources", "forecast-modules_bindings"} {
		var n int64
		require.NoError(t, db.Model(&model.Permission{}).Where("code = ?", code).Count(&n).Error)
		require.Zero(t, n, "the retired menu %s is not seeded", code)
		require.False(t, model.IsPlatformRoute(code), code)
	}
}

// An upgrade must not take a page away from a role: the grants of a split menu move to its successors.
func TestUpgradeCarriesMenuGrantsToTheSplitMenusAndRemovesTheRetiredOnes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })

	// an installation from before the split: the old menus under "forecast", a custom role holding one of them
	forecast := model.Permission{Name: "Forecast", Code: "forecast", Type: "DIRECTORY", Status: true}
	require.NoError(t, db.Create(&forecast).Error)
	oldEval := model.Permission{Name: "Backtests", Code: "forecast_evaluation", Type: "MENU", ParentID: forecast.ID, Status: true}
	oldRec := model.Permission{Name: "Recs", Code: "forecast_recommendations", Type: "MENU", ParentID: forecast.ID, Status: true}
	oldLDAP := model.Permission{Name: "LDAP", Code: "authorization_ldap", Type: "MENU", Status: true}
	require.NoError(t, db.Create(&oldEval).Error)
	require.NoError(t, db.Create(&oldRec).Error)
	require.NoError(t, db.Create(&oldLDAP).Error)
	role := model.Role{Name: "Analyst", Code: "ANALYST", Status: true}
	require.NoError(t, db.Create(&role).Error)
	require.NoError(t, db.Model(&role).Association("Permissions").Replace([]model.Permission{oldEval}))

	require.NoError(t, seedPlatformPermissions())
	require.NoError(t, migrateMenuGrants(db))
	require.NoError(t, cleanupRetiredModules(db))
	require.NoError(t, seedPlatformPermissions())

	var granted []string
	require.NoError(t, db.Table("permissions").Joins("JOIN sys_role_permissions rp ON rp.permission_id = permissions.id").
		Where("rp.role_id = ?", role.ID).Order("permissions.code").Pluck("permissions.code", &granted).Error)
	require.Contains(t, granted, "forecast-results_evaluations")
	require.Contains(t, granted, "forecast-results_leaderboard")
	require.Contains(t, granted, "forecast-results", "the parent directory comes with its pages")
	require.NotContains(t, granted, "forecast-actions_recommendations", "a role that could not open the old recommendations page does not gain the new ones")
	for _, code := range []string{"forecast_evaluation", "forecast_recommendations", "authorization_ldap"} {
		var n int64
		require.NoError(t, db.Model(&model.Permission{}).Where("code = ?", code).Count(&n).Error)
		require.Zero(t, n, code)
	}
}

// The pages of the snapshots and of the training records are gone: the snapshot history opens from the resource recommendation page
// and the training records over the algorithm run status. Their menus and grants go, and the grants move to those pages.
func TestUpgradeRemovesTheSnapshotsAndTrainingRecordsMenus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())

	role := model.Role{Name: "Analyst", Code: "ANALYST2", Status: true}
	require.NoError(t, db.Create(&role).Error)
	snapshots := model.Permission{Name: "Recommendation Snapshots", Code: "forecast-actions_snapshots", Type: "MENU", Status: true}
	runs := model.Permission{Name: "Training Records", Code: "forecast-results_runs", Type: "MENU", Status: true}
	require.NoError(t, db.Create(&snapshots).Error)
	require.NoError(t, db.Create(&runs).Error)
	require.NoError(t, db.Model(&role).Association("Permissions").Replace([]model.Permission{snapshots, runs}))

	require.NoError(t, migrateMenuGrants(db))
	require.NoError(t, cleanupRetiredModules(db))
	require.NoError(t, cleanupRetiredModules(db), "safe to repeat")

	var left int64
	require.NoError(t, db.Model(&model.Permission{}).Where("code IN ?", []string{"forecast-actions_snapshots", "forecast-results_runs"}).Count(&left).Error)
	require.Zero(t, left)
	var granted []string
	require.NoError(t, db.Table("permissions").Joins("JOIN sys_role_permissions rp ON rp.permission_id = permissions.id").
		Where("rp.role_id = ?", role.ID).Pluck("permissions.code", &granted).Error)
	require.Contains(t, granted, "forecast-actions_recommendations")
	require.Contains(t, granted, "forecast-results_algorithm-runs")

	var rec, executions model.Permission
	require.NoError(t, splitRecommendationMenus(db))
	require.NoError(t, db.Where("code = ?", "forecast-actions_executions").First(&executions).Error)
	require.NoError(t, db.Where("code = ?", "forecast-actions_recommendations").First(&rec).Error)
	require.Less(t, rec.Sort, executions.Sort)
	require.Equal(t, "Resource Recommendations", rec.Name)
}

func TestUpgradeMovesTheLDAPConfigurationIntoTheSystemParameters(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Exec(`CREATE TABLE ldap_setting (id INTEGER PRIMARY KEY, enabled BOOLEAN, url TEXT, start_tls BOOLEAN, skip_verify BOOLEAN, ca_cert TEXT,
		bind_dn TEXT, bind_password TEXT, user_base_dn TEXT, user_filter TEXT, display_name_attr TEXT, email_attr TEXT, phone_attr TEXT, group_base_dn TEXT,
		group_filter TEXT, group_name_attr TEXT, timeout_seconds INTEGER, default_role_code TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE ldap_group_mapping (id INTEGER PRIMARY KEY, group_name TEXT, role_code TEXT)`).Error)
	encrypted, err := cryptoutils.EncryptValue("s3cret")
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO ldap_setting VALUES (1, 1, 'ldaps://ldap.example.org', 0, 0, '', 'cn=svc', ?, 'ou=people', '(uid=%s)', 'cn', 'mail', '', 'ou=groups', '(member=%s)', 'cn', 7, 'VIEWER')`, encrypted).Error)
	require.NoError(t, db.Exec(`INSERT INTO ldap_group_mapping (group_name, role_code) VALUES ('sre', 'ADMIN'), ('dev', 'VIEWER')`).Error)

	require.NoError(t, migrateLDAPToSystemParameters(db))
	row, rt, err := (&ldapauth.Service{DB: db}).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "s3cret", rt.BindPassword, "the encrypted password still decrypts")
	require.True(t, row.Enabled)
	require.Equal(t, "ldaps://ldap.example.org", row.URL)
	require.Equal(t, encrypted, row.BindPassword, "the encrypted password is copied as it is")
	require.Equal(t, 7, row.TimeoutSeconds)
	require.Equal(t, "VIEWER", row.DefaultRoleCode)
	require.Equal(t, "dev=VIEWER\nsre=ADMIN", row.GroupRoleMappings)

	// running it again does not overwrite what an administrator changed since
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", "ldap.url").Update("config_value", "ldaps://other.example.org").Error)
	require.NoError(t, migrateLDAPToSystemParameters(db))
	var url model.SysConfig
	require.NoError(t, db.Where("config_key = ?", "ldap.url").First(&url).Error)
	require.Equal(t, "ldaps://other.example.org", url.ConfigValue)

	// the legacy tables are in the retired inventory and go with the cleanup
	require.NoError(t, cleanupRetiredModules(db))
	require.False(t, db.Migrator().HasTable("ldap_setting"))
	require.False(t, db.Migrator().HasTable("ldap_group_mapping"))
}

func TestCleanupDropsRetiredWorkflowAndTelegramTablesAfterInventory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	for _, table := range []string{"workflow_def", "telegram_bindings", "telegram_bot"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (id) VALUES (1)").Error)
	}
	inventory, err := retiredInventory(db, retiredWorkflowTables)
	require.NoError(t, err)
	require.Equal(t, []RetiredTable{{"telegram_bindings", 1}, {"telegram_bot", 1}, {"workflow_def", 1}}, inventory)
	require.NoError(t, cleanupRetiredModules(db))
	for _, table := range []string{"workflow_def", "telegram_bindings", "telegram_bot"} {
		require.False(t, db.Migrator().HasTable(table), table)
	}
	require.True(t, db.Migrator().HasTable("task_definition"))
}

func TestUpgradeReplacesRemarksThatWereStoredAsGarbage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Exec("INSERT INTO secret_key (name, key_type, status, access_key_hash, encrypted_value, masked_value, remark) VALUES ('good', 'developer', 1, 'h1', 'e', 'm', '读取推荐的凭证'), ('bad', 'developer', 1, 'h2', 'e', 'm', ?)", "��� operator").Error)
	require.NoError(t, repairBrokenRemarks(db))
	require.NoError(t, repairBrokenRemarks(db), "safe to repeat")
	var good, bad model.SecretKey
	require.NoError(t, db.Where("name = ?", "good").First(&good).Error)
	require.NoError(t, db.Where("name = ?", "bad").First(&bad).Error)
	require.Equal(t, "读取推荐的凭证", good.Remark, "a valid remark is left alone")
	require.Equal(t, brokenTextNote, bad.Remark)
}

func TestUpgradeRemovesTheRetiredAlgorithmsAndWhatWasStoredForThem(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Create(&model.AlgorithmConfig{AlgorithmID: "macd_trend", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.AlgorithmConfig{AlgorithmID: "my_macd", Base: "macd_trend", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.AlgorithmConfig{AlgorithmID: "naive", Enabled: true}).Error)
	require.NoError(t, db.Exec("INSERT INTO forecast_training_run (module_id, metric, algorithm_id, impl_version, model_version, status, prediction_time) VALUES (1, 'cpu', 'macd_trend', '1', 'v', 'ok', datetime('now')), (1, 'cpu', 'naive', '1', 'v', 'ok', datetime('now'))").Error)
	require.NoError(t, removeRetiredAlgorithms(db))
	require.NoError(t, removeRetiredAlgorithms(db), "safe to repeat")

	var left []string
	require.NoError(t, db.Model(&model.AlgorithmConfig{}).Pluck("algorithm_id", &left).Error)
	require.Equal(t, []string{"naive"}, left, "the retired algorithm and the custom one built on it are gone")
	var runs []string
	require.NoError(t, db.Table("forecast_training_run").Pluck("algorithm_id", &runs).Error)
	require.Equal(t, []string{"naive"}, runs, "what was stored for it is gone, the others are kept")
}
