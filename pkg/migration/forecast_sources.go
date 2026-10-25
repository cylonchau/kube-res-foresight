package migration

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// The forecast model has three parts again: algorithms are configured once for the whole platform
// (algorithm_config), a module is a Kubernetes workload that owns its metric sources, history window and forecast
// period, and every enabled algorithm applies to every module automatically. The previous shape had stand-alone
// algorithm instances (algorithm_instance) that owned the sources and were tied to modules by hand
// (module_binding). The upgrade runs in two steps around the schema migration:
//
//   - prepareForecastSchema drops the tables whose rows were filed under an algorithm instance (history, predictions,
//     backtests, evaluations, scores): they cannot be mapped to modules, and the history is collected again from
//     Prometheus. The counts are printed first. The old metric sources are kept aside (legacy_metric_source). It also
//     sets apart modules that would share a workload.
//   - convertLegacyForecast gives each module the sources, history window and forecast period of its first bound
//     instance, and turns the algorithms of the instances into platform algorithm settings (enabled, with the
//     parameters of the first instance of that algorithm). algorithm_instance and module_binding are retired by the
//     cleanup (retiredForecastTables).
//
// Databases older than the instance shape (metric slots on the module, module_algorithm) are not converted: their
// modules need their sources added again.
var retiredForecastTables = []string{"module_algorithm", "algorithm_template", "input_set", "algorithm_instance", "module_binding"}

const legacySourceTable = "legacy_metric_source"

// legacyForecastData are tables whose rows were filed under an algorithm instance and are rebuilt per module.
var legacyForecastData = []string{"metric_sample", "metric_series", "forecast_prediction", "backtest_run", "prediction_evaluation", "algorithm_score"}

// hasInstanceShape reports whether the database still has the algorithm-instance tables.
func hasInstanceShape(db *gorm.DB) bool {
	m := db.Migrator()
	return m.HasTable("algorithm_instance") && m.HasTable("metric_source") && m.HasColumn("metric_source", "instance_id")
}

// prepareForecastSchema runs before the schema migration.
func prepareForecastSchema(db *gorm.DB) error {
	m := db.Migrator()
	// an earlier run that stopped half way left the moved-aside table with the indexes of the old one
	if m.HasTable(legacySourceTable) {
		for _, index := range []string{"idx_metric_source_key", "idx_metric_source_name"} {
			if m.HasIndex(legacySourceTable, index) {
				if err := m.DropIndex(legacySourceTable, index); err != nil {
					return fmt.Errorf("drop index %s of %s: %w", index, legacySourceTable, err)
				}
			}
		}
	}
	if hasInstanceShape(db) {
		counts := map[string]int64{}
		for _, table := range legacyForecastData {
			if !m.HasTable(table) {
				continue
			}
			var n int64
			if err := db.Table(table).Count(&n).Error; err != nil {
				return fmt.Errorf("count %s: %w", table, err)
			}
			counts[table] = n
		}
		fmt.Println("upgrade: forecast history, predictions, backtests, evaluations and scores were filed under algorithm instances; they are dropped and collected again per module:")
		for _, table := range legacyForecastData {
			if n, ok := counts[table]; ok {
				fmt.Printf("upgrade:   %s (%d rows)\n", table, n)
			}
		}
		for _, table := range legacyForecastData {
			if m.HasTable(table) {
				if err := m.DropTable(table); err != nil {
					return fmt.Errorf("drop %s: %w", table, err)
				}
			}
		}
		if m.HasTable(legacySourceTable) {
			if err := m.DropTable(legacySourceTable); err != nil {
				return err
			}
		}
		// a renamed table keeps its indexes, and their names would collide with the ones of the new table
		for _, index := range []string{"idx_metric_source_key", "idx_metric_source_name"} {
			if m.HasIndex("metric_source", index) {
				if err := m.DropIndex("metric_source", index); err != nil {
					return fmt.Errorf("drop index %s of the old metric sources: %w", index, err)
				}
			}
		}
		if err := m.RenameTable("metric_source", legacySourceTable); err != nil {
			return fmt.Errorf("set the metric sources of the instances aside: %w", err)
		}
		fmt.Println("upgrade: the metric sources of the algorithm instances are moved to their modules")
	}
	if !m.HasTable("forecast_module") || m.HasColumn("forecast_module", "workload_kind") {
		return nil
	}
	// one module per workload: older modules were told apart by container, which no longer exists
	var rows []struct {
		ID          uint
		Name        string
		Namespace   string
		Workload    string
		Description string
	}
	if err := db.Table("forecast_module").Select("id, name, namespace, workload, description").Order("id").Scan(&rows).Error; err != nil {
		return err
	}
	seen := map[string]uint{}
	for _, r := range rows {
		key := r.Namespace + "/" + r.Workload
		first, dup := seen[key]
		if !dup {
			seen[key] = r.ID
			continue
		}
		note := fmt.Sprintf("DUPLICATE of module %d (same workload): disabled by the upgrade; point it at its own workload before enabling", first)
		if err := db.Table("forecast_module").Where("id = ?", r.ID).
			Updates(map[string]any{"enabled": false, "workload": fmt.Sprintf("%s#%d", r.Workload, r.ID), "description": truncate(note, 255)}).Error; err != nil {
			return err
		}
		fmt.Printf("upgrade: module %q shares workload %s with module %d; it is disabled and renamed %s#%d\n", r.Name, key, first, r.Workload, r.ID)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type legacyInstance struct {
	ID                      uint
	AlgorithmID             string
	Params                  string
	Enabled                 bool
	LookbackHours           int
	HorizonMinutes          int
	MaxFillSteps            int
	TrainBudgetSeconds      int
	FallbackMinHistoryRatio float64
	MaxMissingRatio         float64
	PromoteMinPoints        int
	PromoteMinImprovement   float64
	PromotePeakTolerance    float64
}

type legacyBinding struct {
	ModuleID   uint
	InstanceID uint
	Weight     float64
	Enabled    bool
}

// convertLegacyForecast runs after the schema migration, while the old tables still exist. It is idempotent: the
// moved-aside source table is what says there is work to do, and it is dropped at the end.
func convertLegacyForecast(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(legacySourceTable) {
		return nil
	}
	var instances []legacyInstance
	if m.HasTable("algorithm_instance") {
		if err := db.Table("algorithm_instance").Order("id").Scan(&instances).Error; err != nil {
			return err
		}
	}
	byID := make(map[uint]legacyInstance, len(instances))
	for _, in := range instances {
		byID[in.ID] = in
	}
	var bindings []legacyBinding
	if m.HasTable("module_binding") {
		if err := db.Table("module_binding").Order("priority, id").Scan(&bindings).Error; err != nil {
			return err
		}
	}
	var sources []model.MetricSource
	if err := db.Table(legacySourceTable).Order("priority, id").Scan(&sources).Error; err != nil {
		return err
	}
	// the legacy table has instance_id where the model has module_id: read it by name
	var owners []struct {
		ID         uint
		InstanceID uint
	}
	if err := db.Table(legacySourceTable).Select("id, instance_id").Scan(&owners).Error; err != nil {
		return err
	}
	ownerOf := make(map[uint]uint, len(owners))
	for _, o := range owners {
		ownerOf[o.ID] = o.InstanceID
	}
	sourcesOf := map[uint][]model.MetricSource{}
	for _, s := range sources {
		sourcesOf[ownerOf[s.ID]] = append(sourcesOf[ownerOf[s.ID]], s)
	}

	taken := map[uint]bool{}
	for _, b := range bindings {
		in, ok := byID[b.InstanceID]
		if !ok || !b.Enabled || taken[b.ModuleID] || len(sourcesOf[in.ID]) == 0 {
			continue
		}
		var has int64
		if err := db.Model(&model.MetricSource{}).Where("module_id = ?", b.ModuleID).Count(&has).Error; err != nil {
			return err
		}
		if has > 0 {
			taken[b.ModuleID] = true
			continue
		}
		taken[b.ModuleID] = true
		for _, s := range sourcesOf[in.ID] {
			s.ID, s.ModuleID = 0, b.ModuleID
			if err := db.Create(&s).Error; err != nil {
				return fmt.Errorf("move source %s of instance %d to module %d: %w", s.Key, in.ID, b.ModuleID, err)
			}
		}
		updates := map[string]any{"lookback_hours": in.LookbackHours, "horizon_minutes": in.HorizonMinutes, "max_fill_steps": in.MaxFillSteps,
			"fallback_min_history_ratio": in.FallbackMinHistoryRatio, "max_missing_ratio": in.MaxMissingRatio}
		if err := db.Model(&model.ForecastModule{}).Where("id = ?", b.ModuleID).Updates(updates).Error; err != nil {
			return err
		}
		fmt.Printf("upgrade: module %d takes the metric sources and settings of algorithm instance %d\n", b.ModuleID, in.ID)
	}

	// the algorithms of the instances become platform-wide settings
	algorithms := rdb.NewAlgorithmDAO(db)
	ctx := context.Background()
	if err := algorithms.Ensure(ctx, forecast.IDs(), nil); err != nil {
		return err
	}
	done := map[string]bool{}
	for _, in := range instances {
		if !in.Enabled || done[in.AlgorithmID] {
			continue
		}
		cfg, err := algorithms.Get(ctx, in.AlgorithmID)
		if err != nil {
			fmt.Printf("upgrade: algorithm %q of instance %d is not in the catalog any more; skipped\n", in.AlgorithmID, in.ID)
			continue
		}
		done[in.AlgorithmID] = true
		cfg.Enabled, cfg.Params = true, in.Params
		cfg.TrainBudgetSeconds, cfg.PromoteMinPoints = in.TrainBudgetSeconds, in.PromoteMinPoints
		cfg.PromoteMinImprovement, cfg.PromotePeakTolerance = in.PromoteMinImprovement, in.PromotePeakTolerance
		if err := algorithms.Save(ctx, cfg); err != nil {
			return err
		}
	}
	var ids []string
	for id := range done {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Printf("upgrade: algorithms enabled from the instances: %v\n", ids)
	return m.DropTable(legacySourceTable)
}

// ensureAlgorithmConfigs gives every catalog algorithm its platform setting; the baselines that need no parameter start
// enabled.
func ensureAlgorithmConfigs(db *gorm.DB) error {
	return rdb.NewAlgorithmDAO(db).Ensure(context.Background(), forecast.IDs(), forecastsvc.DefaultEnabled)
}
