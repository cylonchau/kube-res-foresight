package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

var (
	ErrRefreshTokenInvalid = errors.New("invalid refresh token")
	ErrRefreshTokenReused  = errors.New("refresh token reuse detected")
)

type RefreshTokenSessionDAO struct{ db *gorm.DB }

func NewRefreshTokenSessionDAO(db *gorm.DB) *RefreshTokenSessionDAO {
	return &RefreshTokenSessionDAO{db: db}
}

func (dao *RefreshTokenSessionDAO) Create(ctx context.Context, session *model.RefreshTokenSession) error {
	return dao.db.WithContext(ctx).Create(session).Error
}

func (dao *RefreshTokenSessionDAO) Rotate(ctx context.Context, oldHash string, replacement *model.RefreshTokenSession) (*model.RefreshTokenSession, error) {
	var current model.RefreshTokenSession
	reused := false
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ?", oldHash).First(&current).Error; err != nil {
			return ErrRefreshTokenInvalid
		}
		if current.RevokedAt != nil {
			now := time.Now()
			if err := tx.Model(&model.RefreshTokenSession{}).Where("family_id = ? AND revoked_at IS NULL", current.FamilyID).Update("revoked_at", now).Error; err != nil {
				return err
			}
			reused = true
			return nil
		}
		if time.Now().After(current.ExpiresAt) {
			return ErrRefreshTokenInvalid
		}

		now := time.Now()
		if err := tx.Model(&current).Updates(map[string]any{
			"revoked_at":       now,
			"last_used_at":     now,
			"replaced_by_hash": replacement.TokenHash,
		}).Error; err != nil {
			return err
		}
		replacement.UserID = current.UserID
		replacement.FamilyID = current.FamilyID
		replacement.ExpiresAt = current.ExpiresAt
		return tx.Create(replacement).Error
	})
	if err == nil && reused {
		err = ErrRefreshTokenReused
	}
	return &current, err
}

func (dao *RefreshTokenSessionDAO) RevokeToken(ctx context.Context, tokenHash string) error {
	now := time.Now()
	return dao.db.WithContext(ctx).Model(&model.RefreshTokenSession{}).
		Where("token_hash = ? AND revoked_at IS NULL", tokenHash).
		Update("revoked_at", now).Error
}

// RevokeFamilyByTokenHash revokes every refresh token in the login session.
// Access JWTs carrying the same family ID become invalid immediately.
func (dao *RefreshTokenSessionDAO) RevokeFamilyByTokenHash(ctx context.Context, tokenHash string) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.RefreshTokenSession
		if err := tx.Select("family_id").Where("token_hash = ?", tokenHash).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		now := time.Now()
		return tx.Model(&model.RefreshTokenSession{}).
			Where("family_id = ? AND revoked_at IS NULL", session.FamilyID).
			Update("revoked_at", now).Error
	})
}

// IsFamilyActive performs an indexed existence lookup for access-token revocation.
func (dao *RefreshTokenSessionDAO) IsFamilyActive(ctx context.Context, userID uint, familyID string) (bool, error) {
	if familyID == "" {
		return false, nil
	}
	var count int64
	err := dao.db.WithContext(ctx).Model(&model.RefreshTokenSession{}).
		Where("user_id = ? AND family_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, familyID, time.Now()).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// FamilyRevokedReason returns the RevokedReason of the most recently revoked
// session in a family, for a family that is no longer active — lets
// JWTAuthMiddleware show "you were kicked" instead of a generic expired
// message when that's actually what happened. Empty string (with nil error)
// means either the family is still active or its reason wasn't tagged.
func (dao *RefreshTokenSessionDAO) FamilyRevokedReason(ctx context.Context, userID uint, familyID string) (string, error) {
	if familyID == "" {
		return "", nil
	}
	var session model.RefreshTokenSession
	err := dao.db.WithContext(ctx).
		Where("user_id = ? AND family_id = ? AND revoked_at IS NOT NULL", userID, familyID).
		Order("revoked_at DESC").
		Select("revoked_reason").
		First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return session.RevokedReason, nil
}

// HasActiveByClientType returns the most recently used active session for a
// user on a given client type ("web"), or nil if there is none —
// used at login time to detect "already signed in elsewhere on this client"
// before a new token pair is issued.
func (dao *RefreshTokenSessionDAO) HasActiveByClientType(ctx context.Context, userID uint, clientType string) (*model.RefreshTokenSession, error) {
	var session model.RefreshTokenSession
	err := dao.db.WithContext(ctx).
		Where("user_id = ? AND client_type = ? AND revoked_at IS NULL AND expires_at > ?", userID, clientType, time.Now()).
		Order("last_used_at DESC, created_at DESC").
		First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// RevokeActiveByClientType revokes every active session a user has on a
// given client type, tagging each with reason so the kicked-out session can
// later be told why (see FamilyRevokedReason) — the "force login, kick the
// other session" path.
func (dao *RefreshTokenSessionDAO) RevokeActiveByClientType(ctx context.Context, userID uint, clientType, reason string) error {
	now := time.Now()
	return dao.db.WithContext(ctx).Model(&model.RefreshTokenSession{}).
		Where("user_id = ? AND client_type = ? AND revoked_at IS NULL", userID, clientType).
		Updates(map[string]any{"revoked_at": now, "revoked_reason": reason}).Error
}

func (dao *RefreshTokenSessionDAO) RevokeUser(ctx context.Context, userID uint) error {
	now := time.Now()
	return dao.db.WithContext(ctx).Model(&model.RefreshTokenSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

// ListActiveByUser returns every not-revoked, not-expired session for a
// user, most-recently-used first — used to show "who's online, on which
// device" in the admin binding list. LastUsedAt only updates on access
// token refresh (not every request), so this is an approximation of
// real-time presence, not a heartbeat.
func (dao *RefreshTokenSessionDAO) ListActiveByUser(ctx context.Context, userID uint) ([]model.RefreshTokenSession, error) {
	var sessions []model.RefreshTokenSession
	err := dao.db.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("last_used_at DESC").
		Find(&sessions).Error
	return sessions, err
}

// ListActiveByUsers is ListActiveByUser for many users in one query,
// grouped by UserID — for pages that need every user's active sessions at
// once (e.g. the admin bindings list) instead of one query per row.
func (dao *RefreshTokenSessionDAO) ListActiveByUsers(ctx context.Context, userIDs []uint) (map[uint][]model.RefreshTokenSession, error) {
	result := make(map[uint][]model.RefreshTokenSession, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	var sessions []model.RefreshTokenSession
	if err := dao.db.WithContext(ctx).
		Where("user_id IN ? AND revoked_at IS NULL AND expires_at > ?", userIDs, time.Now()).
		Order("last_used_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	for _, s := range sessions {
		result[s.UserID] = append(result[s.UserID], s)
	}
	return result, nil
}
