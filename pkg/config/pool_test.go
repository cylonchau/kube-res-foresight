package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pool of the database connections is set in the config file (the Helm chart writes it), for each database the platform can use.
func TestThePoolSizeIsReadFromTheConfigFileForEveryDatabase(t *testing.T) {
	for name, body := range map[string]string{
		"postgres": "database_driver: postgres\ndatabase:\n  host: pg\n  database: d\n  username: u\n  max_open_connection: '60'\n  max_idle_connection: '30'\n",
		"mysql":    "database_driver: mysql\nmysql:\n  host: my\n  database: d\n  username: u\n  max_open_connection: '60'\n  max_idle_connection: '30'\n",
	} {
		file := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
		cfg, err := Load(file)
		require.NoError(t, err, name)
		section := cfg.Database
		if name == "mysql" {
			section = cfg.MySQL
		}
		require.Equal(t, "60", section.MaxOpenConnection, name)
		require.Equal(t, "30", section.MaxIdleConnection, name)
	}
}
