package config

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigurationFileAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("app_name: test\ndatabase_driver: sqlite\nserver:\n  port: 12345\n  dev_mode: false\ntelegram:\n  enabled: true\n"), 0600))
	t.Setenv("KUBEFORECAST_SERVER_DEV_MODE", "true")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "test", cfg.AppName)
	assert.Equal(t, 12345, cfg.Server.Port)
	assert.True(t, cfg.Server.DevMode)
	assert.Same(t, cfg, Get())
	assert.Same(t, cfg, CONFIG)
	require.NoError(t, InitConfiguration(path))
}

func TestLoadDefaultsAndBadFile(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(previous) })
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "kubeforecast", cfg.AppName)
	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("server: ["), 0600))
	_, err = Load(bad)
	assert.Error(t, err)
}
