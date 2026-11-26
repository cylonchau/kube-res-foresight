package rdb

import (
	"context"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// coarsenWatermarkKey is where the clean-up remembers how far it has coarsened the forecast table: every row created up to it has been looked at. Like the thinning
// watermark it lives in the system parameter table, so a restart does not start over, and it is not a parameter anyone sets.
const coarsenWatermarkKey = "forecast.prediction_coarsened_until"

const (
	// coarsenSlice is how much creation time one look takes. A forecast is made up to a horizon ahead of its creation, so the forecasts of one stretch of target time are
	// created over a stretch a little longer than it: a slice much longer than the stretch kept (fifteen minutes) keeps the rows of a stretch that falls on the edge of two slices
	// down to a few more than one.
	coarsenSlice = time.Hour
	// coarsenRead is how many rows one read of a slice takes (the ids and the keys, not the values).
	coarsenRead = 50000
)

func (dao *PredictionDAO) coarsenWatermark(ctx context.Context) (time.Time, bool) {
	var cfg model.SysConfig
	if err := dao.db.WithContext(ctx).Where("config_key = ?", coarsenWatermarkKey).First(&cfg).Error; err != nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(cfg.ConfigValue, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

func (dao *PredictionDAO) saveCoarsenWatermark(ctx context.Context, at time.Time) error {
	value := strconv.FormatInt(at.Unix(), 10)
	res := dao.db.WithContext(ctx).Model(&model.SysConfig{}).Where("config_key = ?", coarsenWatermarkKey).Update("config_value", value)
	if res.Error != nil || res.RowsAffected > 0 {
		return res.Error
	}
	return dao.db.WithContext(ctx).Create(&model.SysConfig{ConfigKey: coarsenWatermarkKey, ConfigValue: value, ConfigName: "Forecast coarsening watermark (internal)",
		Remark: "Set by the clean-up: the forecasts created up to this time (unix seconds) have been reduced to the resolution kept for old forecasts. Do not edit."}).Error
}

// coarsenKey is one algorithm's forecast of one metric of one module, in one stretch of target time of the length kept.
type coarsenKey struct {
	module    uint
	metric    string
	algorithm string
	quantile  int
	stretch   int64
}

// coarsenRow is what is read of a forecast to decide whether it stays.
type coarsenRow struct {
	ID               uint
	ModuleID         uint
	Metric           string
	AlgorithmID      string
	QuantilePermille int
	TargetTime       time.Time
}

// coarsenSliceIDs are the ids of the forecasts created in (from, to] that are not the first of their stretch of target time: for each algorithm and metric of a module one
// forecast stays in every `every` of target time, the one with the earliest target (the lowest id when two have the same), and the others go.
func (dao *PredictionDAO) coarsenSliceIDs(ctx context.Context, from, to time.Time, every time.Duration) ([]uint, error) {
	size := int64(every / time.Second)
	if size < 1 {
		return nil, nil
	}
	best := map[coarsenKey]coarsenRow{}
	var all []coarsenRow
	var last uint
	for {
		var page []coarsenRow
		err := dao.db.WithContext(ctx).Model(&model.Prediction{}).Select("id", "module_id", "metric", "algorithm_id", "quantile_permille", "target_time").
			Where("created_at > ? AND created_at <= ? AND prediction_time >= ? AND prediction_time <= ? AND id > ?", from, to, from.Add(-rowDayMargin), to.Add(rowDayMargin), last).Order("id ASC").Limit(coarsenRead).Find(&page).Error
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			all = append(all, r)
			k := coarsenKey{r.ModuleID, r.Metric, r.AlgorithmID, r.QuantilePermille, r.TargetTime.Unix() / size}
			if cur, ok := best[k]; !ok || r.TargetTime.Before(cur.TargetTime) || (r.TargetTime.Equal(cur.TargetTime) && r.ID < cur.ID) {
				best[k] = r
			}
		}
		if len(page) < coarsenRead {
			break
		}
		last = page[len(page)-1].ID
	}
	var doomed []uint
	for _, r := range all {
		k := coarsenKey{r.ModuleID, r.Metric, r.AlgorithmID, r.QuantilePermille, r.TargetTime.Unix() / size}
		if best[k].ID != r.ID {
			doomed = append(doomed, r.ID)
		}
	}
	return doomed, nil
}

// CoarsenPredictions reduces the forecasts created before cutoff to one in every `every` of target time for each algorithm and metric of a module, oldest first, in slices of
// creation time, for at most budget; the next run goes on from where this one stopped. It returns how many rows went. The forecasts newer than cutoff are not touched: they are
// the ones the evaluation scores and the charts of the last days read step by step. every <= 0 does nothing.
func (dao *PredictionDAO) CoarsenPredictions(ctx context.Context, cutoff time.Time, every, budget time.Duration) (int64, error) {
	if every <= 0 {
		return 0, nil
	}
	mark, ok := dao.coarsenWatermark(ctx)
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
		end := mark.Add(coarsenSlice)
		if end.After(cutoff) {
			end = cutoff
		}
		ids, err := dao.coarsenSliceIDs(ctx, mark, end, every)
		if err != nil {
			return total, err
		}
		for start := 0; start < len(ids); start += thinBatch {
			stop := min(start+thinBatch, len(ids))
			res := dao.db.WithContext(ctx).Where("id IN ? AND prediction_time >= ? AND prediction_time <= ?", ids[start:stop], mark.Add(-rowDayMargin), end.Add(rowDayMargin)).Delete(&model.Prediction{})
			total += res.RowsAffected
			if res.Error != nil {
				return total, res.Error
			}
		}
		mark = end
		if err := dao.saveCoarsenWatermark(ctx, mark); err != nil {
			return total, err
		}
		if ctx.Err() != nil || (budget > 0 && batchClock().After(deadline)) {
			break
		}
	}
	return total, nil
}
