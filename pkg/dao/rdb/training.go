package rdb

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kubeforecast/pkg/model"
)

// TrainingFilter selects training records.
type TrainingFilter struct {
	ModuleID    uint
	Metric      string
	AlgorithmID string
	Status      string
	From, To    *time.Time // on prediction_time
}

// SaveTraining stores the record of one run.
func (dao *PredictionDAO) SaveTraining(ctx context.Context, t *model.TrainingRun) error {
	return dao.db.WithContext(ctx).Create(t).Error
}

// filtered applies the filter of the training records to a query on them.
func (f TrainingFilter) filtered(q *gorm.DB) *gorm.DB {
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Metric != "" {
		q = q.Where("metric = ?", f.Metric)
	}
	if f.AlgorithmID != "" {
		q = q.Where("algorithm_id = ?", f.AlgorithmID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.From != nil {
		q = q.Where("prediction_time >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("prediction_time <= ?", *f.To)
	}
	return q
}

// ListTrainings pages the records, newest first.
func (dao *PredictionDAO) ListTrainings(ctx context.Context, f TrainingFilter, page, pageSize int) ([]*model.TrainingRun, int64, error) {
	q := f.filtered(dao.db.WithContext(ctx).Model(&model.TrainingRun{}))
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.TrainingRun
	err := q.Order("prediction_time DESC, id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// GetTraining returns one record.
func (dao *PredictionDAO) GetTraining(ctx context.Context, id uint) (*model.TrainingRun, error) {
	var t model.TrainingRun
	if err := dao.db.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// LatestTrainingAt is the time of the newest run recorded for a module; ok is false when it has none.
func (dao *PredictionDAO) LatestTrainingAt(ctx context.Context, moduleID uint) (time.Time, bool, error) {
	var t model.TrainingRun
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("prediction_time DESC").Limit(1).Find(&t).Error
	if err != nil || t.ID == 0 {
		return time.Time{}, false, err
	}
	return t.PredictionTime, true, nil
}

// TrainingAlgorithmCount is how the runs of one algorithm ended.
type TrainingAlgorithmCount struct {
	AlgorithmID string `json:"algorithm_id"`
	OK          int64  `json:"ok"`
	Skipped     int64  `json:"skipped"`
	Failed      int64  `json:"failed"`
}

// TrainingReasonCount is a reason a run did not produce a forecast and how often it happened.
type TrainingReasonCount struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// TrainingSummary answers what the records are for: did the algorithms run, and when they did not, why.
type TrainingSummary struct {
	Total         int64                    `json:"total"`
	OK            int64                    `json:"ok"`
	Skipped       int64                    `json:"skipped"`
	Failed        int64                    `json:"failed"`
	AvgDurationMs float64                  `json:"avg_duration_ms"`
	LastAt        *time.Time               `json:"last_at"`
	Algorithms    []TrainingAlgorithmCount `json:"algorithms"`
	Reasons       []TrainingReasonCount    `json:"reasons"`
}

// SummarizeTrainings counts the records of the filter by how they ended, per algorithm, and lists the most common
// reasons for a skipped or failed run.
func (dao *PredictionDAO) SummarizeTrainings(ctx context.Context, f TrainingFilter) (*TrainingSummary, error) {
	base := func() *gorm.DB { return f.filtered(dao.db.WithContext(ctx).Model(&model.TrainingRun{})) }
	out := &TrainingSummary{Algorithms: []TrainingAlgorithmCount{}, Reasons: []TrainingReasonCount{}}

	var byAlgorithm []struct {
		AlgorithmID string
		Status      string
		N           int64
	}
	if err := base().Select("algorithm_id, status, COUNT(*) AS n").Group("algorithm_id, status").Order("algorithm_id").Scan(&byAlgorithm).Error; err != nil {
		return nil, err
	}
	index := map[string]int{}
	for _, row := range byAlgorithm {
		i, ok := index[row.AlgorithmID]
		if !ok {
			i = len(out.Algorithms)
			index[row.AlgorithmID] = i
			out.Algorithms = append(out.Algorithms, TrainingAlgorithmCount{AlgorithmID: row.AlgorithmID})
		}
		switch row.Status {
		case "ok":
			out.Algorithms[i].OK += row.N
			out.OK += row.N
		case "skipped":
			out.Algorithms[i].Skipped += row.N
			out.Skipped += row.N
		default:
			out.Algorithms[i].Failed += row.N
			out.Failed += row.N
		}
		out.Total += row.N
	}

	var stats struct {
		Avg  float64
		Last *string
	}
	if err := base().Select("COALESCE(AVG(duration_ms), 0) AS avg").Scan(&stats).Error; err != nil {
		return nil, err
	}
	out.AvgDurationMs = stats.Avg
	var last model.TrainingRun
	if err := base().Order("prediction_time DESC").Limit(1).Find(&last).Error; err != nil {
		return nil, err
	}
	if last.ID != 0 {
		at := last.PredictionTime
		out.LastAt = &at
	}

	if err := base().Select("status, reason, COUNT(*) AS count").Where("status <> ? AND reason <> ''", "ok").
		Group("status, reason").Order("count DESC").Limit(5).Scan(&out.Reasons).Error; err != nil {
		return nil, err
	}
	return out, nil
}
