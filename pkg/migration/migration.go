package migration

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/store"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/telegramagent"
	"github.com/cylonchau/kubeforecast/pkg/telegramclient"
	"gorm.io/gorm"
	"strconv"
)

type permissionSeed struct {
	permission model.Permission
	parentCode string
}

func Migrate(driver string) error {
	if err := autoMigrateSchema(store.GetInstance(), model.Models...); err != nil {
		return err
	}
	if model.DB == nil {
		return nil
	}
	if _, err := rdb.MoveMarkers(context.Background(), model.DB); err != nil {
		return err
	}
	for _, upgrade := range []func() error{migrateTelegramAgentOnce, migrateTelegramCacheOnce, migrateTelegramBodyOnce, migrateTelegramChannelsOnce} {
		if err := upgrade(); err != nil {
			return err
		}
	}
	if err := cleanupRetiredModules(model.DB); err != nil {
		return err
	}
	if err := seedPlatformPermissions(); err != nil {
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
		{permission: model.Permission{Name: "Authorization", Code: "authorization", Type: "DIRECTORY", FeRoute: "/authorization", FeComponent: "layout.base", Icon: "carbon:user-role", Sort: 10, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Users", Code: "authorization_user", Type: "MENU", FeRoute: "/authorization/user", FeComponent: "view.authorization_user", Sort: 1, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Roles", Code: "authorization_role", Type: "MENU", FeRoute: "/authorization/role", FeComponent: "view.authorization_role", Sort: 2, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Menus", Code: "authorization_menu", Type: "MENU", FeRoute: "/authorization/menu", FeComponent: "view.authorization_menu", Sort: 3, Visible: true, Status: true}, parentCode: "authorization"},
		{permission: model.Permission{Name: "Workflows", Code: "workflow", Type: "DIRECTORY", FeRoute: "/workflow", FeComponent: "layout.base", Icon: "carbon:flow", Sort: 30, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Pipelines", Code: "workflow_pipelines", Type: "MENU", FeRoute: "/workflow/pipelines", FeComponent: "view.workflow_pipelines", Sort: 1, Visible: true, Status: true}, parentCode: "workflow"},
		{permission: model.Permission{Name: "Instances", Code: "workflow_instances", Type: "MENU", FeRoute: "/workflow/instances", FeComponent: "view.workflow_instances", Sort: 2, Visible: true, Status: true}, parentCode: "workflow"},
		{permission: model.Permission{Name: "Tasks", Code: "task-center", Type: "DIRECTORY", FeRoute: "/task-center", FeComponent: "layout.base", Icon: "carbon:task", Sort: 40, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Definitions", Code: "task-center_definition", Type: "MENU", FeRoute: "/task-center/definition", FeComponent: "view.task-center_definition", Sort: 1, Visible: true, Status: true}, parentCode: "task-center"},
		{permission: model.Permission{Name: "Execution Log", Code: "task-center_audit", Type: "MENU", FeRoute: "/task-center/audit", FeComponent: "view.task-center_audit", Sort: 2, Visible: true, Status: true}, parentCode: "task-center"},
		{permission: model.Permission{Name: "System", Code: "system", Type: "DIRECTORY", FeRoute: "/system", FeComponent: "layout.base", Icon: "carbon:settings", Sort: 50, Visible: true, Status: true}},
		{permission: model.Permission{Name: "Settings", Code: "system_config", Type: "MENU", FeRoute: "/system/config", FeComponent: "view.system_config", Sort: 1, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "Operation Logs", Code: "system_operation-log", Type: "MENU", FeRoute: "/system/operation-log", FeComponent: "view.system_operation-log", Sort: 2, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "API Credentials", Code: "system_secret-keys", Type: "MENU", FeRoute: "/system/secret-keys", FeComponent: "view.system_secret-keys", Sort: 3, Visible: true, Status: true}, parentCode: "system"},
		{permission: model.Permission{Name: "Read APIs", Code: "sys:all:read", Type: "BUTTON", BeApis: `["GET /api/v1/*"]`, Status: true}},
		{permission: model.Permission{Name: "Write APIs", Code: "sys:all:write", Type: "BUTTON", BeApis: `["POST /api/v1/*","PUT /api/v1/*","DELETE /api/v1/*"]`, Status: true}},
	}
	ids := make(map[string]uint)
	for i := range seeds {
		seed := &seeds[i]
		if seed.parentCode != "" {
			seed.permission.ParentID = ids[seed.parentCode]
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
	telegramConfig := config.TelegramConfig{}
	if config.CONFIG != nil {
		telegramConfig = config.CONFIG.Telegram
	}
	configs := []model.SysConfig{
		{ConfigKey: "sys.auth.access_token_minutes", ConfigValue: "30", ConfigName: "Access Token Lifetime Minutes"},
		{ConfigKey: "sys.auth.refresh_token_days", ConfigValue: "30", ConfigName: "Refresh Token Lifetime Days"},
		{ConfigKey: "sys.operation_log.retention_days", ConfigValue: "90", ConfigName: "Operation Log Retention Days"},
		{ConfigKey: telegramagent.ConfigRedisAddr, ConfigValue: "-", ConfigName: "Telegram Forward Redis Address", Remark: "Legacy: only read once to move old forward rules onto forward channels. Configure Redis per channel under Telegram > Forward Channels."},
		{ConfigKey: telegramagent.ConfigRedisPassword, ConfigValue: "-", ConfigName: "Telegram Forward Redis Password"},
		{ConfigKey: telegramagent.ConfigRedisDB, ConfigValue: "-", ConfigName: "Telegram Forward Redis DB"},
		{ConfigKey: telegramagent.ConfigKafkaBrokers, ConfigValue: "-", ConfigName: "Telegram Forward Kafka Brokers", Remark: "Legacy: only read once to move old forward rules onto forward channels. Configure Kafka per channel under Telegram > Forward Channels."},
		{ConfigKey: telegramagent.ConfigKafkaUsername, ConfigValue: "-", ConfigName: "Telegram Forward Kafka Username"},
		{ConfigKey: telegramagent.ConfigKafkaPassword, ConfigValue: "-", ConfigName: "Telegram Forward Kafka Password"},
		{ConfigKey: telegramagent.ConfigKafkaTLS, ConfigValue: "false", ConfigName: "Telegram Forward Kafka TLS"},
		{ConfigKey: telegramclient.ConfigCatchUpMinBacklog, ConfigValue: "2", ConfigName: "Telegram Catch-up Minimum Backlog", Remark: "After a stop, a backlog of at least this many messages is forwarded one by one with a delay instead of all at once."},
		{ConfigKey: telegramclient.ConfigCatchUpDelay, ConfigValue: "30", ConfigName: "Telegram Catch-up Delay Seconds", Remark: "Base wait before each message of a backlog is forwarded."},
		{ConfigKey: telegramclient.ConfigCatchUpJitter, ConfigValue: "30", ConfigName: "Telegram Catch-up Jitter Seconds", Remark: "A random 0..this many seconds are added to each message's wait."},
		{ConfigKey: telegramagent.ConfigCacheTTLHours, ConfigValue: "24", ConfigName: "Telegram Message Cache TTL Hours", Remark: "Cached Telegram messages are deleted after consumption, and at the latest after this many hours."},
		{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "5", ConfigName: "Default Visible Table Columns"},
		{ConfigKey: telegram.ConfigBotEnabled, ConfigValue: strconv.FormatBool(telegramConfig.Enabled), ConfigName: "Telegram Bot Enabled"},
		{ConfigKey: telegram.ConfigBotToken, ConfigValue: telegramConfig.BotToken, ConfigName: "Telegram Bot Token"},
		{ConfigKey: telegram.ConfigBotUsername, ConfigValue: telegramConfig.BotUsername, ConfigName: "Telegram Bot Username"},
		{ConfigKey: telegram.ConfigBotMiniAppURL, ConfigValue: telegramConfig.MiniAppURL, ConfigName: "Telegram Mini App URL"},
		{ConfigKey: telegram.ConfigBotAPIBaseURL, ConfigValue: telegramConfig.APIBaseURL, ConfigName: "Telegram Bot API Base URL"},
	}
	for i := range configs {
		if err := model.DB.Where("config_key = ?", configs[i].ConfigKey).FirstOrCreate(&configs[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// cleanupRetiredModules is opt-in through --migration / --upgrade. It never runs on ordinary startup.
func cleanupRetiredModules(db *gorm.DB) error {
	tables := []string{"chain_address_label", "chain_cluster_member", "chain_entity", "chain_label_claim", "chain_label_source", "chain_monitor", "chain_node", "chain_token_transfer", "coin", "collector_checkpoint", "collector_endpoint", "collector_provider", "collector_task", "decision_output", "decision_run", "decision_state_history", "depth_latest", "depth_raw", "depth_snapshot", "depth_wall", "exchange_fee_rate", "exchange_flow_cursor", "exchange_flow_hour", "exchange_market", "exchange_position", "exchange_position_event", "exchange_position_hour", "exchange_position_raw", "exchange_watch", "grid_run", "indicator_signal", "indicator_value", "kill_switch_audit", "market_derivatives", "market_derivatives_history", "market_funding_rate", "market_kline", "market_kline_official", "market_liquidation", "market_order_book", "market_order_flow", "market_source", "market_ticker_snapshot", "onchain_risk_event", "onchain_risk_rule", "pm_backfill", "pm_mapping", "pm_market", "pm_resolved", "pm_snapshot", "pm_window", "risk_assessment", "risk_incident", "risk_plan", "risk_setting", "risk_venue_setting", "scanner_address_cursor", "scanner_block_checkpoint", "scanner_chain_state", "sentiment_hour", "signal", "signal_asset_state", "signal_composite", "signal_holding", "signal_regime_log", "signal_weight", "signal_weight_log", "skill", "skill_version", "strategy_lot", "strategy_plan_state", "strategy_skill_binding", "trade_alert_rule", "trade_decision", "trade_fill", "trade_intent", "trade_order", "trade_position", "trade_route_config", "trade_route_evaluation", "trade_strategy", "trading_account"}
	for _, table := range tables {
		if db.Migrator().HasTable(table) {
			if err := db.Migrator().DropTable(table); err != nil {
				return fmt.Errorf("remove retired table %s: %w", table, err)
			}
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var permissions []model.Permission
		if err := tx.Find(&permissions).Error; err != nil {
			return err
		}
		removed := make(map[uint]bool)
		for _, p := range permissions {
			if p.Type != "BUTTON" && !model.IsPlatformRoute(p.Code) {
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
				if err := tx.Delete(&p).Error; err != nil {
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
		return tx.Where("config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ? OR config_key LIKE ?", "sys.market.%", "depth.%", "scanner.%", "signal.%", "sentiment.%", "llm.%", "skills.%", "hyperliquid.%", "binance_copy.%", "okx_copy.%", "trade.%").Delete(&model.SysConfig{}).Error
	})
}
