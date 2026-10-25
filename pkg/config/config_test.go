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
	require.NoError(t, os.WriteFile(path, []byte("app_name: test\ndatabase_driver: sqlite\nserver:\n  port: 12345\n  dev_mode: false\n"), 0600))
	t.Setenv("KUBE_RESOURCE_FCST_SERVER_DEV_MODE", "true")
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
	assert.Equal(t, "kube-resource-fcst", cfg.AppName)
	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("server: ["), 0600))
	_, err = Load(bad)
	assert.Error(t, err)
}

func TestLoadKeepsLegacySettingsAndNewEnvironmentWins(t *testing.T) {
	t.Setenv("KUBE_RESOURCE_FCST_SERVER_JWT_SECRET", "")
	require.NoError(t, os.Unsetenv("KUBE_RESOURCE_FCST_SERVER_JWT_SECRET"))
	t.Setenv("KUBEFORECAST_SERVER_JWT_SECRET", "existing-signing-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("app_name: kubeforecast\ndatabase_driver: sqlite\n"), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "kube-resource-fcst", cfg.AppName)
	assert.Equal(t, "existing-signing-key", cfg.Server.JWTSecret)
	t.Setenv("KUBE_RESOURCE_FCST_SERVER_JWT_SECRET", "new-signing-key")
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, "new-signing-key", cfg.Server.JWTSecret)
}

func TestLoadPreservesExistingSQLiteWithoutMovingFiles(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "kubeforecast.db")
	current := filepath.Join(dir, "kube-resource-fcst.db")
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database_driver: sqlite\nsqlite:\n  file: "+filepath.ToSlash(current)+"\n"), 0600))
	require.NoError(t, os.WriteFile(legacy, []byte("existing-database"), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, legacy, cfg.SQLite.File)
	_, err = os.Stat(current)
	assert.True(t, os.IsNotExist(err))
	data, err := os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, "existing-database", string(data))
	// An explicitly existing new database is never replaced with the legacy one.
	require.NoError(t, os.WriteFile(current, []byte("selected-database"), 0600))
	cfg, err = Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(current), cfg.SQLite.File)
}
