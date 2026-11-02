package config

import (
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/store"
	"github.com/cylonchau/kube-res-foresight/pkg/utils"
	"github.com/spf13/viper"
	"os"
	"path/filepath"
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
	Redis          RedisConfig                    `mapstructure:"redis"`
	Metrics        MetricsConfig                  `mapstructure:"metrics"`
}

// MetricsConfig serves /metrics, /healthz and /readyz from standalone modules. Empty
// Listen disables the endpoint.
type MetricsConfig struct {
	Listen string `mapstructure:"listen"`
	// LegacyNames also exports every series under its name before the rename (kube_resource_fcst_*), for the time the alerts and
	// dashboards of a production system are moved to the new one.
	LegacyNames bool `mapstructure:"legacy_names"`
}

// RedisConfig configures optional shared infrastructure.
// An empty Addr keeps everything process-local.
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	Key      string `mapstructure:"key"`
}

// ServerConfig 服务器基础配置
type ServerConfig struct {
	Port      int    `mapstructure:"port"`
	GeoIPPath string `mapstructure:"geoip_path"`
	// JWTSecret signs and verifies API access/refresh tokens. Required for
	// production: without it every instance (and every restart) uses a
	// different generated key, invalidating sessions and breaking multi-
	// instance deployments. Also settable via KUBE_RES_FORESIGHT_SERVER_JWT_SECRET.
	JWTSecret string `mapstructure:"jwt_secret"`
	// DevMode is kept only so configuration files that still set it are read without error; nothing depends on it.
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

	v.SetEnvPrefix("KUBE_RES_FORESIGHT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// Bind both names so saved deployments keep their database connection and JWT key.
	keys := append(v.AllKeys(), "app_name", "database_driver", "server.jwt_secret", "server.port", "server.dev_mode", "metrics.listen", "metrics.legacy_names", "sqlite.file")
	for _, key := range keys {
		env := "KUBE_RES_FORESIGHT_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		if err := v.BindEnv(append([]string{key}, utils.ProjectEnvNames(env)...)...); err != nil {
			return nil, fmt.Errorf("bind environment for %s: %w", key, err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if cfg.AppName == "" || cfg.AppName == "kubeforecast" || cfg.AppName == "kube-resource-fcst" {
		cfg.AppName = "kube-res-foresight"
	}
	if strings.EqualFold(cfg.DatabaseDriver, "sqlite") && filepath.Base(cfg.SQLite.File) == "kube-res-foresight.db" {
		if _, err := os.Stat(cfg.SQLite.File); os.IsNotExist(err) {
			for _, oldName := range []string{"kube-resource-fcst.db", "kubeforecast.db"} {
				legacy := filepath.Join(filepath.Dir(cfg.SQLite.File), oldName)
				if info, err := os.Stat(legacy); err == nil && info.Mode().IsRegular() {
					cfg.SQLite.File = legacy
					break
				}
			}
		}
	}

	globalConfig = cfg
	CONFIG = cfg
	return cfg, nil
}

// Get 获取全局配置单例
func Get() *Config {
	return globalConfig
}
