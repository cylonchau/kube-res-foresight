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

func newWorkflowTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.WorkflowPendingReply{}))
	return db
}

func TestWorkflowPendingReplyDAO_BeginReplacesPrior(t *testing.T) {
	db := newWorkflowTestDB(t)
	ctx := context.Background()
	dao := NewWorkflowPendingReplyDAO(db)

	require.NoError(t, dao.Begin(ctx, 1001, 1, "leader_reject"))
	require.NoError(t, dao.Begin(ctx, 1001, 2, "leader_reject")) // second tap before replying replaces the first

	var count int64
	require.NoError(t, db.Model(&model.WorkflowPendingReply{}).Where("telegram_user_id = ?", 1001).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	instanceID, event, ok, err := dao.Consume(ctx, 1001)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(2), instanceID)
	assert.Equal(t, "leader_reject", event)
}

func TestWorkflowPendingReplyDAO_ConsumeDeletes(t *testing.T) {
	db := newWorkflowTestDB(t)
	ctx := context.Background()
	dao := NewWorkflowPendingReplyDAO(db)

	require.NoError(t, dao.Begin(ctx, 2001, 42, "ops_complete"))

	instanceID, event, ok, err := dao.Consume(ctx, 2001)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(42), instanceID)
	assert.Equal(t, "ops_complete", event)

	// A second consume finds nothing — the row was deleted by the first.
	_, _, ok, err = dao.Consume(ctx, 2001)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestWorkflowPendingReplyDAO_ConsumeIgnoresExpired(t *testing.T) {
	db := newWorkflowTestDB(t)
	ctx := context.Background()
	dao := NewWorkflowPendingReplyDAO(db)

	require.NoError(t, db.Create(&model.WorkflowPendingReply{
		TelegramUserID: 3001,
		InstanceID:     7,
		Event:          "leader_reject",
		ExpiresAt:      time.Now().Add(-time.Minute), // already expired
	}).Error)

	_, _, ok, err := dao.Consume(ctx, 3001)
	require.NoError(t, err)
	assert.False(t, ok)

	var count int64
	require.NoError(t, db.Model(&model.WorkflowPendingReply{}).Where("telegram_user_id = ?", 3001).Count(&count).Error)
	assert.Equal(t, int64(0), count, "the expired row should have been cleaned up")
}

func TestWorkflowPendingReplyDAO_ConsumeNothingPending(t *testing.T) {
	db := newWorkflowTestDB(t)
	ctx := context.Background()
	dao := NewWorkflowPendingReplyDAO(db)

	_, _, ok, err := dao.Consume(ctx, 4001)
	require.NoError(t, err)
	assert.False(t, ok)
}
