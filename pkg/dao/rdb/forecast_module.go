package rdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

// ErrModuleEnabled blocks deleting a module that is still enabled.
var ErrModuleEnabled = errors.New("disable the module before deleting it")

// ErrWorkloadTaken is returned when another module already manages the workload.
var ErrWorkloadTaken = errors.New("another module already manages this workload")

// ModuleDAO manages forecast modules with their metric sources and fixed periods.
type ModuleDAO struct {
	db *gorm.DB
}

func NewModuleDAO(db *gorm.DB) *ModuleDAO { return &ModuleDAO{db: db} }

func (dao *ModuleDAO) Get(ctx context.Context, id uint) (*model.ForecastModule, error) {
	var m model.ForecastModule
	if err := dao.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, err
	}
	if err := dao.loadSources(ctx, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// loadSources attaches the metric sources to the modules, one query for all of them.
func (dao *ModuleDAO) loadSources(ctx context.Context, modules ...*model.ForecastModule) error {
	if len(modules) == 0 {
		return nil
	}
	ids := make([]uint, len(modules))
	for i, m := range modules {
		ids[i] = m.ID
	}
	var sources []*model.MetricSource
	if err := dao.db.WithContext(ctx).Where("module_id IN ?", ids).Order("priority ASC, id ASC").Find(&sources).Error; err != nil {
		return err
	}
	by := map[uint][]*model.MetricSource{}
	for _, s := range sources {
		by[s.ModuleID] = append(by[s.ModuleID], s)
	}
	for _, m := range modules {
		m.Sources = by[m.ID]
		if m.Sources == nil {
			m.Sources = []*model.MetricSource{}
		}
	}
	return nil
}

func (dao *ModuleDAO) nameTaken(ctx context.Context, name string, exceptID uint) (bool, error) {
	var count int64
	q := dao.db.WithContext(ctx).Model(&model.ForecastModule{}).Where("name = ?", name)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	err := q.Count(&count).Error
	return count > 0, err
}

// workloadTaken reports whether another module already manages the workload: two modules adjusting one workload
// would fight over its requests and replicas.
func (dao *ModuleDAO) workloadTaken(ctx context.Context, m *model.ForecastModule) (bool, error) {
	var count int64
	q := dao.db.WithContext(ctx).Model(&model.ForecastModule{}).
		Where("namespace = ? AND workload_kind = ? AND workload = ?", m.Namespace, m.WorkloadKind, m.Workload)
	if m.ID != 0 {
		q = q.Where("id <> ?", m.ID)
	}
	err := q.Count(&count).Error
	return count > 0, err
}

func (dao *ModuleDAO) Create(ctx context.Context, m *model.ForecastModule) error {
	if taken, err := dao.nameTaken(ctx, m.Name, 0); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	if taken, err := dao.workloadTaken(ctx, m); err != nil {
		return err
	} else if taken {
		return ErrWorkloadTaken
	}
	if err := dao.db.WithContext(ctx).Create(m).Error; err != nil {
		return err
	}
	if m.JVMHeapPressure == 0 {
		// the column default (0.85) replaces a zero on insert; an explicit 0 means the rule is off
		return dao.db.WithContext(ctx).Model(m).Select("jvm_heap_pressure").Updates(map[string]any{"jvm_heap_pressure": 0}).Error
	}
	return nil
}

func (dao *ModuleDAO) Update(ctx context.Context, m *model.ForecastModule) error {
	if taken, err := dao.nameTaken(ctx, m.Name, m.ID); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	if taken, err := dao.workloadTaken(ctx, m); err != nil {
		return err
	} else if taken {
		return ErrWorkloadTaken
	}
	return dao.db.WithContext(ctx).Omit("Sources").Save(m).Error
}

// ModuleFilter narrows List.
type ModuleFilter struct {
	Keyword   string
	Namespace string
	Mode      string
	Enabled   *bool
}

func (dao *ModuleDAO) List(ctx context.Context, f ModuleFilter, page, pageSize int) ([]*model.ForecastModule, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.ForecastModule{})
	if f.Keyword != "" {
		q = q.Where("name LIKE ? OR workload LIKE ?", like(f.Keyword), like(f.Keyword))
	}
	if f.Namespace != "" {
		q = q.Where("namespace = ?", f.Namespace)
	}
	if f.Mode != "" {
		q = q.Where("mode = ?", f.Mode)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize = normalizePage(page, pageSize)
	var list []*model.ForecastModule
	if err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, dao.loadSources(ctx, list...)
}

// ListEnabled returns every enabled module.
func (dao *ModuleDAO) ListEnabled(ctx context.Context) ([]*model.ForecastModule, error) {
	var list []*model.ForecastModule
	if err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, dao.loadSources(ctx, list...)
}

// Delete removes a disabled module with its metric sources, the history collected for them, fixed periods, scores, run
// state, predictions, backtests, evaluations, training records and recommendations: nothing of a module that is gone is
// worth listing. The operator reports and admission decisions stay as the audit trail of what was done to the workload.
func (dao *ModuleDAO) Delete(ctx context.Context, id uint) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m model.ForecastModule
		if err := tx.First(&m, id).Error; err != nil {
			return err
		}
		if m.Enabled {
			return ErrModuleEnabled
		}
		var seriesIDs []uint
		if err := tx.Model(&model.MetricSeries{}).Where("module_id = ?", id).Pluck("id", &seriesIDs).Error; err != nil {
			return err
		}
		if len(seriesIDs) > 0 {
			if err := tx.Where("series_id IN ?", seriesIDs).Delete(&model.MetricSample{}).Error; err != nil {
				return err
			}
		}
		for _, table := range []interface{}{&model.MetricSeries{}, &model.MetricSource{}, &model.ModulePeriod{}, &model.ModuleRunState{}, &model.AlgorithmScore{},
			&model.Recommendation{}, &model.Prediction{}, &model.PredictionEvaluation{}, &model.BacktestRun{}, &model.TrainingRun{}, &model.ScaleOutcome{}} {
			if err := tx.Where("module_id = ?", id).Delete(table).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.ForecastModule{}, id).Error
	})
}

// SetBypass switches the module-level bypass.
func (dao *ModuleDAO) SetBypass(ctx context.Context, id uint, bypass bool, reason string) error {
	res := dao.db.WithContext(ctx).Model(&model.ForecastModule{}).Where("id = ?", id).
		Updates(map[string]interface{}{"bypass": bypass, "bypass_reason": trimTo(reason, 255)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Periods returns a module's fixed periods.
func (dao *ModuleDAO) Periods(ctx context.Context, moduleID uint) ([]*model.ModulePeriod, error) {
	var list []*model.ModulePeriod
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("priority DESC, name ASC").Find(&list).Error
	return list, err
}

func (dao *ModuleDAO) GetPeriod(ctx context.Context, id uint) (*model.ModulePeriod, error) {
	var p model.ModulePeriod
	if err := dao.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (dao *ModuleDAO) SavePeriod(ctx context.Context, p *model.ModulePeriod) error {
	var count int64
	q := dao.db.WithContext(ctx).Model(&model.ModulePeriod{}).Where("module_id = ? AND name = ?", p.ModuleID, p.Name)
	if p.ID != 0 {
		q = q.Where("id <> ?", p.ID)
	}
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Save(p).Error
}

func (dao *ModuleDAO) DeletePeriod(ctx context.Context, moduleID, id uint) error {
	res := dao.db.WithContext(ctx).Where("module_id = ? AND id = ?", moduleID, id).Delete(&model.ModulePeriod{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ---- metric sources

// Sources returns a module's metric sources, enabled or not.
func (dao *ModuleDAO) Sources(ctx context.Context, moduleID uint) ([]*model.MetricSource, error) {
	var list []*model.MetricSource
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("priority ASC, id ASC").Find(&list).Error
	return list, err
}

func (dao *ModuleDAO) GetSource(ctx context.Context, id uint) (*model.MetricSource, error) {
	var s model.MetricSource
	if err := dao.db.WithContext(ctx).First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// freeKey returns a series key for the kind that no other source of the module uses: the kind itself, then
// kind_2, kind_3 and so on (at most 20 characters, the width of the series column).
func freeKey(taken map[string]bool, kind string) string {
	if len(kind) > 14 {
		kind = kind[:14]
	}
	if !taken[kind] {
		return kind
	}
	for n := 2; ; n++ {
		key := fmt.Sprintf("%s_%d", kind, n)
		if !taken[key] {
			return key
		}
	}
}

// SaveSource creates or updates a source. A new source gets its series key from its kind; the key and the kind of
// an existing one never change, because stored history and predictions are filed under them.
func (dao *ModuleDAO) SaveSource(ctx context.Context, s *model.MetricSource) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var others []*model.MetricSource
		if err := tx.Where("module_id = ?", s.ModuleID).Find(&others).Error; err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, o := range others {
			if o.ID == s.ID {
				continue
			}
			taken[o.Key] = true
			if strings.EqualFold(o.Name, s.Name) {
				return ErrNameTaken
			}
		}
		if s.ID == 0 {
			s.Key = freeKey(taken, s.Kind)
			return tx.Create(s).Error
		}
		var current model.MetricSource
		if err := tx.First(&current, s.ID).Error; err != nil {
			return err
		}
		if current.ModuleID != s.ModuleID {
			return gorm.ErrRecordNotFound
		}
		s.Key, s.Kind = current.Key, current.Kind
		return tx.Save(s).Error
	})
}

// DeleteSource removes a source and the history stored for it.
func (dao *ModuleDAO) DeleteSource(ctx context.Context, moduleID, id uint) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var s model.MetricSource
		if err := tx.Where("module_id = ? AND id = ?", moduleID, id).First(&s).Error; err != nil {
			return err
		}
		var series []uint
		if err := tx.Model(&model.MetricSeries{}).Where("module_id = ? AND metric = ?", moduleID, s.Key).Pluck("id", &series).Error; err != nil {
			return err
		}
		if len(series) > 0 {
			if err := tx.Where("series_id IN ?", series).Delete(&model.MetricSample{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", series).Delete(&model.MetricSeries{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.MetricSource{}, id).Error
	})
}
