package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// RecommendationsSince are the recommendations a module made at or after a time, oldest first, bounded to 5000 rows.
func (dao *PredictionDAO) RecommendationsSince(ctx context.Context, moduleID uint, since time.Time) ([]*model.Recommendation, error) {
	var list []*model.Recommendation
	err := dao.db.WithContext(ctx).Where("module_id = ? AND created_at >= ?", moduleID, since).Order("created_at ASC, id ASC").Limit(5000).Find(&list).Error
	return list, err
}

// AppliedExecutionsSince are the replica changes the operator made for a module at or after a time, oldest first.
func (dao *PredictionDAO) AppliedExecutionsSince(ctx context.Context, moduleID uint, since time.Time) ([]*model.ScalingExecution, error) {
	var list []*model.ScalingExecution
	err := dao.db.WithContext(ctx).Where("module_id = ? AND result = ? AND created_at >= ?", moduleID, model.ExecApplied, since).
		Order("created_at ASC, id ASC").Limit(5000).Find(&list).Error
	return list, err
}

// TrainingsSince are the training records of one algorithm of a module at or after a time, newest first, bounded to 500 rows.
func (dao *PredictionDAO) TrainingsSince(ctx context.Context, moduleID uint, algorithmID string, since time.Time) ([]*model.TrainingRun, error) {
	var list []*model.TrainingRun
	err := dao.db.WithContext(ctx).Where("module_id = ? AND algorithm_id = ? AND created_at >= ?", moduleID, algorithmID, since).
		Order("created_at DESC, id DESC").Limit(500).Find(&list).Error
	return list, err
}
