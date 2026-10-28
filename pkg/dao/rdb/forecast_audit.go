package rdb

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// order turns a requested sort into an ORDER BY from a whitelist (never from user text), newest first by default.
func order(sort, dir string, allowed map[string]string, fallback string) string {
	column, ok := allowed[sort]
	if !ok {
		return fallback
	}
	if strings.EqualFold(dir, "asc") {
		return column + " ASC, id ASC"
	}
	return column + " DESC, id DESC"
}

// RecommendationFilter narrows the recommendation snapshots.
type RecommendationFilter struct {
	ModuleID uint
	Mode     string
	Version  int
	// Source matches any of the three fields (algorithm, fallback, original, fixed_period, bypass, oom_floor).
	Source   string
	Degraded *bool
	OOMFloor *bool
	// LatestOnly keeps the newest snapshot of each module: the current one. Older versions are read per module.
	LatestOnly bool
	Since      *time.Time
	Until      *time.Time
	Sort       string
	Dir        string
}

var recommendationSorts = map[string]string{
	"created_at": "created_at", "version": "version", "replicas": "replicas", "cpu_milli": "cpu_milli", "memory_bytes": "memory_bytes",
}

func (f RecommendationFilter) apply(q *gorm.DB) *gorm.DB {
	// what was recommended for a module that is gone is not listed: it is no recommendation of anything now
	q = q.Where("module_id IN (SELECT id FROM forecast_module)")
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Mode != "" {
		q = q.Where("mode = ?", f.Mode)
	}
	if f.Version > 0 {
		q = q.Where("version = ?", f.Version)
	}
	if f.Source != "" {
		q = q.Where("cpu_source = ? OR memory_source = ? OR replicas_source = ?", f.Source, f.Source, f.Source)
	}
	if f.LatestOnly {
		q = q.Where("id IN (SELECT MAX(id) FROM recommendation GROUP BY module_id)")
	}
	if f.Degraded != nil {
		q = q.Where("degraded = ?", *f.Degraded)
	}
	if f.OOMFloor != nil {
		q = q.Where("oom_floor = ?", *f.OOMFloor)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	return q
}

// SearchRecommendations lists snapshots with filters and a whitelisted sort.
func (dao *PredictionDAO) SearchRecommendations(ctx context.Context, f RecommendationFilter, page, pageSize int) ([]*model.Recommendation, int64, error) {
	q := f.apply(dao.db.WithContext(ctx).Model(&model.Recommendation{}))
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.Recommendation
	err := q.Order(order(f.Sort, f.Dir, recommendationSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// FallbackFilter narrows the degradation records: snapshots where a field fell back, kept its original value or
// was raised by the OOM protection.
type FallbackFilter struct {
	ModuleID uint
	// Reason matches a reason code of any degraded field.
	Reason string
	Since  *time.Time
	Until  *time.Time
	Sort   string
	Dir    string
}

// SearchFallbacks lists the degraded snapshots.
func (dao *PredictionDAO) SearchFallbacks(ctx context.Context, f FallbackFilter, page, pageSize int) ([]*model.Recommendation, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.Recommendation{}).Where("(degraded = ? OR oom_floor = ?) AND module_id IN (SELECT id FROM forecast_module)", true, true)
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Reason != "" {
		if f.Reason == "oom_floor" {
			q = q.Where("oom_floor = ?", true)
		} else {
			// reason codes are a closed set of [a-z_] words, stored as JSON values: "field":"code"
			q = q.Where("degrade_reasons LIKE ?", "%\""+strings.ReplaceAll(strings.ReplaceAll(f.Reason, "%", ""), "\"", "")+"\"%")
		}
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.Recommendation
	err := q.Order(order(f.Sort, f.Dir, recommendationSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// NextHealthy returns the first later snapshot of the module that is neither degraded nor floored, or nil while the
// module is still degraded: its time is when the degradation ended.
func (dao *PredictionDAO) NextHealthy(ctx context.Context, moduleID uint, afterVersion int) (*model.Recommendation, error) {
	var rec model.Recommendation
	err := dao.db.WithContext(ctx).Where("module_id = ? AND version > ? AND degraded = ? AND oom_floor = ?", moduleID, afterVersion, false, false).
		Order("version ASC").Limit(1).Find(&rec).Error
	if err != nil || rec.ID == 0 {
		return nil, err
	}
	return &rec, nil
}

// ExecutionFilter narrows the scaling execution reports.
type ExecutionFilter struct {
	ModuleID uint
	Result   string
	Action   string
	Keyword  string
	Since    *time.Time
	Until    *time.Time
	Sort     string
	Dir      string
}

var executionSorts = map[string]string{
	"created_at": "created_at", "desired": "desired", "current": "current", "decision_version": "decision_version",
}

// SearchExecutions lists execution reports with filters and a whitelisted sort.
func (dao *PredictionDAO) SearchExecutions(ctx context.Context, f ExecutionFilter, page, pageSize int) ([]*model.ScalingExecution, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.ScalingExecution{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Result != "" {
		q = q.Where("result = ?", f.Result)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("target_name LIKE ? OR namespace LIKE ? OR scaler LIKE ? OR reason LIKE ?", like, like, like, like)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.ScalingExecution
	err := q.Order(order(f.Sort, f.Dir, executionSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// RecommendationByVersion returns one snapshot of a module.
func (dao *PredictionDAO) RecommendationByVersion(ctx context.Context, moduleID uint, version int) (*model.Recommendation, error) {
	var rec model.Recommendation
	if err := dao.db.WithContext(ctx).Where("module_id = ? AND version = ?", moduleID, version).First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// BacktestFilter narrows the backtest records.
type BacktestFilter struct {
	ModuleID    uint
	Metric      string
	AlgorithmID string
	Promoted    *bool
	Since       *time.Time
	Until       *time.Time
	Sort        string
	Dir         string
}

var backtestSorts = map[string]string{"created_at": "created_at", "algorithm_id": "algorithm_id", "promoted": "promoted"}

// SearchBacktests lists backtest records with filters and a whitelisted sort.
func (dao *PredictionDAO) SearchBacktests(ctx context.Context, f BacktestFilter, page, pageSize int) ([]*model.BacktestRun, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.BacktestRun{})
	if f.ModuleID != 0 {
		q = q.Where("module_id = ?", f.ModuleID)
	}
	if f.Metric != "" {
		q = q.Where("metric = ?", f.Metric)
	}
	if f.AlgorithmID != "" {
		q = q.Where("algorithm_id = ?", f.AlgorithmID)
	}
	if f.Promoted != nil {
		q = q.Where("promoted = ?", *f.Promoted)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.BacktestRun
	err := q.Order(order(f.Sort, f.Dir, backtestSorts, "id DESC")).Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// ModuleSummary is what a module has produced, for the module list: the newest recommendation and whether it is
// degraded now.
type ModuleSummary struct {
	LatestVersion   int        `json:"latest_version"`
	LatestAt        *time.Time `json:"latest_at"`
	Degraded        bool       `json:"degraded"`
	DegradeReasons  string     `json:"degrade_reasons"`
	OOMFloor        bool       `json:"oom_floor"`
	ConsecutiveFail int        `json:"consecutive_failures"`
	LastError       string     `json:"last_error"`
	HealthyCycles   int        `json:"healthy_cycles"`
}

// ModuleSummaries builds the summaries of the given modules with one query per table.
func (dao *PredictionDAO) ModuleSummaries(ctx context.Context, modules []*model.ForecastModule) (map[uint]ModuleSummary, error) {
	out := make(map[uint]ModuleSummary, len(modules))
	if len(modules) == 0 {
		return out, nil
	}
	ids := make([]uint, len(modules))
	for i, m := range modules {
		ids[i] = m.ID
		out[m.ID] = ModuleSummary{}
	}

	var latest []model.Recommendation
	if err := dao.db.WithContext(ctx).Table("recommendation AS r").
		Select("r.module_id, r.version, r.created_at, r.degraded, r.degrade_reasons, r.oom_floor").
		Joins("JOIN (SELECT module_id, MAX(version) AS v FROM recommendation WHERE module_id IN ? GROUP BY module_id) AS m ON m.module_id = r.module_id AND m.v = r.version", ids).
		Scan(&latest).Error; err != nil {
		return nil, err
	}
	for _, r := range latest {
		s := out[r.ModuleID]
		at := r.CreatedAt
		s.LatestVersion, s.LatestAt, s.Degraded, s.DegradeReasons, s.OOMFloor = r.Version, &at, r.Degraded, r.DegradeReasons, r.OOMFloor
		out[r.ModuleID] = s
	}

	var states []model.ModuleRunState
	if err := dao.db.WithContext(ctx).Where("module_id IN ?", ids).Find(&states).Error; err != nil {
		return nil, err
	}
	for _, st := range states {
		s := out[st.ModuleID]
		s.ConsecutiveFail, s.LastError, s.HealthyCycles = st.ConsecutiveFailures, st.LastError, st.HealthyCycles
		out[st.ModuleID] = s
	}
	return out, nil
}
