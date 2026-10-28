package migration

import (
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// splitRecommendationMenus is the upgrade of the page that listed the recommendations as "snapshots": it is now the resource
// recommendation (the current one of each module); the history of a module is opened from it. The menus take their order and
// the names follow the new meaning. It is safe to repeat.
func splitRecommendationMenus(db *gorm.DB) error {
	order := map[string]int{
		"forecast-actions_recommendations": 1, "forecast-actions_executions": 2,
		"forecast-actions_fallbacks": 3, "forecast-actions_admissions": 4,
	}
	for code, sort := range order {
		if err := db.Model(&model.Permission{}).Where("code = ?", code).Update("sort", sort).Error; err != nil {
			return err
		}
	}
	names := map[string]string{"forecast-actions_recommendations": "Resource Recommendations", "forecast-actions_fallbacks": "Degradation and Rollback Records"}
	for code, name := range names {
		if err := db.Model(&model.Permission{}).Where("code = ?", code).Update("name", name).Error; err != nil {
			return err
		}
	}
	return nil
}

// removeOrphanForecastData deletes what was stored for modules that no longer exist: deleting a module used to keep its
// recommendations, predictions, evaluations, backtests and training records, which then showed up in the lists as
// recommendations of nothing. It is safe to repeat.
func removeOrphanForecastData(db *gorm.DB) error {
	for _, table := range []string{"recommendation", "forecast_prediction", "prediction_evaluation", "backtest_run", "forecast_training_run", "algorithm_score"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		if err := db.Exec("DELETE FROM " + table + " WHERE module_id NOT IN (SELECT id FROM forecast_module)").Error; err != nil {
			return err
		}
	}
	return nil
}
