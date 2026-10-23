package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"time"
)

type LoginAuditLogDAO struct {
	db *gorm.DB
}

func NewLoginAuditLogDAO(db *gorm.DB) *LoginAuditLogDAO {
	return &LoginAuditLogDAO{db: db}
}

func (dao *LoginAuditLogDAO) Record(ctx context.Context, username, clientIP string, success bool) error {
	return dao.db.WithContext(ctx).Create(&model.LoginAuditLog{
		Username: username, ClientIP: clientIP, Success: success,
	}).Error
}

// CountFailuresSince returns the number of failed login attempts recorded
// since `since` — used by the /report weekly summary as "suspicious logins".
func (dao *LoginAuditLogDAO) CountFailuresSince(ctx context.Context, since time.Time) (int64, error) {
	var count int64
	err := dao.db.WithContext(ctx).Model(&model.LoginAuditLog{}).
		Where("success = ? AND created_at >= ?", false, since).
		Count(&count).Error
	return count, err
}

// LatestSuccessByUsername returns the timestamp of a user's most recent
// successful login, or nil if there's no recorded success.
func (dao *LoginAuditLogDAO) LatestSuccessByUsername(ctx context.Context, username string) (*time.Time, error) {
	log, err := dao.LatestSuccessRecordByUsername(ctx, username)
	if err != nil || log == nil {
		return nil, err
	}
	return &log.CreatedAt, nil
}

// LatestSuccessRecordByUsername returns a user's most recent successful
// login record in full (including ClientIP), or nil if there's no recorded
// success. Use this over LatestSuccessByUsername when the caller also needs
// to show where the login came from (e.g. the user list's "last login IP"
// column, or a user's own "this session" dashboard card).
func (dao *LoginAuditLogDAO) LatestSuccessRecordByUsername(ctx context.Context, username string) (*model.LoginAuditLog, error) {
	var log model.LoginAuditLog
	err := dao.db.WithContext(ctx).
		Where("username = ? AND success = ?", username, true).
		Order("created_at DESC").
		First(&log).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &log, nil
}

// LatestSuccessByUsernames returns each username's most recent successful
// login timestamp in one query, for pages that need this for many users at
// once (e.g. the admin bindings list) instead of one query per row.
// Usernames with no recorded success are simply absent from the result map.
func (dao *LoginAuditLogDAO) LatestSuccessByUsernames(ctx context.Context, usernames []string) (map[string]*time.Time, error) {
	result := make(map[string]*time.Time, len(usernames))
	if len(usernames) == 0 {
		return result, nil
	}
	var logs []model.LoginAuditLog
	if err := dao.db.WithContext(ctx).
		Where("username IN ? AND success = ?", usernames, true).
		Order("created_at DESC").
		Find(&logs).Error; err != nil {
		return nil, err
	}
	for _, log := range logs {
		if _, seen := result[log.Username]; !seen {
			t := log.CreatedAt
			result[log.Username] = &t
		}
	}
	return result, nil
}
