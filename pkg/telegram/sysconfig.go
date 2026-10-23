package telegram

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"strconv"
	"strings"
)

// SysConfigProvider resolves one SysConfig row by key — decouples this
// package from pkg/dao/rdb's concrete SysConfigDAO the same way
// BindingManager/TemplateProvider do (see bot.go's top-of-file comment:
// pkg/dao/rdb already imports this package for TelegramUser, so the
// reverse import would cycle). A concrete *rdb.SysConfigDAO already has
// this exact method, so callers just pass it straight through.
type SysConfigProvider interface {
	GetByKey(ctx context.Context, key string) (*model.SysConfig, error)
}

// LoadConfigFromSysConfig reads the bot's admin-editable SysConfig rows
// (ConfigBot*, falling back to the legacy ConfigLegacyBot* keys) into a
// config.TelegramConfig. Which SysConfig keys back the bot config, their
// legacy-key fallback order, and how a boolean value is parsed are this
// package's own concern — not pkg/cmd/server/server.go's, whose job is
// only to construct a SysConfigProvider and start the process.
//
// A nil configDAO (e.g. the database hasn't been initialized yet) yields a
// zero-value/disabled config rather than an error, so a caller can pass
// through an untyped-nil interface for that case without a special branch.
func LoadConfigFromSysConfig(configDAO SysConfigProvider) (config.TelegramConfig, error) {
	if configDAO == nil {
		return config.TelegramConfig{}, nil
	}
	ctx := context.Background()
	value := func(keys ...string) string {
		for _, key := range keys {
			cfg, err := configDAO.GetByKey(ctx, key)
			if err == nil && cfg != nil {
				v := strings.TrimSpace(cfg.ConfigValue)
				if v != "" && v != "-" {
					return v
				}
			}
		}
		return ""
	}
	checkBool := func(keys ...string) bool {
		for _, key := range keys {
			cfg, err := configDAO.GetByKey(ctx, key)
			if err == nil && cfg != nil {
				if b, err := strconv.ParseBool(strings.TrimSpace(cfg.ConfigValue)); err == nil && b {
					return true
				}
			}
		}
		return false
	}
	enabled := checkBool(ConfigBotEnabled, ConfigIntermediateBotEnabled, ConfigLegacyBotEnabled)
	return config.TelegramConfig{
		Enabled:     enabled,
		BotToken:    value(ConfigBotToken, ConfigIntermediateBotToken, ConfigLegacyBotToken),
		BotUsername: value(ConfigBotUsername, ConfigIntermediateBotUsername, ConfigLegacyBotUsername),
		MiniAppURL:  value(ConfigBotMiniAppURL, ConfigIntermediateMiniAppURL, ConfigLegacyMiniAppURL),
		APIBaseURL:  value(ConfigBotAPIBaseURL, ConfigIntermediateAPIBaseURL, ConfigLegacyAPIBaseURL),
	}, nil
}
