package telegramagent

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type mapConfig map[string]string

func (m mapConfig) GetValueOrDefault(_ context.Context, key, fallback string) (string, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	return fallback, nil
}

type failingConfig struct{}

func (failingConfig) GetValueOrDefault(context.Context, string, string) (string, error) {
	return "", errors.New("db down")
}

func TestChannelSettingsFallBackToThePlatformRedis(t *testing.T) {
	ctx := context.Background()
	platform := config.RedisConfig{Addr: "10.0.0.1:6379", Password: "pw", DB: 3}

	cfg, err := LoadChannelConfig(ctx, mapConfig{}, platform)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:6379", cfg.RedisAddr, "no Telegram-specific Redis: use the platform one")
	assert.Equal(t, "pw", cfg.RedisPassword)
	assert.Equal(t, 3, cfg.RedisDB)
	assert.Empty(t, cfg.KafkaBrokers)

	cfg, err = LoadChannelConfig(ctx, mapConfig{
		ConfigRedisAddr: "redis:6380", ConfigRedisPassword: "-", ConfigRedisDB: "5",
		ConfigKafkaBrokers: " k1:9092, ,k2:9092 ", ConfigKafkaUsername: "u", ConfigKafkaPassword: "p", ConfigKafkaTLS: "true",
	}, platform)
	require.NoError(t, err)
	assert.Equal(t, "redis:6380", cfg.RedisAddr)
	assert.Equal(t, "pw", cfg.RedisPassword, "a dash means unset")
	assert.Equal(t, 5, cfg.RedisDB)
	assert.Equal(t, []string{"k1:9092", "k2:9092"}, cfg.KafkaBrokers)
	assert.Equal(t, "u", cfg.KafkaUsername)
	assert.True(t, cfg.KafkaTLS)

	_, err = LoadChannelConfig(ctx, mapConfig{ConfigRedisDB: "x"}, platform)
	assert.ErrorContains(t, err, ConfigRedisDB)
	_, err = LoadChannelConfig(ctx, mapConfig{ConfigRedisDB: "-2"}, platform)
	assert.Error(t, err)
	cfg, err = LoadChannelConfig(ctx, failingConfig{}, platform)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:6379", cfg.RedisAddr, "an unreadable setting falls back instead of stopping forwarding")
}
