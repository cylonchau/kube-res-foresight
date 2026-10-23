package rdb

import (
	"context"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"

	"github.com/DATA-DOG/go-sqlmock"
)

// setupMockDB initializes a mocked gorm.DB using sqlmock
func setupMockDB() (*gorm.DB, sqlmock.Sqlmock, error) {
	db, mock, err := sqlmock.New()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sqlmock: %w", err)
	}

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      db,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open gorm: %w", err)
	}

	return gormDB, mock, nil
}

// mustSetupMockDB is helper that panics on error
func mustSetupMockDB() (*gorm.DB, sqlmock.Sqlmock) {
	db, mock, err := setupMockDB()
	if err != nil {
		log.Fatal(err)
	}
	return db, mock
}

// createTestContext returns a context for testing
func createTestContext() context.Context {
	return context.Background()
}
