package migration

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/telegramagent"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
	"time"
)

func useDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	previousDB, previousConfig := model.DB, config.CONFIG
	model.DB = db
	config.CONFIG = &config.Config{}
	t.Cleanup(func() { model.DB = previousDB; config.CONFIG = previousConfig })
	return db
}

func TestTelegramAgentMigrationUpgradesAnExistingInstallation(t *testing.T) {
	db := useDB(t)
	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"

	// What a pre-agent database looks like: a globally unique command, a
	// subscription without membership state, and the bot kept in SysConfig.
	require.NoError(t, db.Exec("CREATE TABLE bot_command_def (id integer primary key autoincrement, command text not null)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_bot_command_def_command ON bot_command_def(command)").Error)
	require.NoError(t, db.Exec("CREATE TABLE telegram_client_subscription (id integer primary key autoincrement, account_id integer not null, peer_id integer not null, peer_type text not null, enabled numeric default 1)").Error)
	require.NoError(t, db.Exec("INSERT INTO telegram_client_subscription (account_id, peer_id, peer_type) VALUES (1, 7, 'channel')").Error)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}))
	for key, value := range map[string]string{
		telegram.ConfigBotEnabled: "true", telegram.ConfigBotToken: token, telegram.ConfigBotUsername: "kubeforecast_bot",
		telegram.ConfigBotAPIBaseURL: "http://bot-api:8081", telegram.ConfigBotMiniAppURL: "https://app",
	} {
		require.NoError(t, db.Create(&model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: key}).Error)
	}

	require.NoError(t, Migrate("sqlite"))

	var bots []model.TelegramBot
	require.NoError(t, db.Find(&bots).Error)
	require.Len(t, bots, 1, "the configured bot becomes the first telegram_bot row")
	assert.Equal(t, "default", bots[0].Name)
	assert.Equal(t, "kubeforecast_bot", bots[0].Username)
	assert.Equal(t, "http://bot-api:8081", bots[0].APIBaseURL)
	assert.True(t, bots[0].Enabled)
	plain, err := rdb.NewTelegramBotDAO(db).Token(&bots[0])
	require.NoError(t, err)
	assert.Equal(t, token, plain)

	var sub model.TelegramClientSubscription
	require.NoError(t, db.First(&sub).Error)
	assert.Equal(t, model.TelegramSubJoined, sub.State, "subscriptions that predate membership tracking are already joined")

	// A command can now exist once per bot.
	require.NoError(t, db.Create(&model.BotCommandDef{BotID: 1, Command: "/start", Name: "a", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.BotCommandDef{BotID: 2, Command: "/start", Name: "b", Enabled: true}).Error)
	assert.Error(t, db.Create(&model.BotCommandDef{BotID: 2, Command: "/start", Name: "c", Enabled: true}).Error)

	// The adapter remains internal; it creates no independent navigation module.
	var menu []model.Permission
	require.NoError(t, db.Where("code LIKE ?", "telegram_%").Find(&menu).Error)
	assert.Empty(t, menu)
	var other int64
	db.Model(&model.Permission{}).Where("code = ? OR code LIKE ? OR code = ?", "bot", "bot_%", "telegram_pipelines").Count(&other)
	assert.Zero(t, other, "no separate Bot menu and no Pipeline entry")

	// Running it again neither duplicates the bot nor fails.
	require.NoError(t, Migrate("sqlite"))
	var count int64
	db.Model(&model.TelegramBot{}).Count(&count)
	assert.EqualValues(t, 1, count)
}

func TestTelegramAgentMigrationSkipsWhatItCannotCarryOver(t *testing.T) {
	db := useDB(t)
	require.NoError(t, db.AutoMigrate(model.Models...))

	// No bot configured: nothing to carry over.
	require.NoError(t, seedLegacyTelegramBot(db))
	var count int64
	db.Model(&model.TelegramBot{}).Count(&count)
	assert.Zero(t, count)

	// A token that no longer looks valid is skipped, not fatal.
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: telegram.ConfigBotToken, ConfigValue: "not-a-token", ConfigName: "t"}).Error)
	require.NoError(t, seedLegacyTelegramBot(db))
	db.Model(&model.TelegramBot{}).Count(&count)
	assert.Zero(t, count)

	// An existing bot is never overwritten.
	token := "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
	name := "mine"
	_, err := rdb.NewTelegramBotDAO(db).Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", telegram.ConfigBotToken).Update("config_value", token).Error)
	require.NoError(t, seedLegacyTelegramBot(db))
	db.Model(&model.TelegramBot{}).Count(&count)
	assert.EqualValues(t, 1, count)

	// Without a database the migration is a no-op.
	model.DB = nil
	assert.NoError(t, migrateTelegramAgentOnce())
}

func TestTheOldTelegramMenuEntriesAreRetiredAndChannelSettingsSeeded(t *testing.T) {
	db := useDB(t)
	require.NoError(t, db.AutoMigrate(&model.Permission{}))
	retired := []string{"telegram_bindings", "telegram_commands", "telegram_templates", "telegram_pipelines", "bot", "bot_bots", "bot_bindings", "bot_commands", "bot_config", "bot_status"}
	for _, code := range retired {
		require.NoError(t, db.Create(&model.Permission{Name: code, Code: code, Type: "MENU", Status: true}).Error)
	}
	require.NoError(t, Migrate("sqlite"))
	for _, code := range retired {
		var n int64
		db.Model(&model.Permission{}).Where("code = ?", code).Count(&n)
		assert.Zero(t, n, code)
	}
	for _, key := range []string{telegramagent.ConfigRedisAddr, telegramagent.ConfigKafkaBrokers, telegramagent.ConfigKafkaTLS, telegramagent.ConfigCacheTTLHours} {
		var n int64
		db.Model(&model.SysConfig{}).Where("config_key = ?", key).Count(&n)
		assert.EqualValues(t, 1, n, key)
	}
}

func TestTheOldMessageHistoryIsRemovedAndTheTextColumnDropped(t *testing.T) {
	db := useDB(t)
	// An old install: messages stored as plain-text history, without any cache state.
	require.NoError(t, db.Exec("CREATE TABLE telegram_client_message (id integer primary key autoincrement, created_at datetime, account_id integer not null, subscription_id integer not null, peer_id integer not null, telegram_message_id integer not null, text text)").Error)
	require.NoError(t, db.Exec("INSERT INTO telegram_client_message (account_id, subscription_id, peer_id, telegram_message_id, text) VALUES (1, 1, 1, 1, 'old')").Error)
	require.NoError(t, Migrate("sqlite"))

	var left int64
	db.Model(&model.TelegramClientMessage{}).Count(&left)
	assert.Zero(t, left, "the uncompressed history is gone and can never be forwarded")
	assert.False(t, db.Migrator().HasColumn("telegram_client_message", "text"), "the plain text column is dropped to free its storage")
	assert.True(t, db.Migrator().HasColumn(&model.TelegramClientMessage{}, "body"))

	// A message cached after the upgrade survives a second run, and its text round-trips.
	row := model.TelegramClientMessage{AccountID: 1, PeerID: 2, TelegramMessageID: 9, Status: model.TelegramMsgPending, MessageAt: time.Now()}
	row.SetText("fresh message")
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, Migrate("sqlite"))
	var got model.TelegramClientMessage
	require.NoError(t, db.First(&got, row.ID).Error)
	require.NoError(t, got.LoadText())
	assert.Equal(t, "fresh message", got.Text)

	model.DB = nil
	assert.NoError(t, migrateTelegramCacheOnce())
	assert.NoError(t, migrateTelegramBodyOnce())
}

func TestOldForwardRulesAreMovedToChannelsThatNameWhereAndWho(t *testing.T) {
	db := useDB(t)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.TelegramForwardRule{}, &model.TelegramForwardChannel{}, &model.WorkflowDef{}))
	require.NoError(t, db.Create(&model.WorkflowDef{WorkflowKey: "review", Name: "Review", InitialState: "new", PipelineJSON: "{}", Enabled: true}).Error)
	for key, value := range map[string]string{
		telegramagent.ConfigRedisAddr: "redis.internal:6379", telegramagent.ConfigRedisDB: "3",
		telegramagent.ConfigKafkaBrokers: "k1:9092,k2:9092",
	} {
		require.NoError(t, db.Create(&model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: key}).Error)
	}
	// Written directly, the way the old build saved them.
	for _, r := range []model.TelegramForwardRule{
		{Name: "rate", TargetKind: "job", TargetRef: "llm_rating"},
		{Name: "rate again", TargetKind: "job", TargetRef: "llm_rating"},
		{Name: "flow", TargetKind: "workflow", TargetRef: "review"},
		{Name: "queue", TargetKind: "redis", TargetRef: "tg:messages"},
		{Name: "topic", TargetKind: "kafka", TargetRef: "tg-messages"},
		{Name: "user", TargetKind: "notify", TargetRef: "telegram", TargetRecipient: "42"},
	} {
		r.Enabled = true
		require.NoError(t, db.Create(&r).Error)
	}

	require.NoError(t, migrateTelegramChannelsOnce())

	var channels []model.TelegramForwardChannel
	require.NoError(t, db.Order("id").Find(&channels).Error)
	require.Len(t, channels, 4, "rules writing to the same job share one channel")
	by := map[string]model.TelegramForwardChannel{}
	for _, c := range channels {
		by[c.Name] = c
	}
	job := by["job:llm_rating"]
	assert.Equal(t, model.ChannelTransportDirect, job.Transport)
	assert.Equal(t, model.ChannelConsumerJob, job.ConsumerKind)
	assert.Equal(t, "llm_rating", job.ConsumerRef)
	flow := by["workflow:review"]
	assert.Equal(t, model.ChannelConsumerWorkflow, flow.ConsumerKind)
	stream := by["redis:tg:messages"]
	assert.Equal(t, model.ChannelTransportRedis, stream.Transport)
	assert.Equal(t, "tg:messages", stream.Stream)
	assert.Equal(t, "redis.internal:6379", stream.RedisAddr)
	assert.Equal(t, 3, stream.RedisDB)
	assert.Equal(t, "kubeforecast", stream.ConsumerGroup)
	assert.Equal(t, model.ChannelConsumerExternal, stream.ConsumerKind, "nothing in this system read the old queue")
	topic := by["kafka:tg-messages"]
	assert.Equal(t, "k1:9092,k2:9092", topic.Brokers)
	assert.Equal(t, "tg-messages", topic.Topic)

	var rules []model.TelegramForwardRule
	require.NoError(t, db.Order("id").Find(&rules).Error)
	for _, r := range rules[:5] {
		assert.Equal(t, model.ForwardTargetChannel, r.TargetKind, r.Name)
		assert.NotZero(t, r.ChannelID, r.Name)
	}
	assert.Equal(t, rules[0].ChannelID, rules[1].ChannelID)
	assert.Equal(t, model.ForwardTargetNotify, rules[5].TargetKind, "a rule to users is untouched")

	// Running it again changes nothing.
	require.NoError(t, migrateTelegramChannelsOnce())
	var n int64
	db.Model(&model.TelegramForwardChannel{}).Count(&n)
	assert.EqualValues(t, 4, n)
}
