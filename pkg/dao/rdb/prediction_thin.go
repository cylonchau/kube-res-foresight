package rdb

import (
	"context"
	"gorm.io/gorm"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// thinWatermarkKey is where the clean-up remembers how far it has thinned the forecast table: every row created up to it has been looked at. It is
// kept in the system parameter table so a restart does not start over (the table has hundreds of millions of rows), and it is not a parameter anyone sets.
const thinWatermarkKey = "forecast.prediction_thinned_until"

const (
	// thinSlice is how much creation time one look takes, and thinBatch how many rows one delete removes.
	thinSlice = 15 * time.Minute
	thinBatch = 10000
)

func (dao *PredictionDAO) thinWatermark(ctx context.Context) (time.Time, bool) {
	var cfg model.SysConfig
	if err := dao.db.WithContext(ctx).Where("config_key = ?", thinWatermarkKey).First(&cfg).Error; err != nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(cfg.ConfigValue, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

func (dao *PredictionDAO) saveThinWatermark(ctx context.Context, at time.Time) error {
	value := strconv.FormatInt(at.Unix(), 10)
	res := dao.db.WithContext(ctx).Model(&model.SysConfig{}).Where("config_key = ?", thinWatermarkKey).Update("config_value", value)
	if res.Error != nil || res.RowsAffected > 0 {
		return res.Error
	}
	return dao.db.WithContext(ctx).Create(&model.SysConfig{ConfigKey: thinWatermarkKey, ConfigValue: value, ConfigName: "Forecast thinning watermark (internal)",
		Remark: "Set by the clean-up: the forecasts created up to this time (unix seconds) have been thinned. Do not edit."}).Error
}

// supersededIDs are the ids of at most limit forecasts created in (from, to] for which a newer run has made a forecast of the same target by
// the same algorithm at or before the target: the forecast that applied to the target is the newest of them, the others are never read.
// A forecast made after its target (hindsight) is never replaced by this, and replaces nothing.
func (dao *PredictionDAO) supersededIDs(ctx context.Context, from, to time.Time, limit int) ([]uint, error) {
	var ids []uint
	err := dao.db.WithContext(ctx).Raw(`SELECT p.id FROM forecast_prediction p WHERE p.created_at > ? AND p.created_at <= ? AND p.prediction_time >= ? AND p.prediction_time <= ? AND EXISTS (
		SELECT 1 FROM forecast_prediction n WHERE n.module_id = p.module_id AND n.metric = p.metric AND n.target_time = p.target_time
		AND n.algorithm_id = p.algorithm_id AND n.quantile_permille = p.quantile_permille
		AND n.prediction_time > p.prediction_time AND n.prediction_time <= p.target_time) LIMIT ?`, from, to, from.Add(-rowDayMargin), to.Add(rowDayMargin), limit).Scan(&ids).Error
	return ids, err
}

// ThinPredictions deletes the forecasts created before cutoff that a newer run replaced (see supersededIDs), oldest first, in slices of creation time
// and batches of ten thousand rows, for at most budget; the next run goes on from where this one stopped. It returns how many rows went. The
// forecasts of the last cutoff are not touched: the evaluation scores whole runs, and has had the time to by then.
func (dao *PredictionDAO) ThinPredictions(ctx context.Context, cutoff time.Time, budget time.Duration) (int64, error) {
	mark, ok := dao.thinWatermark(ctx)
	if !ok {
		var oldest model.Prediction
		err := dao.db.WithContext(ctx).Select("id", "created_at").Order("created_at ASC").Limit(1).Take(&oldest).Error
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		mark = oldest.CreatedAt.UTC().Add(-time.Second)
	}
	deadline := batchClock().Add(budget)
	var total int64
	for mark.Before(cutoff) {
		end := mark.Add(thinSlice)
		if end.After(cutoff) {
			end = cutoff
		}
		for {
			ids, err := dao.supersededIDs(ctx, mark, end, thinBatch)
			if err != nil {
				return total, err
			}
			if len(ids) > 0 {
				res := dao.db.WithContext(ctx).Where("id IN ? AND prediction_time >= ? AND prediction_time <= ?", ids, mark.Add(-rowDayMargin), end.Add(rowDayMargin)).Delete(&model.Prediction{})
				total += res.RowsAffected
				if res.Error != nil {
					return total, res.Error
				}
			}
			if len(ids) < thinBatch {
				break
			}
			if ctx.Err() != nil || (budget > 0 && batchClock().After(deadline)) {
				return total, dao.saveThinWatermark(ctx, mark) // the slice is not finished: it is looked at again next time
			}
		}
		mark = end
		if err := dao.saveThinWatermark(ctx, mark); err != nil {
			return total, err
		}
		if ctx.Err() != nil || (budget > 0 && batchClock().After(deadline)) {
			break
		}
	}
	return total, nil
}
