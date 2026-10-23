package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func newLoginAuditLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.LoginAuditLog{}))
	return db
}

func TestLoginAuditLogDAO(t *testing.T) {
	db := newLoginAuditLogTestDB(t)
	ctx := context.Background()
	dao := NewLoginAuditLogDAO(db)

	// Record success and failure
	require.NoError(t, dao.Record(ctx, "admin", "192.168.1.10", true))
	require.NoError(t, dao.Record(ctx, "admin", "192.168.1.10", false))
	require.NoError(t, dao.Record(ctx, "hacker", "10.0.0.1", false))

	// CountFailuresSince
	since := time.Now().Add(-1 * time.Hour)
	count, err := dao.CountFailuresSince(ctx, since)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// LatestSuccessByUsername
	lastSuccess, err := dao.LatestSuccessByUsername(ctx, "admin")
	require.NoError(t, err)
	require.NotNil(t, lastSuccess)

	// Non-existent user success returns nil
	noSuccess, err := dao.LatestSuccessByUsername(ctx, "hacker")
	require.NoError(t, err)
	assert.Nil(t, noSuccess)
}
