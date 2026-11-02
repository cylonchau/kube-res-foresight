package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// PruneScores keeps the newest keep evaluations of a module and deletes the older ones. An evaluation is the set of scores
// written at one time (one per algorithm), so the cap counts evaluations, not rows. It returns how many rows were removed.
func (dao *PredictionDAO) PruneScores(ctx context.Context, moduleID uint, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	var cut []time.Time
	if err := dao.db.WithContext(ctx).Model(&model.AlgorithmScore{}).Where("module_id = ?", moduleID).
		Distinct().Order("created_at DESC").Offset(keep).Limit(1).Pluck("created_at", &cut).Error; err != nil {
		return 0, err
	}
	if len(cut) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("module_id = ? AND created_at <= ?", moduleID, cut[0]).Delete(&model.AlgorithmScore{})
	return res.RowsAffected, res.Error
}
