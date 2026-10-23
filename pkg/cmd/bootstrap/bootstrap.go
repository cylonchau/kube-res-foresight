package bootstrap

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/store"
	"gorm.io/gorm"
)

// LoadConfig initializes the process-wide configuration and logger once for a
// command. Keeping this in one place prevents the combined runtime from
// loading the same configuration concurrently in each module.
func LoadConfig(configFile string) error {
	if configFile == "" {
		return nil
	}
	if err := config.InitConfiguration(configFile); err != nil {
		return err
	}
	if err := logger.Initialize(logger.Config{Loggers: config.CONFIG.Loggers}); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

// InitializeDatabase initializes the existing shared store and publishes the
// resulting GORM handle through model.DB, matching the established command
// lifecycle used by platform and job.
func InitializeDatabase(sqlDriver string) (*gorm.DB, error) {
	if config.CONFIG == nil {
		return nil, fmt.Errorf("configuration is not initialized")
	}

	dbCfg, err := databaseConfig(config.CONFIG, sqlDriver)
	if err != nil {
		return nil, err
	}
	if dbCfg.IsEmpty() {
		return nil, fmt.Errorf("database configuration for %s is empty", sqlDriver)
	}

	s := store.GetInstance()
	if err := s.Initialize(dbCfg); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	model.DB = s.GetDB()
	if model.DB == nil {
		return nil, fmt.Errorf("database initialization returned a nil handle")
	}
	return model.DB, nil
}

func databaseConfig(cfg *config.Config, sqlDriver string) (store.DatabaseConfig, error) {
	var dbCfg store.DatabaseConfig
	switch sqlDriver {
	case "mysql":
		dbCfg = cfg.MySQL
		dbCfg.Type = store.MySQL
	case "sqlite":
		dbCfg = cfg.SQLite
		dbCfg.Type = store.SQLite
	case "postgres", "postgresql":
		dbCfg = cfg.Database
		dbCfg.Type = store.PostgreSQL
	default:
		return store.DatabaseConfig{}, fmt.Errorf("unsupported sql driver %q", sqlDriver)
	}
	return dbCfg, nil
}
