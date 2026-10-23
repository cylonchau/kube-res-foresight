package config

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/store"
	"github.com/spf13/viper"
	"strings"
)

// Config 全局配置结构
type Config struct {
	AppName        string                         `mapstructure:"app_name"`
	DatabaseDriver string                         `mapstructure:"database_driver"`
	MySQL          store.DatabaseConfig           `mapstructure:"mysql"`
	SQLite         store.DatabaseConfig           `mapstructure:"sqlite"`
	Database       store.DatabaseConfig           `mapstructure:"database"` // 保留旧的兼容性
	Server         ServerConfig                   `mapstructure:"server"`
	Loggers        map[string]logger.LoggerConfig `mapstructure:"loggers"`
	Telegram       TelegramConfig                 `mapstructure:"telegram"`
	Redis          RedisConfig                    `mapstructure:"redis"`
	Metrics        MetricsConfig                  `mapstructure:"metrics"`
}

// MetricsConfig serves /metrics and /healthz from standalone modules. Empty
// Listen disables the endpoint.
type MetricsConfig struct {
	Listen string `mapstructure:"listen"`
}

// RedisConfig configures optional shared infrastructure.
// An empty Addr keeps everything process-local.
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	Key      string `mapstructure:"key"`
}

type TelegramConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	BotToken    string `mapstructure:"bot_token"`
	BotUsername string `mapstructure:"bot_username"`
	MiniAppURL  string `mapstructure:"miniapp_url"`
	// APIBaseURL optionally points the long-poll receiver at a self-hosted
	// telegram-bot-api server instead of api.telegram.org. Empty keeps the
	// library default.
	APIBaseURL string `mapstructure:"api_base_url"`
}

// ServerConfig 服务器基础配置
type ServerConfig struct {
	Port      int    `mapstructure:"port"`
	GeoIPPath string `mapstructure:"geoip_path"`
	// JWTSecret signs and verifies API access/refresh tokens. Required for
	// production: without it every instance (and every restart) uses a
	// different generated key, invalidating sessions and breaking multi-
	// instance deployments. Also settable via KUBEFORECAST_SERVER_JWT_SECRET.
	JWTSecret string `mapstructure:"jwt_secret"`
	// DevMode gates local-development-only conveniences that must never
	// reach production — currently just the Telegram mock-login endpoint
	// (POST /api/v1/telegram/auth/mock-login), which logs in as the first
	// active admin with no credentials at all. Defaults to false (safe);
	// the route is not even registered unless this is explicitly true.
	// Also settable via KUBEFORECAST_SERVER_DEV_MODE.
	DevMode bool `mapstructure:"dev_mode"`
	// CORSAllowedOrigins lists exact origins (scheme+host+port, e.g.
	// "http://localhost:10000") allowed to make cross-origin requests with
	// credentials. Empty (the default) sends no CORS headers at all — safe
	// for the normal production setup, where this same binary serves both
	// the API and the built SPA from one origin and no browser ever needs
	// cross-origin access. Only needed when a frontend dev server (or any
	// client on a different origin) talks to the API directly.
	CORSAllowedOrigins []string `mapstructure:"cors_allowed_origins"`
}

var (
	globalConfig *Config
	CONFIG       *Config
)

// InitConfiguration 初始化配置 (兼容新架构)
func InitConfiguration(cfgPath string) error {
	_, err := Load(cfgPath)
	return err
}

// Load 从指定路径加载配置
func Load(cfgPath string) (*Config, error) {
	v := viper.New()
	if cfgPath != "" {
		v.SetConfigFile(cfgPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
	}

	v.SetEnvPrefix("KUBEFORECAST")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if cfg.AppName == "" {
		cfg.AppName = "kubeforecast"
	}

	globalConfig = cfg
	CONFIG = cfg
	return cfg, nil
}

// Get 获取全局配置单例
func Get() *Config {
	return globalConfig
}
