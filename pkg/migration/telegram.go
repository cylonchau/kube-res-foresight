package migration

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/telegramagent"
	"gorm.io/gorm"
	"strings"
	"time"
)

const telegramAgentMigrationKey = "migration.telegram_agent.v1"
const telegramCacheMigrationKey = "migration.telegram_cache.v1"
const telegramBodyMigrationKey = "migration.telegram_body.v1"

// migrateTelegramAgentOnce moves an installation that predates the Telegram
// Agent onto the new model, once per database:
//   - the unique index on bot_command_def.command is dropped, because a
//     command is now unique per bot (bot_id, command);
//   - subscriptions created before membership was tracked are marked joined;
//   - the single bot kept in SysConfig becomes the first telegram_bot row, so
//     the Telegram Agent only ever reads the table.
func migrateTelegramAgentOnce() error {
	if model.DB == nil {
		return nil
	}
	var done int64
	if err := model.DB.Model(&model.SysConfig{}).Where("config_key = ?", telegramAgentMigrationKey).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	migrator := model.DB.Migrator()
	if migrator.HasIndex(&model.BotCommandDef{}, "idx_bot_command_def_command") {
		if err := migrator.DropIndex(&model.BotCommandDef{}, "idx_bot_command_def_command"); err != nil {
			return fmt.Errorf("drop the single-bot command index: %w", err)
		}
	}
	if err := model.DB.Model(&model.TelegramClientSubscription{}).Where("state = ?", model.TelegramSubPending).
		Update("state", model.TelegramSubJoined).Error; err != nil {
		return fmt.Errorf("mark existing subscriptions joined: %w", err)
	}
	if err := seedLegacyTelegramBot(model.DB); err != nil {
		return err
	}
	return model.DB.Create(&model.SysConfig{ConfigKey: telegramAgentMigrationKey, ConfigValue: "done", ConfigName: "Telegram Agent Migration Marker"}).Error
}

// seedLegacyTelegramBot turns the SysConfig bot into a telegram_bot row when
// no bot exists yet. A token that is no longer valid is skipped, not fatal.
func seedLegacyTelegramBot(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&model.TelegramBot{}).Count(&existing).Error; err != nil || existing > 0 {
		return err
	}
	cfg, err := telegram.LoadConfigFromSysConfig(rdb.NewSysConfigDAO(db))
	if err != nil || cfg.BotToken == "" {
		return err
	}
	name := "default"
	username := cfg.BotUsername
	if _, err := rdb.NewTelegramBotDAO(db).Create(context.Background(), rdb.TelegramBotInput{
		Name: &name, Token: &cfg.BotToken, Username: &username,
		APIBaseURL: &cfg.APIBaseURL, MiniAppURL: &cfg.MiniAppURL, Enabled: &cfg.Enabled,
	}); err != nil {
		fmt.Printf("skipped migrating the configured Telegram bot: %v\n", err)
	}
	return nil
}

// migrateTelegramCacheOnce turns the old message history into cache entries
// that are already consumed. Messages are now only a temporary cache, so the
// cleanup job deletes them shortly after the TG Agent starts, and no forward
// rule ever replays them.
func migrateTelegramCacheOnce() error {
	if model.DB == nil {
		return nil
	}
	var done int64
	if err := model.DB.Model(&model.SysConfig{}).Where("config_key = ?", telegramCacheMigrationKey).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	longAgo := time.Now().Add(-24 * time.Hour)
	if err := model.DB.Model(&model.TelegramClientMessage{}).Where("status = ? OR status = ''", model.TelegramMsgPending).
		Updates(map[string]any{"status": model.TelegramMsgConsumed, "consumed_at": &longAgo}).Error; err != nil {
		return fmt.Errorf("retire the Telegram message history: %w", err)
	}
	return model.DB.Create(&model.SysConfig{ConfigKey: telegramCacheMigrationKey, ConfigValue: "done", ConfigName: "Telegram Message Cache Migration Marker"}).Error
}

// migrateTelegramBodyOnce moves the cache to compressed bodies. Older rows kept
// the text in a plain column; they are retired history, so they are deleted and
// the column is dropped, which also frees its storage.
func migrateTelegramBodyOnce() error {
	if model.DB == nil {
		return nil
	}
	var done int64
	if err := model.DB.Model(&model.SysConfig{}).Where("config_key = ?", telegramBodyMigrationKey).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	migrator := model.DB.Migrator()
	if migrator.HasColumn("telegram_client_message", "text") {
		if err := model.DB.Where("body IS NULL").Delete(&model.TelegramClientMessage{}).Error; err != nil {
			return fmt.Errorf("remove the uncompressed Telegram messages: %w", err)
		}
		// Raw SQL: the model no longer has this column, and both SQLite (3.35+) and
		// MySQL support the statement.
		if err := model.DB.Exec("ALTER TABLE telegram_client_message DROP COLUMN `text`").Error; err != nil {
			return fmt.Errorf("drop the plain Telegram message text column: %w", err)
		}
	}
	return model.DB.Create(&model.SysConfig{ConfigKey: telegramBodyMigrationKey, ConfigValue: "done", ConfigName: "Telegram Message Body Migration Marker"}).Error
}

const telegramChannelMigrationKey = "migration.telegram_channels.v1"

// migrateTelegramChannelsOnce moves forward rules saved before forward channels existed onto channels,
// once per database. A job or workflow rule becomes a direct channel; a Redis rule becomes a Redis
// Stream channel and a Kafka rule a Kafka channel, with the connection settings that were kept in the
// system settings. Every channel gets the consumer the old rule implied: the Job module or the Workflow
// engine for the direct ones, and an outside consumer for the brokers (nothing in this system read them).
func migrateTelegramChannelsOnce() error {
	if model.DB == nil {
		return nil
	}
	var done int64
	if err := model.DB.Model(&model.SysConfig{}).Where("config_key = ?", telegramChannelMigrationKey).Count(&done).Error; err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	var rules []model.TelegramForwardRule
	if err := model.DB.Where("target_kind IN ?", []string{
		model.ForwardTargetJob, model.ForwardTargetWorkflow, model.ForwardTargetRedis, model.ForwardTargetKafka,
	}).Find(&rules).Error; err != nil {
		return err
	}
	if len(rules) > 0 {
		var platform config.RedisConfig
		if config.CONFIG != nil {
			platform = config.CONFIG.Redis
		}
		legacy, err := telegramagent.LoadChannelConfig(context.Background(), rdb.NewSysConfigDAO(model.DB), platform)
		if err != nil {
			legacy = telegramagent.ChannelConfig{}
		}
		channels := rdb.NewTelegramForwardChannelDAO(model.DB)
		byKey := map[string]int64{}
		for _, rule := range rules {
			key := rule.TargetKind + "|" + rule.TargetRef
			id, ok := byKey[key]
			if !ok {
				in := rdb.ChannelInput{TelegramForwardChannel: model.TelegramForwardChannel{
					Name: rule.TargetKind + ":" + rule.TargetRef, Enabled: true, ConsumerEnabled: true,
					Remark: "created from forward rule \"" + rule.Name + "\"",
				}}
				switch rule.TargetKind {
				case model.ForwardTargetJob:
					in.Transport, in.ConsumerKind, in.ConsumerRef = model.ChannelTransportDirect, model.ChannelConsumerJob, rule.TargetRef
				case model.ForwardTargetWorkflow:
					in.Transport, in.ConsumerKind, in.ConsumerRef = model.ChannelTransportDirect, model.ChannelConsumerWorkflow, rule.TargetRef
				case model.ForwardTargetRedis:
					in.Transport, in.ConsumerKind, in.ConsumerGroup = model.ChannelTransportRedis, model.ChannelConsumerExternal, "kubeforecast"
					in.Stream = rule.TargetRef
					in.RedisAddr, in.RedisDB, in.RedisPassword = legacy.RedisAddr, legacy.RedisDB, legacy.RedisPassword
					if platform.Addr == legacy.RedisAddr {
						in.RedisAddr, in.RedisDB, in.RedisPassword = "", 0, ""
					}
				case model.ForwardTargetKafka:
					in.Transport, in.ConsumerKind, in.ConsumerGroup = model.ChannelTransportKafka, model.ChannelConsumerExternal, "kubeforecast"
					in.Topic = rule.TargetRef
					in.Brokers = strings.Join(legacy.KafkaBrokers, ",")
					in.KafkaUsername, in.KafkaPassword, in.KafkaTLS = legacy.KafkaUsername, legacy.KafkaPassword, legacy.KafkaTLS
				}
				created, err := channels.Create(context.Background(), in)
				if err != nil {
					// A channel that cannot be built (Kafka rule without brokers) stays disabled for the operator to fix.
					in.Enabled = false
					if in.Transport == model.ChannelTransportKafka && in.Brokers == "" {
						in.Brokers = "kafka:9092"
					}
					if created, err = channels.Create(context.Background(), in); err != nil {
						return fmt.Errorf("move forward rule %q to a channel: %w", rule.Name, err)
					}
				}
				id = created.ID
				byKey[key] = id
			}
			if err := model.DB.Model(&model.TelegramForwardRule{}).Where("id = ?", rule.ID).
				Updates(map[string]any{"target_kind": model.ForwardTargetChannel, "channel_id": id}).Error; err != nil {
				return err
			}
		}
	}
	return model.DB.Create(&model.SysConfig{ConfigKey: telegramChannelMigrationKey, ConfigValue: "done", ConfigName: "Telegram Forward Channels Migration Marker"}).Error
}
