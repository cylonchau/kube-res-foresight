package store

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRDBStore_Initialize_SQLite(t *testing.T) {
	s := GetInstance()

	// 1. Basic configuration for SQLite memory mode
	config := DatabaseConfig{
		Type:              SQLite,
		File:              ":memory:",
		MaxOpenConnection: "10",
		MaxIdleConnection: "5",
	}

	// 2. Test Initialization
	err := s.Initialize(config)
	assert.NoError(t, err)

	// 3. Test IsInitialized
	assert.True(t, s.IsInitialized())

	// 4. Test GetDB
	db := s.GetDB()
	assert.NotNil(t, db)

	// 5. Test GetDatabaseType
	assert.Equal(t, SQLite, s.GetDatabaseType())

	// 6. Test HealthCheck
	err = s.HealthCheck()
	assert.NoError(t, err)

	// 7. Clean up
	err = s.Close()
	assert.NoError(t, err)
}

func TestRDBStore_SQLiteUsesSerializedWALConnections(t *testing.T) {
	s := &RDBStore{}
	err := s.Initialize(DatabaseConfig{
		Type: SQLite, File: filepath.Join(t.TempDir(), "runtime.db"),
		MaxOpenConnection: "20", MaxIdleConnection: "10",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	sqlDB, err := s.GetDB().DB()
	require.NoError(t, err)
	assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)

	var journalMode string
	require.NoError(t, s.GetDB().Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
	assert.Equal(t, "wal", journalMode)
	var busyTimeout int
	require.NoError(t, s.GetDB().Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
	assert.Equal(t, 10000, busyTimeout)
}

func TestRDBStore_MySQL_Mock(t *testing.T) {
	// 1. Create sqlmock
	sqlDB, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer sqlDB.Close()

	// 2. Mock Expectation for GORM MySQL version probe
	mock.ExpectQuery("SELECT VERSION()").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow("8.0.0"))

	// 3. Initialize GORM with mock sqlDB
	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn: sqlDB,
	}), &gorm.Config{})
	assert.NoError(t, err)

	// 4. Manual setup RDBStore
	s := &RDBStore{
		db: gormDB,
		config: DatabaseConfig{
			Type: MySQL,
		},
	}

	// 5. Test HealthCheck
	err = s.HealthCheck()
	assert.NoError(t, err)

	// 6. Test Database Type
	assert.Equal(t, MySQL, s.GetDatabaseType())

	// 7. Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRDBStore_PostgreSQL_Mock(t *testing.T) {
	// 1. Create sqlmock
	sqlDB, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer sqlDB.Close()

	// 2. Initialize GORM with mock sqlDB
	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}), &gorm.Config{})
	assert.NoError(t, err)

	// 4. Manual setup RDBStore
	s := &RDBStore{
		db: gormDB,
		config: DatabaseConfig{
			Type: PostgreSQL,
		},
	}

	// 5. Test HealthCheck
	err = s.HealthCheck()
	assert.NoError(t, err)

	// 6. Test Database Type
	assert.Equal(t, PostgreSQL, s.GetDatabaseType())

	// 7. Verify all expectations were met
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRDBStore_MonitorConnectionPool(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	assert.NoError(t, err)
	defer sqlDB.Close()

	gormDB, _ := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB}), &gorm.Config{})
	s := &RDBStore{db: gormDB}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// This should run and exit when context is cancelled
	s.MonitorConnectionPool(ctx)
	// If it doesn't hang, it's successful in terms of loop control
}

func TestRDBStore_ValidateConfig(t *testing.T) {
	// Test missing file for SQLite
	s := &RDBStore{}
	s.config = DatabaseConfig{
		Type: SQLite,
		File: "",
	}
	err := s.validateConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database file path is required")

	// Test missing host for MySQL
	s.config = DatabaseConfig{
		Type: MySQL,
		Host: "",
	}
	err = s.validateConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database host is required")

	// Test missing database for Postgres
	s.config = DatabaseConfig{
		Type:     PostgreSQL,
		Host:     "localhost",
		Port:     5432,
		Username: "user",
		Database: "",
	}
	err = s.validateConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database name is required")
}
