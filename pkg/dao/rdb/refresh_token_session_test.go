package rdb

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func newRefreshTokenSessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RefreshTokenSession{}))
	return db
}

// TestRefreshTokenSessionDAO_ClientTypeConflictAndKick covers the
// single-session-per-client-type flow this DAO backs: HasActiveByClientType
// finds the conflicting session, RevokeActiveByClientType kicks it out with
// a reason, and FamilyRevokedReason lets the kicked session's own family be
// told why on its next request.
func TestRefreshTokenSessionDAO_ClientTypeConflictAndKick(t *testing.T) {
	db := newRefreshTokenSessionTestDB(t)
	ctx := context.Background()
	dao := NewRefreshTokenSessionDAO(db)

	require.NoError(t, dao.Create(ctx, &model.RefreshTokenSession{
		UserID: 1, TokenHash: "hash-web-1", FamilyID: "family-web-1", ClientType: "web",
		ExpiresAt: time.Now().Add(24 * time.Hour), ClientIP: "1.2.3.4",
	}))
	require.NoError(t, dao.Create(ctx, &model.RefreshTokenSession{
		UserID: 1, TokenHash: "hash-tma-1", FamilyID: "family-tma-1", ClientType: "tma",
		ExpiresAt: time.Now().Add(24 * time.Hour), ClientIP: "5.6.7.8",
	}))

	// A second "web" login should see the first web session as a conflict...
	existing, err := dao.HasActiveByClientType(ctx, 1, "web")
	require.NoError(t, err)
	require.NotNil(t, existing)
	assert.Equal(t, "1.2.3.4", existing.ClientIP)

	// ...but the "tma" session is on a different client type, unaffected.
	tmaExisting, err := dao.HasActiveByClientType(ctx, 1, "tma")
	require.NoError(t, err)
	require.NotNil(t, tmaExisting)

	// Force-login kicks every active "web" session, tagging why.
	require.NoError(t, dao.RevokeActiveByClientType(ctx, 1, "web", "kicked"))

	activeAfterKick, err := dao.HasActiveByClientType(ctx, 1, "web")
	require.NoError(t, err)
	assert.Nil(t, activeAfterKick)

	// The kicked session's family is no longer active...
	active, err := dao.IsFamilyActive(ctx, 1, "family-web-1")
	require.NoError(t, err)
	assert.False(t, active)

	// ...and can be told it was a kick, not a generic expiry.
	reason, err := dao.FamilyRevokedReason(ctx, 1, "family-web-1")
	require.NoError(t, err)
	assert.Equal(t, "kicked", reason)

	// The untouched tma session still reports no reason (still active).
	tmaReason, err := dao.FamilyRevokedReason(ctx, 1, "family-tma-1")
	require.NoError(t, err)
	assert.Equal(t, "", tmaReason)

	// A different user's sessions are never touched by another user's kick.
	require.NoError(t, dao.Create(ctx, &model.RefreshTokenSession{
		UserID: 2, TokenHash: "hash-web-2", FamilyID: "family-web-2", ClientType: "web",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
	otherUserSession, err := dao.HasActiveByClientType(ctx, 2, "web")
	require.NoError(t, err)
	assert.NotNil(t, otherUserSession)
}

func TestRefreshTokenSessionDAO_HasActiveByClientType_NoneOrExpired(t *testing.T) {
	db := newRefreshTokenSessionTestDB(t)
	ctx := context.Background()
	dao := NewRefreshTokenSessionDAO(db)

	// Nothing at all yet.
	none, err := dao.HasActiveByClientType(ctx, 42, "web")
	require.NoError(t, err)
	assert.Nil(t, none)

	// An expired session doesn't count as active.
	require.NoError(t, dao.Create(ctx, &model.RefreshTokenSession{
		UserID: 42, TokenHash: "expired-hash", FamilyID: "expired-family", ClientType: "web",
		ExpiresAt: time.Now().Add(-time.Hour),
	}))
	expired, err := dao.HasActiveByClientType(ctx, 42, "web")
	require.NoError(t, err)
	assert.Nil(t, expired)
}
