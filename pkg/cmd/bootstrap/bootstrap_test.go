package bootstrap

import (
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeDatabaseRejectsMissingConfig(t *testing.T) {
	previous := config.CONFIG
	config.CONFIG = nil
	t.Cleanup(func() { config.CONFIG = previous })

	db, err := InitializeDatabase("sqlite")
	assert.Nil(t, db)
	assert.EqualError(t, err, "configuration is not initialized")
}

func TestLoadConfigAllowsEmptyPath(t *testing.T) {
	require.NoError(t, LoadConfig(""))
}

func TestLoadConfigReadsYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("app_name: phase1-test\n"), 0o600))
	require.NoError(t, LoadConfig(path))
	assert.Equal(t, "phase1-test", config.CONFIG.AppName)
}

func TestInitializeDatabaseRejectsUnsupportedDriver(t *testing.T) {
	previous := config.CONFIG
	config.CONFIG = &config.Config{SQLite: store.DatabaseConfig{File: ":memory:"}}
	t.Cleanup(func() { config.CONFIG = previous })

	db, err := InitializeDatabase("oracle")
	assert.Nil(t, db)
	assert.EqualError(t, err, `unsupported sql driver "oracle"`)
}

func TestDatabaseConfigSelectsSupportedDrivers(t *testing.T) {
	cfg := &config.Config{
		SQLite:   store.DatabaseConfig{File: "test.db"},
		MySQL:    store.DatabaseConfig{Host: "mysql"},
		Database: store.DatabaseConfig{Host: "postgres"},
	}
	tests := []struct {
		driver string
		typeID store.DBType
		host   string
	}{
		{driver: "sqlite", typeID: store.SQLite},
		{driver: "mysql", typeID: store.MySQL, host: "mysql"},
		{driver: "postgres", typeID: store.PostgreSQL, host: "postgres"},
		{driver: "postgresql", typeID: store.PostgreSQL, host: "postgres"},
	}
	for _, test := range tests {
		t.Run(test.driver, func(t *testing.T) {
			result, err := databaseConfig(cfg, test.driver)
			require.NoError(t, err)
			assert.Equal(t, test.typeID, result.Type)
			assert.Equal(t, test.host, result.Host)
		})
	}
}

func TestInitializeDatabaseRejectsEmptyDriverConfig(t *testing.T) {
	previous := config.CONFIG
	config.CONFIG = &config.Config{}
	t.Cleanup(func() { config.CONFIG = previous })

	db, err := InitializeDatabase("sqlite")
	assert.Nil(t, db)
	assert.EqualError(t, err, "database configuration for sqlite is empty")
}

func TestInitializeDatabaseSQLite(t *testing.T) {
	store.ResetInstance(nil)
	t.Cleanup(func() { store.ResetInstance(nil) })
	previous := config.CONFIG
	config.CONFIG = &config.Config{SQLite: store.DatabaseConfig{File: ":memory:"}}
	t.Cleanup(func() { config.CONFIG = previous })

	db, err := InitializeDatabase("sqlite")
	require.NoError(t, err)
	require.NotNil(t, db)
}
