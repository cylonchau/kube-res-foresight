package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
	"time"
)

type OperationLogDAO struct{ db *gorm.DB }

func NewOperationLogDAO(db *gorm.DB) *OperationLogDAO {
	return &OperationLogDAO{db: db}
}

func (dao *OperationLogDAO) Create(ctx context.Context, log *model.OperationLog) error {
	return dao.db.WithContext(ctx).Create(log).Error
}

// OperationLogListFilter narrows List to the filters the "系统管理/操作日志"
// page exposes: source (ClientType), a free-text keyword matched against
// username/path/client IP, success/failure, and a created-at range.
type OperationLogListFilter struct {
	ClientType string
	Keyword    string
	Success    *bool
	Since      *time.Time
	Until      *time.Time
	Offset     int
	Limit      int
}

func (dao *OperationLogDAO) List(ctx context.Context, filter OperationLogListFilter) ([]model.OperationLog, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.OperationLog{})
	if filter.ClientType != "" {
		q = q.Where("client_type = ?", filter.ClientType)
	}
	if filter.Keyword != "" {
		like := "%" + filter.Keyword + "%"
		q = q.Where("username LIKE ? OR path LIKE ? OR client_ip LIKE ? OR request_id LIKE ? OR client_name LIKE ?", like, like, like, like, like)
	}
	if filter.Success != nil {
		if *filter.Success {
			q = q.Where("status_code < ?", 400)
		} else {
			q = q.Where("status_code >= ?", 400)
		}
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

	var logs []model.OperationLog
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	err := q.Order("created_at DESC").Offset(filter.Offset).Limit(limit).Find(&logs).Error
	return logs, total, err
}

// DeleteAll removes every operation_log row — used by the page's "清空"
// button when an admin wants a clean slate (e.g. after clearing out noise
// from before a filtering rule existed) rather than waiting for the
// retention job to age rows out on its own schedule.
func (dao *OperationLogDAO) DeleteAll(ctx context.Context) error {
	return dao.db.WithContext(ctx).Where("1 = 1").Delete(&model.OperationLog{}).Error
}

// PurgeOlderThan deletes operation_log rows older than cutoff in bounded
// batches so the retention job never holds one giant DELETE lock. Returns
// the total number of rows removed.
func (dao *OperationLogDAO) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	const batchSize = 500
	var total int64
	for {
		res := dao.db.WithContext(ctx).
			Where("created_at < ?", cutoff).
			Limit(batchSize).
			Delete(&model.OperationLog{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < batchSize {
			return total, nil
		}
	}
}
