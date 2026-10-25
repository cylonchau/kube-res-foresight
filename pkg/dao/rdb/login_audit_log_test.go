package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
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

func TestLoginAuditLogDAOListFiltersSortsAndPages(t *testing.T) {
	db := newLoginAuditLogTestDB(t)
	ctx := context.Background()
	dao := NewLoginAuditLogDAO(db)
	now := time.Now()
	rows := []model.LoginAuditLog{
		{Username: "alice", ClientIP: "10.0.0.1", Success: true, Method: "password", Reason: "ok", CreatedAt: now.Add(-3 * time.Hour)},
		{Username: "alice", ClientIP: "10.0.0.2", Success: false, Method: "password", Reason: "invalid_credentials", CreatedAt: now.Add(-2 * time.Hour)},
		{Username: "bob", ClientIP: "10.0.0.2", Success: false, Method: "ldap", Reason: "user_disabled", CreatedAt: now.Add(-1 * time.Hour)},
	}
	require.NoError(t, db.Create(&rows).Error)

	failed := false
	got, total, err := dao.List(ctx, LoginAuditListFilter{Success: &failed, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Equal(t, "bob", got[0].Username, "newest first by default")

	got, total, err = dao.List(ctx, LoginAuditListFilter{ClientIP: "10.0.0.2", Reason: "invalid_credentials", Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "alice", got[0].Username)

	since, until := now.Add(-150*time.Minute), now.Add(-90*time.Minute)
	_, total, err = dao.List(ctx, LoginAuditListFilter{Since: &since, Until: &until, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	got, total, err = dao.List(ctx, LoginAuditListFilter{SortBy: "username", Offset: 2, Limit: 2})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, got, 1)
	assert.Equal(t, "bob", got[0].Username, "username ascending, third row on the second page")

	got, _, err = dao.List(ctx, LoginAuditListFilter{SortBy: "injected; DROP TABLE x", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, "bob", got[0].Username, "an unknown sort column falls back to newest first")
}

func TestLoginAuditLogDAOPurgeOlderThan(t *testing.T) {
	db := newLoginAuditLogTestDB(t)
	dao := NewLoginAuditLogDAO(db)
	require.NoError(t, db.Create(&model.LoginAuditLog{Username: "old", CreatedAt: time.Now().AddDate(0, 0, -40)}).Error)
	require.NoError(t, db.Create(&model.LoginAuditLog{Username: "new", CreatedAt: time.Now()}).Error)
	removed, err := dao.PurgeOlderThan(context.Background(), time.Now().AddDate(0, 0, -30))
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
}
