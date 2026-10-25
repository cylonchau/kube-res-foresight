package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// QueryRowLimit bounds the rows one metric query reads; a query that hits it reports itself truncated.
const QueryRowLimit = 200000

// PredictionsBetween returns the predictions whose target time is in [from, to], oldest prediction first so a
// later prediction of the same target overwrites an earlier one when the caller keeps the latest. algorithm
// and metric are optional filters. The second result is true when the row limit cut the result short.
func (dao *PredictionDAO) PredictionsBetween(ctx context.Context, instanceID uint, metric, algorithm string, from, to time.Time) ([]model.Prediction, bool, error) {
	q := dao.db.WithContext(ctx).Model(&model.Prediction{}).
		Select("metric, algorithm_id, prediction_time, target_time, quantile_permille, value").
		Where("module_id = ? AND target_time >= ? AND target_time <= ?", instanceID, from, to)
	if metric != "" {
		q = q.Where("metric = ?", metric)
	}
	if algorithm != "" {
		q = q.Where("algorithm_id = ?", algorithm)
	}
	var rows []model.Prediction
	err := q.Order("prediction_time ASC, target_time ASC").Limit(QueryRowLimit + 1).Find(&rows).Error
	return trimRows(rows, err)
}

// EvaluationsBetween returns the scored prediction runs whose prediction time is in [from, to], oldest first.
func (dao *PredictionDAO) EvaluationsBetween(ctx context.Context, instanceID uint, metric, algorithm string, from, to time.Time) ([]model.PredictionEvaluation, bool, error) {
	q := dao.db.WithContext(ctx).Model(&model.PredictionEvaluation{}).
		Where("module_id = ? AND prediction_time >= ? AND prediction_time <= ? AND points > 0", instanceID, from, to)
	if metric != "" {
		q = q.Where("metric = ?", metric)
	}
	if algorithm != "" {
		q = q.Where("algorithm_id = ?", algorithm)
	}
	var rows []model.PredictionEvaluation
	err := q.Order("prediction_time ASC").Limit(QueryRowLimit + 1).Find(&rows).Error
	return trimRows(rows, err)
}

// BacktestsBetween returns the backtest runs created in [from, to], oldest first.
func (dao *PredictionDAO) BacktestsBetween(ctx context.Context, instanceID uint, metric, algorithm string, from, to time.Time) ([]model.BacktestRun, bool, error) {
	q := dao.db.WithContext(ctx).Model(&model.BacktestRun{}).
		Where("module_id = ? AND created_at >= ? AND created_at <= ?", instanceID, from, to)
	if metric != "" {
		q = q.Where("metric = ?", metric)
	}
	if algorithm != "" {
		q = q.Where("algorithm_id = ?", algorithm)
	}
	var rows []model.BacktestRun
	err := q.Order("created_at ASC").Limit(QueryRowLimit + 1).Find(&rows).Error
	return trimRows(rows, err)
}

// RecommendationsBetween returns the recommendation versions created in [from, to], oldest first.
func (dao *PredictionDAO) RecommendationsBetween(ctx context.Context, moduleID uint, from, to time.Time) ([]model.Recommendation, bool, error) {
	var rows []model.Recommendation
	err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Omit("detail").
		Where("module_id = ? AND created_at >= ? AND created_at <= ?", moduleID, from, to).
		Order("created_at ASC").Limit(QueryRowLimit + 1).Find(&rows).Error
	return trimRows(rows, err)
}

// ExecutionsBetween returns the operator reports created in [from, to], oldest first.
func (dao *PredictionDAO) ExecutionsBetween(ctx context.Context, moduleID uint, from, to time.Time) ([]model.ScalingExecution, bool, error) {
	var rows []model.ScalingExecution
	err := dao.db.WithContext(ctx).Model(&model.ScalingExecution{}).
		Where("module_id = ? AND created_at >= ? AND created_at <= ?", moduleID, from, to).
		Order("created_at ASC").Limit(QueryRowLimit + 1).Find(&rows).Error
	return trimRows(rows, err)
}

func trimRows[T any](rows []T, err error) ([]T, bool, error) {
	if err != nil {
		return nil, false, err
	}
	if len(rows) > QueryRowLimit {
		return rows[:QueryRowLimit], true, nil
	}
	return rows, false, nil
}
