package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func newBotAPINodeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramBotAPINode{}))
	return db
}

func TestTelegramBotAPINodeDAO(t *testing.T) {
	db := newBotAPINodeTestDB(t)
	ctx := context.Background()
	dao := NewTelegramBotAPINodeDAO(db)

	node := &model.TelegramBotAPINode{
		Name:            "hk-bot-node-1",
		Status:          "pending",
		Enabled:         true,
		Hostname:        "node-hk-01",
		OperatingSystem: "linux",
		Architecture:    "amd64",
		Version:         "1.0.0",
		DesiredRunning:  true,
		Port:            8081,
		LocalMode:       true,
		APIID:           "123456",
	}
	require.NoError(t, db.Create(node).Error)

	// GetByID
	got, err := dao.GetByID(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "hk-bot-node-1", got.Name)
	assert.Equal(t, 8081, got.Port)

	// GetByName
	byName, err := dao.GetByName(ctx, "hk-bot-node-1")
	require.NoError(t, err)
	assert.Equal(t, node.ID, byName.ID)

	// ListPaginated
	kw := "hk"
	list, total, err := dao.ListPaginated(ctx, &apis.TelegramBotAPINodeListReq{
		Keyword: &kw,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, list, 1)

	// ApproveWithToken. The node hasn't heartbeated yet (LastSeenAt is nil),
	// so GetByID's staleness downgrade shows it as "offline" here — matching
	// ListPaginated's existing behavior for the same never-seen case, which
	// is the point of applyStaleness being shared by both read paths.
	require.NoError(t, dao.ApproveWithToken(ctx, node.ID, "token-hash-123", "token-encrypted-abc"))
	approved, err := dao.GetByID(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "offline", approved.Status)
	assert.Equal(t, "token-hash-123", approved.TokenHash)

	// ApproveWithToken on an already-approved (no longer pending) node must
	// fail with the race-condition sentinel, not silently succeed.
	err = dao.ApproveWithToken(ctx, node.ID, "token-hash-456", "token-encrypted-def")
	assert.ErrorIs(t, err, ErrNodeNotPending)

	// SetEnabled
	require.NoError(t, dao.SetEnabled(ctx, node.ID, false))
	disabled, err := dao.GetByID(ctx, node.ID)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)
	assert.Equal(t, "disabled", disabled.Status)

	// UpdateDesiredConfig
	newPort := 8082
	desiredRun := false
	apiHashEncrypted := "encrypted-hash"
	require.NoError(t, dao.UpdateDesiredConfig(ctx, node.ID, &apis.TelegramBotAPINodeDesiredConfigReq{
		Port:           &newPort,
		DesiredRunning: &desiredRun,
	}, &apiHashEncrypted))

	configUpdated, err := dao.GetByID(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, 8082, configUpdated.Port)
	assert.False(t, configUpdated.DesiredRunning)
	assert.Equal(t, "encrypted-hash", configUpdated.APIHashEncrypted)

	// Reject
	require.NoError(t, dao.Reject(ctx, node.ID))
	rejected, err := dao.GetByID(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Status)

	// Delete
	require.NoError(t, dao.Delete(ctx, node.ID))
	_, err = dao.GetByID(ctx, node.ID)
	assert.Error(t, err)
}
