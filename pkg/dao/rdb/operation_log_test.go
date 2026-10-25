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

func newOperationLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.OperationLog{}))
	return db
}

func TestOperationLogDAO_ListFilters(t *testing.T) {
	db := newOperationLogTestDB(t)
	ctx := context.Background()
	dao := NewOperationLogDAO(db)

	require.NoError(t, dao.Create(ctx, &model.OperationLog{
		ClientType: "page", Username: "admin", Method: "POST", Path: "/api/v1/users",
		Module: "users", StatusCode: 200, ClientIP: "1.1.1.1",
	}))
	require.NoError(t, dao.Create(ctx, &model.OperationLog{
		ClientType: "tma", Username: "ops", Method: "POST", Path: "/api/v1/records",
		Module: "records", StatusCode: 500, ClientIP: "2.2.2.2",
	}))
	require.NoError(t, dao.Create(ctx, &model.OperationLog{
		ClientType: "api", Username: "developer:key1", Method: "DELETE", Path: "/api/v1/users/1",
		Module: "users", StatusCode: 204, ClientIP: "3.3.3.3",
	}))

	// Filter by client type.
	logs, total, err := dao.List(ctx, OperationLogListFilter{ClientType: "tma"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, "ops", logs[0].Username)

	// Filter by keyword (matches path).
	logs, total, err = dao.List(ctx, OperationLogListFilter{Keyword: "users"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, logs, 2)

	// Filter by success/failure.
	success := true
	logs, total, err = dao.List(ctx, OperationLogListFilter{Success: &success})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	for _, l := range logs {
		assert.Less(t, l.StatusCode, 400)
	}

	failed := false
	logs, total, err = dao.List(ctx, OperationLogListFilter{Success: &failed})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, 500, logs[0].StatusCode)
}

func TestOperationLogDAO_DeleteAll(t *testing.T) {
	db := newOperationLogTestDB(t)
	ctx := context.Background()
	dao := NewOperationLogDAO(db)

	require.NoError(t, dao.Create(ctx, &model.OperationLog{ClientType: "page", Method: "POST", Path: "/api/v1/users"}))
	require.NoError(t, dao.Create(ctx, &model.OperationLog{ClientType: "tma", Method: "PUT", Path: "/api/v1/records/1"}))

	_, total, err := dao.List(ctx, OperationLogListFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)

	require.NoError(t, dao.DeleteAll(ctx))

	_, total, err = dao.List(ctx, OperationLogListFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

func TestOperationLogDAO_PurgeOlderThan(t *testing.T) {
	db := newOperationLogTestDB(t)
	ctx := context.Background()
	dao := NewOperationLogDAO(db)

	old := model.OperationLog{ClientType: "page", Method: "POST", Path: "/api/v1/users", StatusCode: 200}
	require.NoError(t, db.Create(&old).Error)
	// Backdate it past the retention cutoff directly, since CreatedAt is
	// otherwise stamped by GORM at insert time.
	require.NoError(t, db.Model(&old).Update("created_at", time.Now().AddDate(0, 0, -100)).Error)

	recent := model.OperationLog{ClientType: "page", Method: "POST", Path: "/api/v1/records", StatusCode: 200}
	require.NoError(t, db.Create(&recent).Error)

	removed, err := dao.PurgeOlderThan(ctx, time.Now().AddDate(0, 0, -90))
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)

	_, total, err := dao.List(ctx, OperationLogListFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
}
