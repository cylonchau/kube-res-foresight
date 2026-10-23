package telegram

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"testing"
)

// fakeSysConfigProvider is an in-memory SysConfigProvider — a missing key
// returns a non-nil error (any error; LoadConfigFromSysConfig only checks
// err == nil), matching how rdb.SysConfigDAO.GetByKey behaves for a row
// that doesn't exist.
type fakeSysConfigProvider struct {
	values map[string]string
}

func (f *fakeSysConfigProvider) GetByKey(ctx context.Context, key string) (*model.SysConfig, error) {
	v, ok := f.values[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &model.SysConfig{ConfigKey: key, ConfigValue: v}, nil
}

func TestLoadConfigFromSysConfig_NilProviderYieldsZeroValue(t *testing.T) {
	cfg, err := LoadConfigFromSysConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Enabled || cfg.BotToken != "" {
		t.Fatalf("expected a zero-value config for a nil provider, got: %#v", cfg)
	}
}

func TestLoadConfigFromSysConfig_ReadsPrimaryKeys(t *testing.T) {
	provider := &fakeSysConfigProvider{values: map[string]string{
		ConfigBotEnabled:    "true",
		ConfigBotToken:      "primary-token",
		ConfigBotUsername:   "primary_bot",
		ConfigBotMiniAppURL: "https://miniapp.example",
		ConfigBotAPIBaseURL: "https://api.example",
	}}

	cfg, err := LoadConfigFromSysConfig(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled || cfg.BotToken != "primary-token" || cfg.BotUsername != "primary_bot" ||
		cfg.MiniAppURL != "https://miniapp.example" || cfg.APIBaseURL != "https://api.example" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadConfigFromSysConfig_FallsBackToLegacyKeys(t *testing.T) {
	provider := &fakeSysConfigProvider{values: map[string]string{
		ConfigLegacyBotEnabled: "true",
		ConfigLegacyBotToken:   "legacy-token",
	}}

	cfg, err := LoadConfigFromSysConfig(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled || cfg.BotToken != "legacy-token" {
		t.Fatalf("expected fallback to legacy keys, got: %#v", cfg)
	}
}

func TestLoadConfigFromSysConfig_FallsBackToIntermediateKeys(t *testing.T) {
	provider := &fakeSysConfigProvider{values: map[string]string{
		ConfigIntermediateBotEnabled:  "true",
		ConfigIntermediateBotToken:    "intermediate-token",
		ConfigIntermediateBotUsername: "intermediate_bot",
	}}

	cfg, err := LoadConfigFromSysConfig(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Enabled || cfg.BotToken != "intermediate-token" || cfg.BotUsername != "intermediate_bot" {
		t.Fatalf("expected fallback to intermediate keys, got: %#v", cfg)
	}
}

// TestLoadConfigFromSysConfig_BlankAndDashTreatedAsUnset covers the "-"
// sentinel (used by the admin UI to represent "explicitly cleared") and an
// empty string both being skipped in favor of the next fallback key, not
// returned as-is.
func TestLoadConfigFromSysConfig_BlankAndDashTreatedAsUnset(t *testing.T) {
	provider := &fakeSysConfigProvider{values: map[string]string{
		ConfigBotToken:       "-",
		ConfigLegacyBotToken: "fallback-token",
		ConfigBotUsername:    "",
	}}

	cfg, err := LoadConfigFromSysConfig(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BotToken != "fallback-token" {
		t.Fatalf("expected \"-\" to be treated as unset and fall back, got %q", cfg.BotToken)
	}
	if cfg.BotUsername != "" {
		t.Fatalf("expected empty username to stay empty (no legacy key set either), got %q", cfg.BotUsername)
	}
}
