package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// telegramAgentFreshness is how recently an agent must have reported for the
// platform to call it online.
const telegramAgentFreshness = 30 * time.Second

type TelegramAgentStatusDAO struct{ db *gorm.DB }

func NewTelegramAgentStatusDAO(db *gorm.DB) *TelegramAgentStatusDAO {
	return &TelegramAgentStatusDAO{db: db}
}

type TelegramAgentFilter struct {
	Keyword string
	Online  *bool
}

// Report stores one agent heartbeat (insert or update by agent id).
func (d *TelegramAgentStatusDAO) Report(ctx context.Context, status *model.TelegramAgentStatus) error {
	now := time.Now()
	status.SeenAt = &now
	return d.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "agent_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"updated_at", "version", "started_at", "seen_at", "accounts", "bots", "pending", "forwarded", "failed", "last_error", "jobs_json",
		}),
	}).Create(status).Error
}

func (d *TelegramAgentStatusDAO) List(ctx context.Context, page, pageSize int, f TelegramAgentFilter) ([]model.TelegramAgentStatus, int64, error) {
	page, pageSize = pageBounds(page, pageSize)
	var rows []model.TelegramAgentStatus
	db := d.db.WithContext(ctx).Model(&model.TelegramAgentStatus{})
	if f.Keyword != "" {
		db = db.Where("agent_id LIKE ?", like(f.Keyword))
	}
	cutoff := time.Now().Add(-telegramAgentFreshness)
	if f.Online != nil {
		if *f.Online {
			db = db.Where("seen_at >= ?", cutoff)
		} else {
			db = db.Where("seen_at IS NULL OR seen_at < ?", cutoff)
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	now := time.Now()
	for i := range rows {
		rows[i].Online = rows[i].SeenAt != nil && now.Sub(*rows[i].SeenAt) <= telegramAgentFreshness
	}
	return rows, total, nil
}

// Delete removes the heartbeat rows of agents that no longer exist.
func (d *TelegramAgentStatusDAO) Delete(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := d.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.TelegramAgentStatus{})
	return res.RowsAffected, res.Error
}
