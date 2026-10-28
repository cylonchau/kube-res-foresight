package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// SaveOutcome records a scale-up decided ahead of the load.
func (dao *PredictionDAO) SaveOutcome(ctx context.Context, o *model.ScaleOutcome) error {
	return dao.db.WithContext(ctx).Create(o).Error
}

// UpdateOutcome stores the evaluation of an outcome.
func (dao *PredictionDAO) UpdateOutcome(ctx context.Context, o *model.ScaleOutcome) error {
	return dao.db.WithContext(ctx).Save(o).Error
}

// OpenOutcomes are the scale-ups of a module whose verdict can still change, oldest first.
func (dao *PredictionDAO) OpenOutcomes(ctx context.Context, moduleID uint) ([]*model.ScaleOutcome, error) {
	var list []*model.ScaleOutcome
	err := dao.db.WithContext(ctx).Where("module_id = ? AND final = ?", moduleID, false).Order("decided_at ASC").Find(&list).Error
	return list, err
}

// RecentOutcomes are the newest scale-ups of a module, newest first; finalOnly keeps the judged ones.
func (dao *PredictionDAO) RecentOutcomes(ctx context.Context, moduleID uint, limit int, finalOnly bool) ([]*model.ScaleOutcome, error) {
	q := dao.db.WithContext(ctx).Where("module_id = ?", moduleID)
	if finalOnly {
		q = q.Where("final = ?", true)
	}
	var list []*model.ScaleOutcome
	err := q.Order("decided_at DESC").Limit(limit).Find(&list).Error
	return list, err
}

// ListOutcomes pages the scale-ups of a module (all modules when moduleID is 0), newest first.
func (dao *PredictionDAO) ListOutcomes(ctx context.Context, moduleID uint, verdict string, page, pageSize int) ([]*model.ScaleOutcome, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.ScaleOutcome{})
	if moduleID != 0 {
		q = q.Where("module_id = ?", moduleID)
	}
	if verdict != "" {
		q = q.Where("verdict = ?", verdict)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.ScaleOutcome
	err := q.Order("decided_at DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// PruneOutcomes deletes judged scale-ups older than the cutoff and returns how many.
func (dao *PredictionDAO) PruneOutcomes(ctx context.Context, before time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("final = ? AND decided_at < ?", true, before).Delete(&model.ScaleOutcome{})
	return res.RowsAffected, res.Error
}

// OutcomesSince are the scale-ups a module decided at or after a time, oldest first.
func (dao *PredictionDAO) OutcomesSince(ctx context.Context, moduleID uint, since time.Time) ([]*model.ScaleOutcome, error) {
	var list []*model.ScaleOutcome
	err := dao.db.WithContext(ctx).Where("module_id = ? AND decided_at >= ?", moduleID, since).Order("decided_at ASC").Find(&list).Error
	return list, err
}
