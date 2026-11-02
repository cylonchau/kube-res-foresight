package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// AllModules returns every module, enabled or not, without its sources: the home page counts them and looks at their state.
func (dao *ModuleDAO) AllModules(ctx context.Context) ([]*model.ForecastModule, error) {
	var list []*model.ForecastModule
	err := dao.db.WithContext(ctx).Order("id ASC").Find(&list).Error
	return list, err
}

// AllRunStates returns the remembered state of every module that has one, by module id.
func (dao *RunStateDAO) AllRunStates(ctx context.Context) (map[uint]*model.ModuleRunState, error) {
	var list []*model.ModuleRunState
	if err := dao.db.WithContext(ctx).Find(&list).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]*model.ModuleRunState, len(list))
	for _, st := range list {
		out[st.ModuleID] = st
	}
	return out, nil
}

// LatestRecommendations returns the newest recommendation of every module that has one, by module id (one query, whatever the number
// of modules).
func (dao *PredictionDAO) LatestRecommendations(ctx context.Context) (map[uint]*model.Recommendation, error) {
	newest := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Select("MAX(id)").Group("module_id")
	var list []*model.Recommendation
	if err := dao.db.WithContext(ctx).Where("id IN (?)", newest).Find(&list).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]*model.Recommendation, len(list))
	for _, r := range list {
		out[r.ModuleID] = r
	}
	return out, nil
}

// AppliedExecutionsInWindow are the replica changes the operator made, for every module, at or after a time, oldest first.
func (dao *PredictionDAO) AppliedExecutionsInWindow(ctx context.Context, since time.Time) ([]*model.ScalingExecution, error) {
	var list []*model.ScalingExecution
	err := dao.db.WithContext(ctx).Where("result = ? AND created_at >= ?", model.ExecApplied, since).Order("created_at ASC, id ASC").Limit(20000).Find(&list).Error
	return list, err
}

// CountFailedTrainings counts the forecast runs that failed at or after a time, for every module.
func (dao *PredictionDAO) CountFailedTrainings(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.TrainingRun{}).Where("status = ? AND created_at >= ?", "failed", since).Count(&n).Error
	return n, err
}
