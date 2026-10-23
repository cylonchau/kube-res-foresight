package telegramagent

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"strconv"
	"strings"
)

// SysConfig keys. The message cache TTL is a live setting. The Redis and Kafka keys are how forwarding
// to Redis and Kafka was configured before forward channels existed; they are read once, when old rules
// are moved to channels (see LoadChannelConfig), and nothing writes to a broker from them any more.
const (
	ConfigRedisAddr     = "telegram.forward.redis.addr"
	ConfigRedisPassword = "telegram.forward.redis.password"
	ConfigRedisDB       = "telegram.forward.redis.db"
	ConfigKafkaBrokers  = "telegram.forward.kafka.brokers"
	ConfigKafkaUsername = "telegram.forward.kafka.username"
	ConfigKafkaPassword = "telegram.forward.kafka.password"
	ConfigKafkaTLS      = "telegram.forward.kafka.tls"
	ConfigCacheTTLHours = "telegram.cache.ttl_hours"
)

// ChannelConfig is the connection setting of the two forward channels.
type ChannelConfig struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	KafkaBrokers  []string
	KafkaUsername string
	KafkaPassword string
	KafkaTLS      bool
}

// ConfigSource reads one setting; *rdb.SysConfigDAO implements it.
type ConfigSource interface {
	GetValueOrDefault(ctx context.Context, key, fallback string) (string, error)
}

// LoadChannelConfig reads the channel settings. Redis falls back to the
// platform-wide Redis of config.yaml when no Telegram-specific address is set.
func LoadChannelConfig(ctx context.Context, src ConfigSource, platformRedis config.RedisConfig) (ChannelConfig, error) {
	get := func(key, fallback string) string {
		value, err := src.GetValueOrDefault(ctx, key, fallback)
		if err != nil {
			return fallback
		}
		value = strings.TrimSpace(value)
		if value == "" || value == "-" {
			return fallback
		}
		return value
	}
	cfg := ChannelConfig{
		RedisAddr:     get(ConfigRedisAddr, platformRedis.Addr),
		RedisPassword: get(ConfigRedisPassword, platformRedis.Password),
		KafkaUsername: get(ConfigKafkaUsername, ""),
		KafkaPassword: get(ConfigKafkaPassword, ""),
	}
	cfg.RedisDB = platformRedis.DB
	if raw := get(ConfigRedisDB, ""); raw != "" {
		db, err := strconv.Atoi(raw)
		if err != nil || db < 0 {
			return cfg, fmt.Errorf("%s must be a non-negative number", ConfigRedisDB)
		}
		cfg.RedisDB = db
	}
	for _, broker := range strings.Split(get(ConfigKafkaBrokers, ""), ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			cfg.KafkaBrokers = append(cfg.KafkaBrokers, broker)
		}
	}
	cfg.KafkaTLS, _ = strconv.ParseBool(get(ConfigKafkaTLS, "false"))
	return cfg, nil
}
