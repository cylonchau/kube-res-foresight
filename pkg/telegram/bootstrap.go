package telegram

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"gorm.io/gorm"
)

// Bootstrap wires the outbound side of Telegram into engine: the ChatDriver
// that delivers pipeline notifications and this package's command handlers.
// It never opens a connection to Telegram and never starts polling; bot
// listeners and MTProto sessions belong to the Telegram Agent process only.
//
// The driver resolves its credentials per send from the telegram_bot table
// (first enabled bot), falling back to the legacy SysConfig bot for installs
// that have not been migrated yet, so bot changes apply without a restart.
//
// Safe to call with a nil db (nothing is wired, a warning is logged).
func Bootstrap(ctx context.Context, db *gorm.DB, engine *workflow.Engine) {
	if db == nil || engine == nil {
		logger.Warn("Skipping Telegram bootstrap: no database or workflow engine available")
		return
	}
	engine.SetChatDriver(NewDynamicChatDriver(SenderFor(db)))
	RegisterCommandHandlers(engine, rdb.NewTelegramBindingDAO(db), rdb.NewNotifyTemplateDAO(db))
}

// SenderFor returns the provider of the current outbound credentials.
func SenderFor(db *gorm.DB) SenderProvider {
	bots := rdb.NewTelegramBotDAO(db)
	configDAO := rdb.NewSysConfigDAO(db)
	return func(ctx context.Context) (string, string, string, bool, error) {
		token, baseURL, miniApp, ok, err := bots.DefaultSender(ctx)
		if err != nil || ok {
			return token, baseURL, miniApp, ok, err
		}
		cfg, err := LoadConfigFromSysConfig(configDAO)
		if err != nil || !cfg.Enabled || cfg.BotToken == "" {
			return "", "", "", false, err
		}
		return cfg.BotToken, cfg.APIBaseURL, cfg.MiniAppURL, true, nil
	}
}
