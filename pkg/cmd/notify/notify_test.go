package notify

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/config"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestNewCommandExposesNotifyDatabaseFlags(t *testing.T) {
	cmd := NewCommand()
	require.Equal(t, "notify", cmd.Use)
	require.NotNil(t, cmd.Flags().Lookup("config"))
	require.NotNil(t, cmd.Flags().Lookup("sql-driver"))
}

func TestRunReportsConfigurationFailure(t *testing.T) {
	err := run(context.Background(), filepath.Join(t.TempDir(), "missing", "config.yaml"), "sqlite")
	require.ErrorContains(t, err, "initialize notify module")
}

func TestRunInitializesStandaloneNotifyDatabase(t *testing.T) {
	previousConfig, previousDB := config.CONFIG, model.DB
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	databasePath := filepath.Join(directory, "notify.db")
	contents := "app_name: test\nsqlite:\n  file: '" + databasePath + "'\n  max_open_connection: '1'\n  max_idle_connection: '1'\n"
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, run(ctx, configPath, "sqlite"))
	t.Cleanup(func() {
		if model.DB != nil {
			sqlDB, err := model.DB.DB()
			if err == nil {
				_ = sqlDB.Close()
			}
		}
		config.CONFIG, model.DB = previousConfig, previousDB
	})
}
