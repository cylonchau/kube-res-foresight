package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type NotificationDAO struct {
	db *gorm.DB
}

func NewNotificationDAO(db *gorm.DB) *NotificationDAO {
	return &NotificationDAO{db: db}
}

// RecordDelivery creates or updates a notification delivery attempt record.
func (dao *NotificationDAO) RecordDelivery(ctx context.Context, record *model.NotificationDelivery) error {
	if record == nil || record.NotificationID == "" {
		return errors.New("notification id is required")
	}
	if record.Status == "" {
		record.Status = "PENDING"
	}
	return dao.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "notification_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"status", "retry_count", "last_error", "sent_at", "acked_at", "updated_at",
		}),
	}).Create(record).Error
}

// GetByNotificationID fetches a notification delivery by its unique ID.
func (dao *NotificationDAO) GetByNotificationID(ctx context.Context, notificationID string) (*model.NotificationDelivery, error) {
	var row model.NotificationDelivery
	err := dao.db.WithContext(ctx).Where("notification_id = ?", strings.TrimSpace(notificationID)).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// IsDeduped returns true if an identical dedup key was successfully recorded within the specified window.
func (dao *NotificationDAO) IsDeduped(ctx context.Context, dedupKey string, window time.Duration) bool {
	if dedupKey == "" || window <= 0 {
		return false
	}
	threshold := time.Now().UTC().Add(-window)
	var count int64
	err := dao.db.WithContext(ctx).
		Model(&model.NotificationDelivery{}).
		Where("dedup_key = ? AND created_at >= ? AND status != ?", dedupKey, threshold, "FAILED").
		Count(&count).Error
	return err == nil && count > 0
}

// AckNotification marks a notification as acknowledged (e.g. by on-call operator or webhook callback).
func (dao *NotificationDAO) AckNotification(ctx context.Context, notificationID string) error {
	result := dao.db.WithContext(ctx).
		Model(&model.NotificationDelivery{}).
		Where("notification_id = ?", strings.TrimSpace(notificationID)).
		Updates(map[string]interface{}{
			"status":   "ACKED",
			"acked_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateStatus updates the delivery status, retry count and last error.
func (dao *NotificationDAO) UpdateStatus(ctx context.Context, notificationID, status, lastError string) error {
	updates := map[string]interface{}{
		"status":     status,
		"last_error": lastError,
	}
	if status == "SENT" {
		updates["sent_at"] = time.Now().UTC()
	}
	return dao.db.WithContext(ctx).
		Model(&model.NotificationDelivery{}).
		Where("notification_id = ?", strings.TrimSpace(notificationID)).
		Updates(updates).Error
}

// GetUnackedDeliveries retrieves high-severity notifications sent before olderThan that have not yet been ACKed.
func (dao *NotificationDAO) GetUnackedDeliveries(ctx context.Context, severity string, olderThan time.Duration) ([]model.NotificationDelivery, error) {
	threshold := time.Now().UTC().Add(-olderThan)
	var rows []model.NotificationDelivery
	query := dao.db.WithContext(ctx).
		Where("status = ? AND sent_at <= ?", "SENT", threshold)
	if severity != "" {
		query = query.Where("severity = ?", severity)
	}
	err := query.Find(&rows).Error
	return rows, err
}

// ListDeliveries retrieves the most recent notification deliveries.
func (dao *NotificationDAO) ListDeliveries(ctx context.Context, limit int) ([]model.NotificationDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []model.NotificationDelivery
	err := dao.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

type NotificationStats struct {
	Total     int64 `json:"total"`
	Sent      int64 `json:"sent"`
	Acked     int64 `json:"acked"`
	Escalated int64 `json:"escalated"`
	Failed    int64 `json:"failed"`
	UnackedP0 int64 `json:"unacked_p0"`
	// Unacked counts the critical and high notifications (P0, P1) still waiting for an operator. Lower
	// levels are records of what was sent; nobody has to acknowledge them.
	Unacked int64 `json:"unacked"`
}

// StatusUnacked is a status filter that stands for the deliveries that need an operator: critical and high
// notifications (P0, P1) nobody has acknowledged yet. An unacknowledged one is escalated to a second
// channel after a while, which is why somebody has to say they have seen it.
const StatusUnacked = "UNACKED"

var (
	unackedStatuses = []string{"PENDING", "SENT", "ESCALATED"}
	needsAckLevels  = []string{"P0", "P1"}
)

func filterDeliveries(db *gorm.DB, channel, severity, status, keyword string) *gorm.DB {
	if channel != "" {
		db = db.Where("channel = ?", channel)
	}
	if severity != "" {
		db = db.Where("severity = ?", severity)
	}
	switch status {
	case "":
	case StatusUnacked:
		db = db.Where("status IN ? AND severity IN ?", unackedStatuses, needsAckLevels)
	default:
		db = db.Where("status = ?", status)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		db = db.Where("(title LIKE ? OR content LIKE ? OR recipient LIKE ? OR notification_id LIKE ? OR dedup_key LIKE ?)", like, like, like, like, like)
	}
	return db
}

// AckMany acknowledges the given notifications and returns how many changed.
func (dao *NotificationDAO) AckMany(ctx context.Context, notificationIDs []string) (int64, error) {
	if len(notificationIDs) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).
		Where("notification_id IN ? AND status IN ?", notificationIDs, unackedStatuses).
		Updates(map[string]interface{}{"status": "ACKED", "acked_at": time.Now().UTC()})
	return res.RowsAffected, res.Error
}

// AckMatching acknowledges every unacknowledged delivery that matches the filters, on every page.
func (dao *NotificationDAO) AckMatching(ctx context.Context, channel, severity, keyword string) (int64, error) {
	q := filterDeliveries(dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}), channel, severity, StatusUnacked, keyword)
	res := q.Updates(map[string]interface{}{"status": "ACKED", "acked_at": time.Now().UTC()})
	return res.RowsAffected, res.Error
}

// ListDeliveriesPaginated retrieves paginated notification deliveries with optional filters.
func (dao *NotificationDAO) ListDeliveriesPaginated(ctx context.Context, page, pageSize int, channel, severity, status, keyword string) ([]model.NotificationDelivery, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	query := filterDeliveries(dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}), channel, severity, status, keyword)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.NotificationDelivery
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// GetStats returns delivery aggregate statistics.
func (dao *NotificationDAO) GetStats(ctx context.Context) (*NotificationStats, error) {
	stats := &NotificationStats{}
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Count(&stats.Total).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status = ?", "SENT").Count(&stats.Sent).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status = ?", "ACKED").Count(&stats.Acked).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status = ?", "ESCALATED").Count(&stats.Escalated).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status = ?", "FAILED").Count(&stats.Failed).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status = ? AND severity IN ?", "SENT", []string{"P0", "P1"}).Count(&stats.UnackedP0).Error
	_ = dao.db.WithContext(ctx).Model(&model.NotificationDelivery{}).Where("status IN ? AND severity IN ?", unackedStatuses, needsAckLevels).Count(&stats.Unacked).Error
	return stats, nil
}
