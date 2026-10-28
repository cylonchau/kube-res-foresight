package migration

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/store"
	"gorm.io/gorm"
	"sort"
	"strings"
)

type permissionSeed struct {
	permission model.Permission
	parentCode string
}

func Migrate(driver string) error {
	if model.DB != nil {
		if err := prepareForecastSchema(model.DB); err != nil {
			return err
		}
	}
	if err := autoMigrateSchema(store.GetInstance(), model.Models...); err != nil {
		return err
	}
	if model.DB == nil {
		return nil
	}
	if err := normalizeSQLiteTimes(model.DB); err != nil {
		return err
	}
	if err := seedUserContacts(model.DB); err != nil {
		return err
	}
	if _, err := rdb.MoveMarkers(context.Background(), model.DB); err != nil {
		return err
	}
	if err := convertLegacyForecast(model.DB); err != nil {
		return err
	}
	if err := ensureAlgorithmConfigs(model.DB); err != nil {
		return err
	}
	// the new menus exist before the retired ones are removed, so their grants can be carried over
	if err := seedPlatformPermissions(); err != nil {
		return err
	}
	if err := migrateMenuGrants(model.DB); err != nil {
		return err
	}
	if err := splitRecommendationMenus(model.DB); err != nil {
		return err
	}
	if err := removeOrphanForecastData(model.DB); err != nil {
		return err
	}
	if _, err := rdb.NewPredictionDAO(model.DB).BackfillSnapshots(context.Background()); err != nil {
		return err
	}
	if err := migrateLDAPToSystemParameters(model.DB); err != nil {
		return err
	}
	if err := cleanupRetiredModules(model.DB); err != nil {
		return err
	}
	if err := seedPlatformPermissions(); err != nil {
		return err
	}
	if err := ensureForecastTasks(); err != nil {
		return err
	}
	if err := seedDefaultUsers(); err != nil {
		return err
	}
	return EnsureDefaultConfigs()
}
func Upgrade(driver string) error { return Migrate(driver) }

func autoMigrateSchema(s store.Store, models ...interface{}) error {
	if model.DB == nil {
		return s.AutoMigrate(models...)
	}
	schemaConfig := *model.DB.Config
	schemaConfig.DisableForeignKeyConstraintWhenMigrating = true
	schemaDB := model.DB.Session(&gorm.Session{NewDB: true})
	schemaDB.Config = &schemaConfig
	if err := schemaDB.AutoMigrate(models...); err != nil {
		return err
	}
	return model.DB.AutoMigrate(models...)
}
func seedPlatformPermissions() error {
	if model.DB == nil {
		return nil
	}
	seeds := []permissionSeed{
		{permission: model.Permission{Name: "User Center", Code: "user-center", Type: "MENU", FeRoute: "/user-center", FeComponent: "view.user-center", Sort: 1, Visible: false, Status: true}},
		{permission: model.Permission{Name: "Authorization", Code: "authorization", Type: "DIRECTORY", FeRoute: "/authorization", FeComponent: "layout.base", Icon: "carbon:user-role", Sort: 60, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Users", Code: "authorization_user", Type: "MENU", FeRoute: "/authorization/user", FeComponent: "view.authorization_user", Icon: "carbon:user", Sort: 1, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Roles", Code: "authorization_role", Type: "MENU", FeRoute: "/authorization/role", FeComponent: "view.authorization_role", Icon: "carbon:group", Sort: 2, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Menus", Code: "authorization_menu", Type: "MENU", FeRoute: "/authorization/menu", FeComponent: "view.authorization_menu", Icon: "carbon:menu", Sort: 3, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Data Access", Code: "datasource", Type: "DIRECTORY", FeRoute: "/datasource", FeComponent: "layout.base", Icon: "carbon:data-base", Sort: 20, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Data Source Connections", Code: "datasource_connections", Type: "MENU", FeRoute: "/datasource/connections", FeComponent: "view.datasource_connections", Icon: "carbon:connect", Sort: 1, Visible: true, Status: true}, parentCode: "datasource"},
		{permission: model.Permission{Name: "Metric Templates", Code: "datasource_metric-mappings", Type: "MENU", FeRoute: "/datasource/metric-mappings", FeComponent: "view.datasource_metric-mappings", Icon: "carbon:data-structured", Sort: 2, Visible: true, Status: true}, parentCode: "datasource"},
		{permission: model.Permission{Name: "Built-in Metric Presets", Code: "datasource_presets", Type: "MENU", FeRoute: "/datasource/presets", FeComponent: "view.datasource_presets", Icon: "carbon:template", Sort: 3, Visible: true, Status: true}, parentCode: "datasource"},
		{permission: model.Permission{Name: "Module Management", Code: "forecast-modules", Type: "DIRECTORY", FeRoute: "/forecast-modules", FeComponent: "layout.base", Icon: "carbon:cube", Sort: 24, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Module List", Code: "forecast-modules_modules", Type: "MENU", FeRoute: "/forecast-modules/modules", FeComponent: "view.forecast-modules_modules", Icon: "carbon:cube", Sort: 1, Visible: true, Status: true}, parentCode: "forecast-modules"},
		{permission: model.Permission{Name: "Forecast Management", Code: "forecast", Type: "DIRECTORY", FeRoute: "/forecast", FeComponent: "layout.base", Icon: "carbon:forecast-lightning", Sort: 25, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Algorithm Management", Code: "forecast_algorithms", Type: "MENU", FeRoute: "/forecast/algorithms", FeComponent: "view.forecast_algorithms", Icon: "carbon:model-alt", Sort: 1, Visible: true, Status: true}, parentCode: "forecast"},
		{permission: model.Permission{Name: "Algorithm Scores", Code: "forecast_score-records", Type: "MENU", FeRoute: "/forecast/score-records", FeComponent: "view.forecast_score-records", Icon: "carbon:report", Sort: 2, Visible: true, Status: true}, parentCode: "forecast"},
		{permission: model.Permission{Name: "Current Ranking", Code: "forecast_ranking", Type: "MENU", FeRoute: "/forecast/ranking", FeComponent: "view.forecast_ranking", Icon: "carbon:trophy", Sort: 3, Visible: true, Status: true}, parentCode: "forecast"},
		{permission: model.Permission{Name: "Forecast Results", Code: "forecast-results", Type: "DIRECTORY", FeRoute: "/forecast-results", FeComponent: "layout.base", Icon: "carbon:chart-evaluation", Sort: 26, Visible: true, Status: true}},
		{permission: model.Permission{Name: "History Data", Code: "forecast-results_history", Type: "MENU", FeRoute: "/forecast-results/history", FeComponent: "view.forecast-results_history", Icon: "carbon:chart-line", Sort: 1, Visible: true, Status: true}, parentCode: "forecast-results"},
		{permission: model.Permission{Name: "Measured Accuracy Records", Code: "forecast-results_evaluations", Type: "MENU", FeRoute: "/forecast-results/evaluations", FeComponent: "view.forecast-results_evaluations", Icon: "carbon:chart-evaluation", Sort: 3, Visible: true, Status: true}, parentCode: "forecast-results"},
		{permission: model.Permission{Name: "Algorithm Leaderboard", Code: "forecast-results_leaderboard", Type: "MENU", FeRoute: "/forecast-results/leaderboard", FeComponent: "view.forecast-results_leaderboard", Icon: "carbon:trophy", Sort: 4, Visible: true, Status: true}, parentCode: "forecast-results"},
		{permission: model.Permission{Name: "Algorithm Run Status", Code: "forecast-results_algorithm-runs", Type: "MENU", FeRoute: "/forecast-results/algorithm-runs", FeComponent: "view.forecast-results_algorithm-runs", Icon: "carbon:analytics", Sort: 7, Visible: true, Status: true}, parentCode: "forecast-results"},
		{permission: model.Permission{Name: "Recommendations and Execution", Code: "forecast-actions", Type: "DIRECTORY", FeRoute: "/forecast-actions", FeComponent: "layout.base", Icon: "carbon:recommend", Sort: 27, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Resource Recommendations", Code: "forecast-actions_recommendations", Type: "MENU", FeRoute: "/forecast-actions/recommendations", FeComponent: "view.forecast-actions_recommendations", Icon: "carbon:recommend", Sort: 1, Visible: true, Status: true}, parentCode: "forecast-actions"},
		{permission: model.Permission{Name: "Scaling Execution Records", Code: "forecast-actions_executions", Type: "MENU", FeRoute: "/forecast-actions/executions", FeComponent: "view.forecast-actions_executions", Icon: "carbon:scales", Sort: 3, Visible: true, Status: true}, parentCode: "forecast-actions"},
		{permission: model.Permission{Name: "Degradation and Rollback Records", Code: "forecast-actions_fallbacks", Type: "MENU", FeRoute: "/forecast-actions/fallbacks", FeComponent: "view.forecast-actions_fallbacks", Icon: "carbon:warning-alt", Sort: 4, Visible: true, Status: true}, parentCode: "forecast-actions"},
		{permission: model.Permission{Name: "Admission Decisions", Code: "forecast-actions_admissions", Type: "MENU", FeRoute: "/forecast-actions/admissions", FeComponent: "view.forecast-actions_admissions", Icon: "carbon:application-web", Sort: 5, Visible: true, Status: true}, parentCode: "forecast-actions"},
		{permission: model.Permission{Name: "Monitoring Metrics", Code: "monitoring", Type: "DIRECTORY", FeRoute: "/monitoring", FeComponent: "layout.base", Icon: "carbon:dashboard", Sort: 28, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Metrics Dataset", Code: "monitoring_datasets", Type: "MENU", FeRoute: "/monitoring/datasets", FeComponent: "view.monitoring_datasets", Icon: "carbon:data-vis-4", Sort: 1, Visible: true, Status: true}, parentCode: "monitoring"},
		{permission: model.Permission{Name: "Metric Curves", Code: "monitoring_curves", Type: "MENU", FeRoute: "/monitoring/curves", FeComponent: "view.monitoring_curves", Icon: "carbon:chart-line-smooth", Sort: 3, Visible: true, Status: true}, parentCode: "monitoring"},
		{permission: model.Permission{Name: "Tasks", Code: "task-center", Type: "DIRECTORY", FeRoute: "/task-center", FeComponent: "layout.base", Icon: "carbon:task", Sort: 40, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Definitions", Code: "task-center_definition", Type: "MENU", FeRoute: "/task-center/definition", FeComponent: "view.task-center_definition", Icon: "carbon:task-settings", Sort: 1, Visible: true, Status: true}, parentCode: "task-center"},
		{permission: model.Permission{Name: "Execution Log", Code: "task-center_audit", Type: "MENU", FeRoute: "/task-center/audit", FeComponent: "view.task-center_audit", Icon: "carbon:document", Sort: 2, Visible: true, Status: true}, parentCode: "task-center"},
		{permission: model.Permission{Name: "System", Code: "system", Type: "DIRECTORY", FeRoute: "/system", FeComponent: "layout.base", Icon: "carbon:settings", Sort: 50, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Settings", Code: "system_config", Type: "MENU", FeRoute: "/system/config", FeComponent: "view.system_config", Icon: "carbon:settings-adjust", Sort: 1, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "Operation Logs", Code: "system_operation-log", Type: "MENU", FeRoute: "/system/operation-log", FeComponent: "view.system_operation-log", Icon: "carbon:catalog", Sort: 2, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "Login Logs", Code: "system_login-log", Type: "MENU", FeRoute: "/system/login-log", FeComponent: "view.system_login-log", Icon: "carbon:user-activity", Sort: 4, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "API Credentials", Code: "system_secret-keys", Type: "MENU", FeRoute: "/system/secret-keys", FeComponent: "view.system_secret-keys", Icon: "carbon:password", Sort: 3, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "Admission Recommendations (read)", Code: "forecast:admission:read", Type: "BUTTON", BeApis: `["GET /api/v1/admission/recommendations"]`, Status: true}},
		{permission: model.Permission{Name: "Operator Decisions (read, report)", Code: "forecast:operator:use", Type: "BUTTON", BeApis: `["GET /api/v1/operator/modules","GET /api/v1/operator/decisions","POST /api/v1/operator/reports"]`, Status: true}},
		{permission: model.Permission{Name: "Metric Query Test (read)", Code: "forecast:query:test", Type: "BUTTON", BeApis: `["POST /api/v1/metric-queries/test"]`, Status: true}},
		{permission: model.Permission{Name: "Read APIs", Code: "sys:all:read", Type: "BUTTON", BeApis: `["GET /api/v1/*"]`, Status: true}},
		{permission: model.Permission{Name: "Write APIs", Code: "sys:all:write", Type: "BUTTON", BeApis: `["POST /api/v1/*","PUT /api/v1/*","DELETE /api/v1/*"]`, Status: true}},
	}
	ids := make(map[string]uint)
	for i := range seeds {
		seed := &seeds[i]
		if seed.parentCode != "" {
			seed.permission.ParentID = ids[seed.parentCode]
		}
		// a row an earlier release soft-deleted still holds the code: bring it back instead of inserting a second one
		if err := model.DB.Unscoped().Model(&model.Permission{}).Where("code = ? AND deleted_at IS NOT NULL", seed.permission.Code).
			Update("deleted_at", nil).Error; err != nil {
			return err
		}
		var current model.Permission
		if err := model.DB.Where("code = ?", seed.permission.Code).Assign(seed.permission).FirstOrCreate(&current).Error; err != nil {
			return err
		}
		// Visible has a database default of true, so GORM substitutes true for
		// the zero-value false while inserting a new hidden route. Persist the
		// seed's intended visibility explicitly for both fresh and upgraded DBs.
		if err := model.DB.Model(&current).UpdateColumn("visible", seed.permission.Visible).Error; err != nil {
			return err
		}
		current.Visible = seed.permission.Visible
		// Assign skips zero values, so a menu that moved to the top level (parent 0) or to another parent would keep
		// its old parent in an upgraded database; the seed is the truth.
		if err := model.DB.Model(&current).UpdateColumn("parent_id", seed.permission.ParentID).Error; err != nil {
			return err
		}
		current.ParentID = seed.permission.ParentID
		ids[current.Code] = current.ID
	}

	var role model.Role
	if err := model.DB.Where("code = ?", "ADMIN").FirstOrCreate(&role, model.Role{Name: "Administrator", Code: "ADMIN", Status: true}).Error; err != nil {
		return err
	}
	var permissions []model.Permission
	if err := model.DB.Find(&permissions).Error; err != nil {
		return err
	}
	return model.DB.Model(&role).Association("Permissions").Replace(permissions)
}
func seedDefaultUsers() error {
	if model.DB == nil {
		return nil
	}
	var role model.Role
	if err := model.DB.Where("code = ?", "ADMIN").First(&role).Error; err != nil {
		return err
	}
	var user model.User
	err := model.DB.Where("username = ?", "admin").First(&user).Error
	if err == gorm.ErrRecordNotFound {
		hash, hashErr := utils.HashPassword("123456")
		if hashErr != nil {
			return hashErr
		}
		user = model.User{Username: "admin", PasswordHash: hash, NickName: "Administrator", Status: true, Roles: []model.Role{role}}
		return model.DB.Create(&user).Error
	}
	if err != nil {
		return err
	}
	return model.DB.Model(&user).Association("Roles").Replace([]model.Role{role})
}
func EnsureDefaultConfigs() error {
	if model.DB == nil {
		return nil
	}
	configs := []model.SysConfig{
		{ConfigKey: "sys.auth.access_token_minutes", ConfigValue: "30", ConfigName: "Access Token Lifetime Minutes"},
		{ConfigKey: "sys.auth.refresh_token_days", ConfigValue: "30", ConfigName: "Refresh Token Lifetime Days"},
		{ConfigKey: "forecast.allow_admission_mode", ConfigValue: "false", ConfigName: "Allow Admission Mode", Remark: "Set to true only after the Kyverno policy has been verified in the target cluster. Until then modules cannot be put in admission mode."},
		{ConfigKey: "forecast.allow_auto_mode", ConfigValue: "false", ConfigName: "Allow Auto Mode", Remark: "Set to true only after kube-resource-fcst-operator has been verified to scale and recover in the target cluster. Until then modules cannot be put in auto mode."},
		{ConfigKey: "forecast.resource_change_ratio", ConfigValue: "20", ConfigName: "Pod Resource Change Percent", Remark: "The Pod CPU and memory spec (requests) stays as it is until the forecast differs from it by more than this percent; a smaller difference does not rewrite it. Replicas are not affected: they follow the forecast every cycle."},
		{ConfigKey: "forecast.snapshot_max_per_module", ConfigValue: "200", ConfigName: "Snapshots Kept Per Module", Remark: "How many recommendation snapshots (the first, every change and one an hour at least) a module keeps; the oldest beyond it are deleted. The current recommendation is never deleted."},
		{ConfigKey: "forecast.score_max_per_module", ConfigValue: "300", ConfigName: "Algorithm Evaluations Kept Per Module", Remark: "How many algorithm evaluations (one evaluation scores every algorithm of the module at one time) a module keeps in the score records; the oldest beyond it are deleted automatically, for example 30 or 300."},
		{ConfigKey: "forecast.resource_hold_hours", ConfigValue: "24", ConfigName: "Pod Resource Hold Hours", Remark: "A decrease of the Pod CPU and memory spec waits until the spec has stood this many hours; an increase, a fixed period, a bypass and the OOM protection act at once."},
		{ConfigKey: "sys.auth.login_failure_threshold", ConfigValue: "3", ConfigName: "Login Failures Before Captcha", Remark: "Failed logins within the window after which the account and address must solve a captcha (1-100)."},
		{ConfigKey: "sys.auth.login_failure_window_minutes", ConfigValue: "15", ConfigName: "Login Failure Window Minutes", Remark: "How long failures count towards the captcha threshold (1-1440)."},
		{ConfigKey: "sys.login_log.retention_days", ConfigValue: "90", ConfigName: "Login Log Retention Days", Remark: "Login history older than this is deleted by the server every hour."},
		{ConfigKey: "sys.operation_log.retention_days", ConfigValue: "90", ConfigName: "Operation Log Retention Days"},
		{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "5", ConfigName: "Default Visible Table Columns"},
	}
	configs = append(configs, ldapauth.DefaultConfigs()...) // LDAP login is an ordinary system parameter group, off by default
	for i := range configs {
		if err := model.DB.Where("config_key = ?", configs[i].ConfigKey).FirstOrCreate(&configs[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// retiredWorkflowTables are the tables of the removed workflow engine and Telegram integration. Tables whose
// name starts with "telegram" are retired as well, which also covers the GORM default names of the binding tables.
var retiredWorkflowTables = []string{"bot_command_def", "notify_template", "workflow_def", "workflow_instance", "workflow_pending_reply"}

// RetiredTable is one table the upgrade removes, with the rows it held, printed before the removal.
type RetiredTable struct {
	Name string
	Rows int64
}

// retiredInventory lists the retired tables that exist in the database, so an administrator sees what the
// explicit upgrade is about to drop before it happens.
func retiredInventory(db *gorm.DB, tables []string) ([]RetiredTable, error) {
	existing, err := db.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	retired := make(map[string]bool, len(tables))
	for _, name := range tables {
		retired[name] = true
	}
	inventory := []RetiredTable{}
	for _, name := range existing {
		if !retired[name] && !strings.HasPrefix(name, "telegram") {
			continue
		}
		var rows int64
		if err := db.Table(name).Count(&rows).Error; err != nil {
			return nil, fmt.Errorf("count retired table %s: %w", name, err)
		}
		inventory = append(inventory, RetiredTable{Name: name, Rows: rows})
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i].Name < inventory[j].Name })
	return inventory, nil
}

// cleanupRetiredModules is opt-in through --migration / --upgrade. It never runs on ordinary startup.
func cleanupRetiredModules(db *gorm.DB) error {
	tables := []string{"chain_address_label", "chain_cluster_member", "chain_entity", "chain_label_claim", "chain_label_source", "chain_monitor", "chain_node", "chain_token_transfer", "coin", "collector_checkpoint", "collector_endpoint", "collector_provider", "collector_task", "decision_output", "decision_run", "decision_state_history", "depth_latest", "depth_raw", "depth_snapshot", "depth_wall", "exchange_fee_rate", "exchange_flow_cursor", "exchange_flow_hour", "exchange_market", "exchange_position", "exchange_position_event", "exchange_position_hour", "exchange_position_raw", "exchange_watch", "grid_run", "indicator_signal", "indicator_value", "kill_switch_audit", "market_derivatives", "market_derivatives_history", "market_funding_rate", "market_kline", "market_kline_official", "market_liquidation", "market_order_book", "market_order_flow", "market_source", "market_ticker_snapshot", "onchain_risk_event", "onchain_risk_rule", "pm_backfill", "pm_mapping", "pm_market", "pm_resolved", "pm_snapshot", "pm_window", "risk_assessment", "risk_incident", "risk_plan", "risk_setting", "risk_venue_setting", "scanner_address_cursor", "scanner_block_checkpoint", "scanner_chain_state", "sentiment_hour", "signal", "signal_asset_state", "signal_composite", "signal_holding", "signal_regime_log", "signal_weight", "signal_weight_log", "skill", "skill_version", "strategy_lot", "strategy_plan_state", "strategy_skill_binding", "trade_alert_rule", "trade_decision", "trade_fill", "trade_intent", "trade_order", "trade_position", "trade_route_config", "trade_route_evaluation", "trade_strategy", "trading_account"}
	tables = append(tables, retiredForecastTables...)
	tables = append(tables, retiredWorkflowTables...)
	tables = append(tables, legacyLDAPTables...)
	inventory, err := retiredInventory(db, tables)
	if err != nil {
		return err
	}
	// the whole inventory is printed before anything is dropped, so the operator can see what is about to go
	if len(inventory) > 0 {
		fmt.Printf("upgrade: %d retired tables will be dropped:\n", len(inventory))
		for _, table := range inventory {
			fmt.Printf("upgrade:   %s (%d rows)\n", table.Name, table.Rows)
		}
	}
	for _, table := range inventory {
		if err := db.Migrator().DropTable(table.Name); err != nil {
			return fmt.Errorf("remove retired table %s: %w", table.Name, err)
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var permissions []model.Permission
		if err := tx.Find(&permissions).Error; err != nil {
			return err
		}
		removed := make(map[uint]bool)
		for _, p := range permissions {
			if (p.Type != "BUTTON" && !model.IsPlatformRoute(p.Code)) || model.IsRetiredPermission(p.Code) {
				removed[p.ID] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, p := range permissions {
				if removed[p.ParentID] && !removed[p.ID] {
					removed[p.ID] = true
					changed = true
				}
			}
		}
		for _, p := range permissions {
			if removed[p.ID] {
				if err := tx.Exec("DELETE FROM sys_role_permissions WHERE permission_id = ?", p.ID).Error; err != nil {
					return err
				}
				// hard delete: a soft-deleted row keeps its unique code, and a menu that comes back (a retired code reused
				// by a later release) could then not be seeded
				if err := tx.Unscoped().Delete(&p).Error; err != nil {
					return err
				}
			}
		}
		retiredTasks := []string{"strategy.historical.replay", "market.hourly.aggregation", "scanner.label.import", "sentiment.tally", "signal.composite"}
		if err := tx.Where("task_type IN ?", retiredTasks).Delete(&model.TaskDefinition{}).Error; err != nil {
			return err
		}
		if err := tx.Where("job_type IN ?", retiredTasks).Delete(&model.Job{}).Error; err != nil {
			return err
		}
		return tx.Where("config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ?", "sys.market.%", "telegram.%", "depth.%", "scanner.%", "signal.%", "sentiment.%", "llm.%", "skills.%", "hyperliquid.%", "binance_copy.%", "okx_copy.%", "trade.%").Delete(&model.SysConfig{}).Error
	})
}

// ensureForecastTasks seeds the scheduled forecasting jobs. They do nothing while no module is enabled, and an
// administrator can change or disable their schedule in the task center; existing rows are never overwritten.
func ensureForecastTasks() error {
	if model.DB == nil {
		return nil
	}
	defaults := []model.TaskDefinition{
		{TaskType: "forecast.run", Description: "Collect history, predict and recommend for every enabled module", CronExpr: "@every 5m", Enabled: true},
		{TaskType: "ldap.sync", Description: "Disable accounts the LDAP directory disabled or removed", CronExpr: "@every 1h", Enabled: true},
		{TaskType: "forecast.score", Description: "Score and rank the algorithm instances of every module", CronExpr: "@every 6h", Enabled: true},
		{TaskType: "forecast.evaluate", Description: "Score finished predictions against the actuals collected since", CronExpr: "@every 1h", Enabled: true},
		{TaskType: "forecast.backtest", Description: "Backtest every enabled algorithm against the baselines", CronExpr: "@every 6h", Enabled: true},
	}
	for i := range defaults {
		if err := model.DB.Where("task_type = ?", defaults[i].TaskType).FirstOrCreate(&defaults[i]).Error; err != nil {
			return err
		}
	}
	return nil
}
