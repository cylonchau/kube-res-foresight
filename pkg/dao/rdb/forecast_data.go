package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const sampleBatch = 500

// SampleDAO stores metric history.
type SampleDAO struct {
	db *gorm.DB
}

func NewSampleDAO(db *gorm.DB) *SampleDAO { return &SampleDAO{db: db} }

// EnsureSeries returns the series of a module metric. When the sampling step or the mapping changed, the old
// samples are discarded: values of different queries or resolutions must never be mixed in one series.
// EnsureSeries returns the series of a source. When the mapping, the step or the shape of the values changed, the stored
// samples were made differently from the ones about to be collected, so they are dropped and collected again.
func (dao *SampleDAO) EnsureSeries(ctx context.Context, instanceID uint, metric string, mappingID uint, stepSeconds int, shape string) (*model.MetricSeries, error) {
	var series model.MetricSeries
	err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ?", instanceID, metric).First(&series).Error
	if err == gorm.ErrRecordNotFound {
		series = model.MetricSeries{ModuleID: instanceID, Metric: metric, MappingID: mappingID, StepSeconds: stepSeconds, Shape: shape}
		return &series, dao.db.WithContext(ctx).Create(&series).Error
	}
	if err != nil {
		return nil, err
	}
	if series.Shape == "" {
		// a series stored before sources could shape their values holds the values as queried: the default shape
		series.Shape = model.DefaultShape
	}
	if series.MappingID != mappingID || series.StepSeconds != stepSeconds || series.Shape != shape {
		err = dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("series_id = ?", series.ID).Delete(&model.MetricSample{}).Error; err != nil {
				return err
			}
			series.MappingID, series.StepSeconds, series.Shape = mappingID, stepSeconds, shape
			return tx.Save(&series).Error
		})
		if err != nil {
			return nil, err
		}
	}
	return &series, nil
}

func (dao *SampleDAO) Series(ctx context.Context, instanceID uint, metric string) (*model.MetricSeries, error) {
	var series model.MetricSeries
	if err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ?", instanceID, metric).First(&series).Error; err != nil {
		return nil, err
	}
	return &series, nil
}

// Upsert stores samples, replacing the value of an existing timestamp, in batches.
func (dao *SampleDAO) Upsert(ctx context.Context, seriesID uint, samples []model.MetricSample) error {
	for start := 0; start < len(samples); start += sampleBatch {
		end := start + sampleBatch
		if end > len(samples) {
			end = len(samples)
		}
		batch := make([]model.MetricSample, end-start)
		for i, s := range samples[start:end] {
			batch[i] = model.MetricSample{SeriesID: seriesID, TS: s.TS, Value: s.Value}
		}
		err := dao.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "series_id"}, {Name: "ts"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&batch).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// Range returns the samples in [from, to] ordered by time.
func (dao *SampleDAO) Range(ctx context.Context, seriesID uint, from, to time.Time) ([]model.MetricSample, error) {
	var out []model.MetricSample
	err := dao.db.WithContext(ctx).Where("series_id = ? AND ts >= ? AND ts <= ?", seriesID, from.Unix(), to.Unix()).
		Order("ts ASC").Find(&out).Error
	return out, err
}

// Latest returns the newest stored timestamp, false when the series is empty.
func (dao *SampleDAO) Latest(ctx context.Context, seriesID uint) (time.Time, bool, error) {
	var s model.MetricSample
	err := dao.db.WithContext(ctx).Where("series_id = ?", seriesID).Order("ts DESC").Limit(1).Take(&s).Error
	if err == gorm.ErrRecordNotFound {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(s.TS, 0).UTC(), true, nil
}

// Earliest returns the oldest stored timestamp, false when the series is empty.
func (dao *SampleDAO) Earliest(ctx context.Context, seriesID uint) (time.Time, bool, error) {
	var s model.MetricSample
	err := dao.db.WithContext(ctx).Where("series_id = ?", seriesID).Order("ts ASC").Limit(1).Take(&s).Error
	if err == gorm.ErrRecordNotFound {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(s.TS, 0).UTC(), true, nil
}

// PruneBefore deletes samples older than the retention horizon and returns how many.
func (dao *SampleDAO) PruneBefore(ctx context.Context, seriesID uint, before time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("series_id = ? AND ts < ?", seriesID, before.Unix()).Delete(&model.MetricSample{})
	return res.RowsAffected, res.Error
}

// PredictionDAO stores predictions, backtest runs and recommendations.
type PredictionDAO struct {
	db *gorm.DB
}

func NewPredictionDAO(db *gorm.DB) *PredictionDAO { return &PredictionDAO{db: db} }

// SavePredictions appends predictions; a repeated key (same run replayed) is ignored, never overwritten.
func (dao *PredictionDAO) SavePredictions(ctx context.Context, rows []model.Prediction) error {
	for start := 0; start < len(rows); start += sampleBatch {
		end := start + sampleBatch
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		if err := dao.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error; err != nil {
			return err
		}
	}
	return nil
}

// PredictionFilter narrows ListPredictions.
type PredictionFilter struct {
	ModuleID    uint
	Metric      string
	AlgorithmID string
	From, To    *time.Time // on target_time
}

func (dao *PredictionDAO) ListPredictions(ctx context.Context, f PredictionFilter, page, pageSize int) ([]*model.Prediction, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.Prediction{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Metric != "" {
		q = q.Where("metric = ?", f.Metric)
	}
	if f.AlgorithmID != "" {
		q = q.Where("algorithm_id = ?", f.AlgorithmID)
	}
	if f.From != nil {
		q = q.Where("target_time >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("target_time <= ?", *f.To)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.Prediction
	err := q.Order("prediction_time DESC, target_time ASC, id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// maxCompareRows bounds the forecasts one comparison reads.
const maxCompareRows = 300000

// ForCompare returns, for every algorithm and every target time in [from, to], the newest point forecast made at or before
// that time (the one that applied to it; a forecast made later would be hindsight), for the algorithms given (all when none),
// in target order. Runs overlap (a forecast every five minutes of a fifteen minute horizon puts three forecasts on every target),
// so the newest is picked in the database: the read is bounded by what the chart can show, not by how often the module ran,
// and a long window is not cut off at its newest end by the row limit.
func (dao *PredictionDAO) ForCompare(ctx context.Context, moduleID uint, metric string, from, to time.Time, algorithms []string) ([]model.Prediction, error) {
	point := "quantile_permille IN (0, 500)"
	newest := dao.db.WithContext(ctx).Model(&model.Prediction{}).
		Select("algorithm_id AS algo, target_time AS target, MAX(prediction_time) AS newest").
		Where("module_id = ? AND metric = ? AND target_time >= ? AND target_time <= ? AND prediction_time <= target_time AND "+point, moduleID, metric, from, to).
		Group("algorithm_id, target_time")
	if len(algorithms) > 0 {
		newest = newest.Where("algorithm_id IN ?", algorithms)
	}
	q := dao.db.WithContext(ctx).Model(&model.Prediction{}).
		Joins("JOIN (?) AS latest ON latest.algo = forecast_prediction.algorithm_id AND latest.target = forecast_prediction.target_time AND latest.newest = forecast_prediction.prediction_time", newest).
		Where("forecast_prediction.module_id = ? AND forecast_prediction.metric = ? AND forecast_prediction.quantile_permille IN (0, 500)", moduleID, metric)
	var rows []model.Prediction
	err := q.Order("forecast_prediction.target_time ASC, forecast_prediction.prediction_time ASC").Limit(maxCompareRows).Find(&rows).Error
	return rows, err
}

// LatestRun returns the forecasts (point or quantile, as the algorithm produces) of the newest prediction run of an algorithm for a module metric.
// ForecastsForTargets is, for each moment in [from, to] that an algorithm forecast, the forecast it had for it just before the
// moment: the newest prediction made before the target time. It is what the platform believed the load of that moment would be,
// which is what is compared with the load that came.
func (dao *PredictionDAO) ForecastsForTargets(ctx context.Context, moduleID uint, metric, algorithmID string, from, to time.Time) ([]model.Prediction, error) {
	var rows []model.Prediction
	err := dao.db.WithContext(ctx).
		Where("module_id = ? AND metric = ? AND algorithm_id = ? AND target_time >= ? AND target_time <= ? AND prediction_time < target_time",
			moduleID, metric, algorithmID, from, to).
		Order("target_time ASC, prediction_time DESC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]model.Prediction, 0, len(rows))
	for i, r := range rows {
		if i > 0 && rows[i-1].TargetTime.Equal(r.TargetTime) {
			continue // the first of each target time is the newest made
		}
		out = append(out, r)
	}
	return out, nil
}

func (dao *PredictionDAO) LatestRun(ctx context.Context, instanceID uint, metric, algorithmID string) ([]model.Prediction, error) {
	var newest model.Prediction
	err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ? AND algorithm_id = ?", instanceID, metric, algorithmID).
		Order("prediction_time DESC").Limit(1).Take(&newest).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []model.Prediction
	err = dao.db.WithContext(ctx).Where("module_id = ? AND metric = ? AND algorithm_id = ?  AND prediction_time = ? AND model_version = ?",
		instanceID, metric, algorithmID, newest.PredictionTime, newest.ModelVersion).Order("target_time ASC").Find(&rows).Error
	return rows, err
}

func (dao *PredictionDAO) SaveBacktest(ctx context.Context, run *model.BacktestRun) error {
	return dao.db.WithContext(ctx).Create(run).Error
}

// LatestBacktest returns the newest backtest of an algorithm for a module metric, nil when none exists.
func (dao *PredictionDAO) LatestBacktest(ctx context.Context, instanceID uint, metric, algorithmID string) (*model.BacktestRun, error) {
	var run model.BacktestRun
	err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ? AND algorithm_id = ?", instanceID, metric, algorithmID).
		Order("id DESC").Limit(1).Take(&run).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &run, err
}

func (dao *PredictionDAO) ListBacktests(ctx context.Context, instanceID uint, metric string, page, pageSize int) ([]*model.BacktestRun, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.BacktestRun{})
	if instanceID != 0 {
		q = q.Where("module_id = ?", instanceID)
	}
	if metric != "" {
		q = q.Where("metric = ?", metric)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.BacktestRun
	err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// SaveRecommendation stores the next version for the module. A concurrent writer that took the same version
// makes the insert fail on the unique key; the caller retries.
func (dao *PredictionDAO) SaveRecommendation(ctx context.Context, rec *model.Recommendation) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest model.Recommendation
		err := tx.Where("module_id = ?", rec.ModuleID).Order("version DESC").Limit(1).Take(&latest).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		rec.Version = latest.Version + 1
		return tx.Create(rec).Error
	})
}

// LatestRecommendation returns the newest recommendation, nil when none exists. It is returned even when
// expired: callers decide, using ValidUntil, whether to apply it.
func (dao *PredictionDAO) LatestRecommendation(ctx context.Context, moduleID uint) (*model.Recommendation, error) {
	var rec model.Recommendation
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("version DESC").Limit(1).Take(&rec).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &rec, err
}

func (dao *PredictionDAO) ListRecommendations(ctx context.Context, moduleID uint, page, pageSize int) ([]*model.Recommendation, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.Recommendation{})
	if moduleID != 0 {
		q = q.Where("module_id = ?", moduleID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.Recommendation
	err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// PredictionRun identifies one stored prediction run.
type PredictionRun struct {
	PredictionTime time.Time
	ModelVersion   string
}

// UnevaluatedRuns returns up to limit prediction runs of an algorithm that have no evaluation yet, newest first.
func (dao *PredictionDAO) UnevaluatedRuns(ctx context.Context, instanceID uint, metric, algorithmID string, limit int) ([]PredictionRun, error) {
	var rows []struct {
		PredictionTime time.Time
		ModelVersion   string
	}
	err := dao.db.WithContext(ctx).Model(&model.Prediction{}).
		Select("prediction_time, model_version").
		Where("module_id = ? AND metric = ? AND algorithm_id = ?", instanceID, metric, algorithmID).
		Group("prediction_time, model_version").
		Order("prediction_time DESC").Limit(limit * 4).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	var done []model.PredictionEvaluation
	if err := dao.db.WithContext(ctx).Select("prediction_time", "model_version").
		Where("module_id = ? AND metric = ? AND algorithm_id = ?", instanceID, metric, algorithmID).Find(&done).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, d := range done {
		seen[d.PredictionTime.UTC().Format(time.RFC3339Nano)+"|"+d.ModelVersion] = true
	}
	var out []PredictionRun
	for _, r := range rows {
		if seen[r.PredictionTime.UTC().Format(time.RFC3339Nano)+"|"+r.ModelVersion] {
			continue
		}
		out = append(out, PredictionRun{PredictionTime: r.PredictionTime, ModelVersion: r.ModelVersion})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// RunRows returns the rows of one prediction run ordered by target time.
func (dao *PredictionDAO) RunRows(ctx context.Context, instanceID uint, metric, algorithmID string, predictionTime time.Time, modelVersion string) ([]model.Prediction, error) {
	var rows []model.Prediction
	err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ? AND algorithm_id = ? AND prediction_time = ? AND model_version = ?",
		instanceID, metric, algorithmID, predictionTime, modelVersion).Order("target_time ASC").Find(&rows).Error
	return rows, err
}

// AlgorithmsWithPredictions lists the algorithm ids that have predictions for a module metric.
func (dao *PredictionDAO) AlgorithmsWithPredictions(ctx context.Context, instanceID uint, metric string) ([]string, error) {
	var ids []string
	err := dao.db.WithContext(ctx).Model(&model.Prediction{}).Where("module_id = ? AND metric = ?", instanceID, metric).
		Distinct().Pluck("algorithm_id", &ids).Error
	return ids, err
}

// SaveEvaluation appends an evaluation; the same run evaluated twice is ignored.
func (dao *PredictionDAO) SaveEvaluation(ctx context.Context, e *model.PredictionEvaluation) error {
	return dao.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(e).Error
}

// EvaluationFilter narrows ListEvaluations.
type EvaluationFilter struct {
	ModuleID    uint
	Metric      string
	AlgorithmID string
	Since       *time.Time // on prediction_time
	Until       *time.Time
	Sort        string
	Dir         string
}

var evaluationSorts = map[string]string{"prediction_time": "prediction_time", "wape": "wape", "mae": "mae", "peak_under": "peak_under", "coverage": "coverage", "algorithm_id": "algorithm_id"}

func (dao *PredictionDAO) ListEvaluations(ctx context.Context, f EvaluationFilter, page, pageSize int) ([]*model.PredictionEvaluation, int64, error) {
	q := dao.evaluationQuery(ctx, f)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.PredictionEvaluation
	err := q.Order(order(f.Sort, f.Dir, evaluationSorts, "prediction_time DESC, id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// AllEvaluations returns every evaluation matching the filter (bounded to 20000 rows) for aggregation.
func (dao *PredictionDAO) AllEvaluations(ctx context.Context, f EvaluationFilter) ([]model.PredictionEvaluation, error) {
	var list []model.PredictionEvaluation
	err := dao.evaluationQuery(ctx, f).Order("prediction_time DESC").Limit(20000).Find(&list).Error
	return list, err
}

func (dao *PredictionDAO) evaluationQuery(ctx context.Context, f EvaluationFilter) *gorm.DB {
	q := dao.db.WithContext(ctx).Model(&model.PredictionEvaluation{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Metric != "" {
		q = q.Where("metric = ?", f.Metric)
	}
	if f.AlgorithmID != "" {
		q = q.Where("algorithm_id = ?", f.AlgorithmID)
	}
	if f.Since != nil {
		q = q.Where("prediction_time >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("prediction_time <= ?", *f.Until)
	}
	return q
}

// SaveExecution appends an operator report.
func (dao *PredictionDAO) SaveExecution(ctx context.Context, e *model.ScalingExecution) error {
	return dao.db.WithContext(ctx).Create(e).Error
}

// SaveExecutionOnce stores a report that carries a record id exactly once: a report whose id is already stored (it was sent again) is
// not stored a second time, and created is false. A report without an id is stored as it comes.
func (dao *PredictionDAO) SaveExecutionOnce(ctx context.Context, e *model.ScalingExecution) (created bool, err error) {
	if e.RecordID == nil {
		return true, dao.SaveExecution(ctx, e)
	}
	exists := func() (bool, error) {
		var n int64
		err := dao.db.WithContext(ctx).Model(&model.ScalingExecution{}).Where("record_id = ?", *e.RecordID).Count(&n).Error
		return n > 0, err
	}
	if found, err := exists(); err != nil || found {
		return false, err
	}
	if err := dao.SaveExecution(ctx, e); err != nil {
		// two sends of the same report that arrive together: the other one won the unique index
		if found, ferr := exists(); ferr == nil && found {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListExecutions returns operator reports newest first.
func (dao *PredictionDAO) ListExecutions(ctx context.Context, moduleID uint, result string, page, pageSize int) ([]*model.ScalingExecution, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.ScalingExecution{})
	if moduleID != 0 {
		q = q.Where("module_id = ?", moduleID)
	}
	if result != "" {
		q = q.Where("result = ?", result)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.ScalingExecution
	err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// PruneExecutions deletes reports older than the cutoff and returns how many.
func (dao *PredictionDAO) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.ScalingExecution{})
	return res.RowsAffected, res.Error
}

// ByName returns the module with the exact name.
func (dao *ModuleDAO) ByName(ctx context.Context, name string) (*model.ForecastModule, error) {
	var m model.ForecastModule
	if err := dao.db.WithContext(ctx).Where("name = ?", name).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ForWorkload returns the enabled modules managing a workload. kind may be empty when the caller cannot tell it
// (a Pod's owner chain is not always known): then every kind matches.
func (dao *ModuleDAO) ForWorkload(ctx context.Context, namespace, kind, workload string) ([]*model.ForecastModule, error) {
	var list []*model.ForecastModule
	q := dao.db.WithContext(ctx).Where("namespace = ? AND workload = ? AND enabled = ?", namespace, workload, true)
	if kind != "" {
		q = q.Where("workload_kind = ?", kind)
	}
	err := q.Order("id ASC").Find(&list).Error
	return list, err
}

// Retention is how long each kind of forecasting record is kept. Predictions are the bulk (one row per step per
// run per algorithm), so they go first; evaluations are compact summaries and outlive the raw predictions.
type Retention struct {
	Predictions     time.Duration
	Backtests       time.Duration
	Recommendations time.Duration
	Evaluations     time.Duration
	Trainings       time.Duration
	// RecommendationVersions caps the recent recommendations kept per module, snapshot or not (the newest ones): the same answer
	// repeated every cycle is only needed while it keeps the current recommendation valid. 0 = no cap.
	RecommendationVersions int
	// Snapshots caps the snapshots kept per module (the newest ones): the history of what was recommended and what became of it.
	// 0 = no cap.
	Snapshots int
	// Scores caps the algorithm evaluations kept per module (the newest ones; one evaluation scores every algorithm of the
	// module at one time). 0 = no cap.
	Scores int
	// EvaluationsPerModule caps the measured evaluations kept per module (the newest ones). 0 = no cap.
	EvaluationsPerModule int
}

// DefaultEvaluationsPerModule is how many measured evaluations a module keeps unless the system parameter says otherwise.
const DefaultEvaluationsPerModule = 20000

// DefaultScoresPerModule is how many algorithm evaluations a module keeps unless the system parameter says otherwise.
const DefaultScoresPerModule = 300

// DefaultSnapshotsPerModule is how many snapshots a module keeps unless the system parameter says otherwise.
const DefaultSnapshotsPerModule = 200

// DefaultRetention keeps predictions for 30 days and the audit-relevant summaries for 180.
var DefaultRetention = Retention{
	Predictions:     30 * 24 * time.Hour,
	Backtests:       180 * 24 * time.Hour,
	Recommendations: 180 * 24 * time.Hour,
	Evaluations:     180 * 24 * time.Hour,
	Trainings:       30 * 24 * time.Hour,
	// a module keeps its newest 20 recommendations whatever they are; beyond them only the snapshots stay
	RecommendationVersions: 20,
	Snapshots:              DefaultSnapshotsPerModule,
	Scores:                 DefaultScoresPerModule,
	EvaluationsPerModule:   DefaultEvaluationsPerModule,
}

// Prune deletes records older than their retention and reports how many of each were removed. The latest
// recommendation of every module is always kept, so a module never loses the value it is currently serving.
func (dao *PredictionDAO) Prune(ctx context.Context, now time.Time, r Retention) (map[string]int64, error) {
	removed := map[string]int64{}
	del := func(name string, model interface{}, cutoff time.Duration, where string, args ...interface{}) error {
		if cutoff <= 0 {
			return nil
		}
		q := dao.db.WithContext(ctx).Where(where, append([]interface{}{now.Add(-cutoff)}, args...)...).Delete(model)
		removed[name] = q.RowsAffected
		return q.Error
	}
	if err := del("predictions", &model.Prediction{}, r.Predictions, "created_at < ?"); err != nil {
		return removed, err
	}
	if err := del("trainings", &model.TrainingRun{}, r.Trainings, "created_at < ?"); err != nil {
		return removed, err
	}
	if err := del("backtests", &model.BacktestRun{}, r.Backtests, "created_at < ?"); err != nil {
		return removed, err
	}
	if err := del("evaluations", &model.PredictionEvaluation{}, r.Evaluations, "created_at < ?"); err != nil {
		return removed, err
	}
	if r.EvaluationsPerModule > 0 {
		var evaluated []uint
		if err := dao.db.WithContext(ctx).Model(&model.PredictionEvaluation{}).Distinct().Pluck("module_id", &evaluated).Error; err != nil {
			return removed, err
		}
		for _, id := range evaluated {
			n, err := dao.PruneEvaluations(ctx, id, r.EvaluationsPerModule)
			removed["evaluations"] += n
			if err != nil {
				return removed, err
			}
		}
	}
	if r.Scores > 0 {
		var scored []uint
		if err := dao.db.WithContext(ctx).Model(&model.AlgorithmScore{}).Distinct().Pluck("module_id", &scored).Error; err != nil {
			return removed, err
		}
		for _, id := range scored {
			n, err := dao.PruneScores(ctx, id, r.Scores)
			removed["scores"] += n
			if err != nil {
				return removed, err
			}
		}
	}
	if r.Recommendations <= 0 {
		return removed, nil
	}
	// The ids to keep are read first: MySQL does not allow a DELETE to select from its own table in a subquery.
	var keep []uint
	if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Select("MAX(id)").Group("module_id").Scan(&keep).Error; err != nil {
		return removed, err
	}
	q := dao.db.WithContext(ctx).Where("created_at < ?", now.Add(-r.Recommendations))
	if len(keep) > 0 {
		q = q.Where("id NOT IN ?", keep)
	}
	res := q.Delete(&model.Recommendation{})
	removed["recommendations"] = res.RowsAffected
	if res.Error != nil || r.RecommendationVersions <= 0 {
		return removed, res.Error
	}
	// beyond the newest N of each module only the snapshots stay: the id of the (N+1)th newest is the cut for the others
	var modules []uint
	if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Distinct().Pluck("module_id", &modules).Error; err != nil {
		return removed, err
	}
	for _, id := range modules {
		var cut []uint
		if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("module_id = ?", id).Order("id DESC").Offset(r.RecommendationVersions).Limit(1).Pluck("id", &cut).Error; err != nil {
			return removed, err
		}
		if len(cut) == 0 {
			continue
		}
		over := dao.db.WithContext(ctx).Where("module_id = ? AND id <= ? AND snapshot = ?", id, cut[0], false).Delete(&model.Recommendation{})
		removed["recommendations"] += over.RowsAffected
		if over.Error != nil {
			return removed, over.Error
		}
	}
	return dao.pruneSnapshots(ctx, modules, r.Snapshots, removed)
}

// PruneEvaluations keeps the newest keep measured evaluations of a module and deletes the rest.
func (dao *PredictionDAO) PruneEvaluations(ctx context.Context, moduleID uint, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	var cut []uint
	if err := dao.db.WithContext(ctx).Model(&model.PredictionEvaluation{}).Where("module_id = ?", moduleID).
		Order("id DESC").Offset(keep).Limit(1).Pluck("id", &cut).Error; err != nil {
		return 0, err
	}
	if len(cut) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("module_id = ? AND id <= ?", moduleID, cut[0]).Delete(&model.PredictionEvaluation{})
	return res.RowsAffected, res.Error
}

// pruneSnapshots keeps the newest max snapshots of each module; the newest recommendation of a module is never removed.
func (dao *PredictionDAO) pruneSnapshots(ctx context.Context, modules []uint, max int, removed map[string]int64) (map[string]int64, error) {
	if max <= 0 {
		return removed, nil
	}
	for _, id := range modules {
		var cut []uint
		if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("module_id = ? AND snapshot = ?", id, true).Order("id DESC").Offset(max).Limit(1).Pluck("id", &cut).Error; err != nil {
			return removed, err
		}
		if len(cut) == 0 {
			continue
		}
		var newest []uint
		if err := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("module_id = ?", id).Order("id DESC").Limit(1).Pluck("id", &newest).Error; err != nil {
			return removed, err
		}
		q := dao.db.WithContext(ctx).Where("module_id = ? AND snapshot = ? AND id <= ?", id, true, cut[0])
		if len(newest) > 0 {
			q = q.Where("id <> ?", newest[0])
		}
		res := q.Delete(&model.Recommendation{})
		removed["snapshots"] += res.RowsAffected
		if res.Error != nil {
			return removed, res.Error
		}
	}
	return removed, nil
}

// CountPredictions is how many forecasts an algorithm has ever stored for a module metric.
func (dao *PredictionDAO) CountPredictions(ctx context.Context, moduleID uint, metric, algorithmID string) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.Prediction{}).Where("module_id = ? AND metric = ? AND algorithm_id = ?", moduleID, metric, algorithmID).Count(&n).Error
	return n, err
}

// RunForecast returns the forecast one run wrote (its prediction time, within a second), in target order.
func (dao *PredictionDAO) RunForecast(ctx context.Context, moduleID uint, metric, algorithmID string, predictionTime time.Time) ([]model.Prediction, error) {
	var rows []model.Prediction
	err := dao.db.WithContext(ctx).Where("module_id = ? AND metric = ? AND algorithm_id = ? AND prediction_time >= ? AND prediction_time <= ?",
		moduleID, metric, algorithmID, predictionTime.Add(-time.Second), predictionTime.Add(time.Second)).Order("target_time ASC").Limit(maxCompareRows).Find(&rows).Error
	return rows, err
}
