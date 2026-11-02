package rdb

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
	"strings"
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

// RecordEntry appends a fully described attempt to the login history.
func (dao *LoginAuditLogDAO) RecordEntry(ctx context.Context, entry *model.LoginAuditLog) error {
	if len(entry.UserAgent) > 255 {
		entry.UserAgent = entry.UserAgent[:255]
	}
	return dao.db.WithContext(ctx).Create(entry).Error
}

// LoginAuditListFilter narrows List to what the login-log page filters on.
type LoginAuditListFilter struct {
	Username string
	ClientIP string
	Reason   string
	Method   string
	Success  *bool
	Since    *time.Time
	Until    *time.Time
	// SortBy is one of created_at, username, client_ip, success, reason; SortDesc reverses the default order.
	SortBy   string
	SortDesc bool
	Offset   int
	Limit    int
}

var loginAuditSortColumns = map[string]string{
	"created_at": "created_at", "username": "username", "client_ip": "client_ip", "success": "success", "reason": "reason", "method": "method",
}

// List pages the login history; filtering, ordering and paging all happen in the database.
func (dao *LoginAuditLogDAO) List(ctx context.Context, filter LoginAuditListFilter) ([]model.LoginAuditLog, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.LoginAuditLog{})
	if name := strings.TrimSpace(filter.Username); name != "" {
		q = q.Where("username LIKE ?", "%"+name+"%")
	}
	if ip := strings.TrimSpace(filter.ClientIP); ip != "" {
		q = q.Where("client_ip LIKE ?", "%"+ip+"%")
	}
	if filter.Reason != "" {
		q = q.Where("reason = ?", filter.Reason)
	}
	if filter.Method != "" {
		q = q.Where("method = ?", filter.Method)
	}
	if filter.Success != nil {
		q = q.Where("success = ?", *filter.Success)
	}
	if filter.Since != nil {
		q = q.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		q = q.Where("created_at <= ?", *filter.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	column, ok := loginAuditSortColumns[filter.SortBy]
	direction := " ASC"
	if !ok {
		column, direction = "created_at", " DESC"
	} else if filter.SortDesc {
		direction = " DESC"
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	var logs []model.LoginAuditLog
	err := q.Order(column + direction + ", id DESC").Offset(filter.Offset).Limit(limit).Find(&logs).Error
	return logs, total, err
}

// PurgeOlderThan deletes history older than cutoff in bounded batches and returns how many rows went.
func (dao *LoginAuditLogDAO) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	const batchSize = 500
	var total int64
	for {
		var ids []int64
		if err := dao.db.WithContext(ctx).Model(&model.LoginAuditLog{}).Where("created_at < ?", cutoff).Limit(batchSize).Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.LoginAuditLog{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < batchSize {
			return total, nil
		}
	}
}
