package migration

import (
	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
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

// retiredAlgorithms are algorithms that were taken out of the catalogue. macd_trend borrowed the shape of a stock-market
// indicator, which has no place in a resource forecast (a damped trend, Holt-Winters and Theta do what it did). naive_bayes puts the
// series into quantile bins and predicts a bin, so its resolution is the width of a bin; rnn, gru and lstm train a network on
// the few thousand points of one workload on every run under a time budget, and never had a measurement that they beat the
// statistical algorithms there.
var retiredAlgorithms = []string{"macd_trend", "naive_bayes", "rnn", "gru", "lstm"}

// removeRetiredAlgorithms deletes the setting of a retired algorithm, of the custom algorithms built on it, and what was
// stored for them (forecasts, evaluations, backtests, training records, scores): without the setting the enabled algorithm
// would be reported as failing on every run. The recommendations keep the name of the algorithm they were made with, they are
// history. It is safe to repeat.
func removeRetiredAlgorithms(db *gorm.DB) error {
	if !db.Migrator().HasTable("algorithm_config") {
		return nil
	}
	ids := append([]string(nil), retiredAlgorithms...)
	if db.Migrator().HasColumn("algorithm_config", "base") {
		var variants []string
		if err := db.Table("algorithm_config").Where("base IN ?", retiredAlgorithms).Pluck("algorithm_id", &variants).Error; err != nil {
			return err
		}
		ids = append(ids, variants...)
	}
	for _, table := range []string{"forecast_prediction", "prediction_evaluation", "backtest_run", "forecast_training_run", "algorithm_score", "algorithm_config"} {
		if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, "algorithm_id") {
			continue
		}
		if err := db.Exec("DELETE FROM "+table+" WHERE algorithm_id IN ?", ids).Error; err != nil {
			return err
		}
	}
	return nil
}

// brokenTextNote replaces a remark that was stored as garbage: the bytes of a non-UTF-8 request were replaced by U+FFFD when it was
// saved, so the original text cannot be recovered and has to be written again.
const brokenTextNote = "(备注因提交时编码错误损坏，请重新填写)"

// repairBrokenRemarks replaces remarks that hold U+FFFD (the mark of text that was not valid UTF-8 when it was saved) with a
// note that says so, in the tables whose remark an administrator types. It is safe to repeat.
func repairBrokenRemarks(db *gorm.DB) error {
	for _, target := range []struct{ table, column string }{
		{"secret_key", "remark"}, {"datasource", "remark"}, {"sys_config", "remark"}, {"metric_mapping", "remark"},
	} {
		if !db.Migrator().HasTable(target.table) || !db.Migrator().HasColumn(target.table, target.column) {
			continue
		}
		if err := db.Exec("UPDATE "+target.table+" SET "+target.column+" = ? WHERE "+target.column+" LIKE ?", brokenTextNote, "%�%").Error; err != nil {
			return err
		}
	}
	// the log of a request keeps what was received, minus the bytes that are not text
	if db.Migrator().HasTable("operation_log") {
		return db.Exec("UPDATE operation_log SET body = ? WHERE body LIKE ?", "(请求内容含无法识别的编码，已省略)", "%�%").Error
	}
	return nil
}
