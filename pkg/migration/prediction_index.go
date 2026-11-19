package migration

import (
	"fmt"

	"gorm.io/gorm"
)

// predictionTargetIndex is the index the reads by target time need (see model.Prediction): the module and the metric, the range of target
// times, then the algorithm and the time the forecast was made.
const predictionTargetIndex = "idx_prediction_target"

// ensurePredictionTargetIndex creates the target-time index of the forecast table before the schema migration, on PostgreSQL only, and
// without blocking the writes. The table is the biggest of the platform (a run every few minutes of every algorithm of every module, with
// the quantiles: hundreds of millions of rows in a few months), and the schema migration would create the index with a plain CREATE INDEX,
// which stops every insert into it for as long as the build takes: the pipeline would stall, and the pages with it. CREATE INDEX CONCURRENTLY
// builds it while the platform keeps writing; it cannot run inside a transaction, so it is run here, outside the migration. When the index
// exists the migration finds it and does nothing. A build that was interrupted leaves an invalid index of that name, which is dropped and built
// again. On every other database the migration creates it from the model and this does nothing.
func ensurePredictionTargetIndex(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	if !db.Migrator().HasTable("forecast_prediction") {
		return nil
	}
	valid, exists, err := predictionIndexState(db)
	if err != nil {
		return err
	}
	if exists && valid {
		return nil
	}
	if exists {
		fmt.Printf("upgrade: %s was left invalid by an interrupted build; dropping it to build it again\n", predictionTargetIndex)
		if err := db.Exec("DROP INDEX CONCURRENTLY IF EXISTS " + predictionTargetIndex).Error; err != nil {
			return fmt.Errorf("drop the invalid index %s: %w", predictionTargetIndex, err)
		}
	}
	fmt.Printf("upgrade: building index %s on forecast_prediction without blocking writes; on a large table this takes minutes\n", predictionTargetIndex)
	if err := db.Exec("CREATE INDEX CONCURRENTLY IF NOT EXISTS " + predictionTargetIndex +
		" ON forecast_prediction (module_id, metric, target_time, algorithm_id, prediction_time, quantile_permille)").Error; err != nil {
		return fmt.Errorf("build index %s: %w", predictionTargetIndex, err)
	}
	if valid, _, err = predictionIndexState(db); err != nil {
		return err
	} else if !valid {
		return fmt.Errorf("index %s is still invalid after it was built: drop it (DROP INDEX CONCURRENTLY %s) and run the migration again", predictionTargetIndex, predictionTargetIndex)
	}
	return nil
}

// predictionIndexState says whether the index exists and, if it does, whether it is valid.
func predictionIndexState(db *gorm.DB) (valid, exists bool, err error) {
	var rows []bool
	if err = db.Raw(`SELECT i.indisvalid FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = ?`, predictionTargetIndex).
		Scan(&rows).Error; err != nil {
		return false, false, fmt.Errorf("read the state of index %s: %w", predictionTargetIndex, err)
	}
	if len(rows) == 0 {
		return false, false, nil
	}
	return rows[0], true, nil
}

// listIndex is an index the lists of the pages need on a table that grows without end: the composite of a filter column and the id (the order
// the lists are in), so a list that filters and shows the newest first reads the rows it shows and not the rows it skips. The names are the
// ones of the model tags, so the schema migration finds them and creates nothing.
type listIndex struct{ name, table, columns string }

var listIndexes = []listIndex{
	{"idx_recommendation_module_id", "recommendation", "module_id, id"},
	{"idx_recommendation_degraded_id", "recommendation", "degraded, id"},
	{"idx_recommendation_oom_id", "recommendation", "oom_floor, id"},
	{"idx_scaling_execution_module_order", "scaling_execution", "module_id, id"},
	{"idx_scaling_execution_result_id", "scaling_execution", "result, id"},
	{"idx_prediction_eval_time", "prediction_evaluation", "prediction_time, id"},
	{"idx_prediction_eval_module_time", "prediction_evaluation", "module_id, prediction_time"},
	{"idx_training_time", "forecast_training_run", "prediction_time, id"},
	{"idx_admission_module_id", "admission_decision", "module_id, id"},
}

// ensureListIndexes builds the list indexes before the schema migration, on PostgreSQL only and without blocking the writes (see
// ensurePredictionTargetIndex for why). A table that does not exist yet is skipped: the migration creates it with its indexes. An index that an
// interrupted build left invalid is dropped and built again. On the other databases the migration creates them from the model.
func ensureListIndexes(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	for _, ix := range listIndexes {
		if !db.Migrator().HasTable(ix.table) {
			continue
		}
		var states []bool
		if err := db.Raw(`SELECT i.indisvalid FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = ?`, ix.name).Scan(&states).Error; err != nil {
			return fmt.Errorf("read the state of index %s: %w", ix.name, err)
		}
		if len(states) > 0 && states[0] {
			continue
		}
		if len(states) > 0 {
			fmt.Printf("upgrade: %s was left invalid by an interrupted build; dropping it to build it again\n", ix.name)
			if err := db.Exec("DROP INDEX CONCURRENTLY IF EXISTS " + ix.name).Error; err != nil {
				return fmt.Errorf("drop the invalid index %s: %w", ix.name, err)
			}
		}
		fmt.Printf("upgrade: building index %s on %s without blocking writes\n", ix.name, ix.table)
		if err := db.Exec("CREATE INDEX CONCURRENTLY IF NOT EXISTS " + ix.name + " ON " + ix.table + " (" + ix.columns + ")").Error; err != nil {
			return fmt.Errorf("build index %s: %w", ix.name, err)
		}
	}
	return nil
}

// redundantPredictionIndex was (module, metric, prediction time) on the forecast table: the unique index of the table starts with the same three
// columns, so every read it served is served by that one, and it cost a third of the space of the indexes of the biggest table of the platform
// and a write on every insert. The model does not declare it any more; an existing database drops it here (without blocking the writes on
// PostgreSQL, where a plain DROP INDEX waits for every statement on the table).
const redundantPredictionIndex = "idx_prediction_lookup"

func dropRedundantPredictionIndex(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable("forecast_prediction") {
		return nil
	}
	if !db.Migrator().HasIndex("forecast_prediction", redundantPredictionIndex) {
		return nil
	}
	fmt.Printf("upgrade: dropping the redundant index %s of forecast_prediction (the unique index starts with the same columns)\n", redundantPredictionIndex)
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec("DROP INDEX CONCURRENTLY IF EXISTS " + redundantPredictionIndex).Error; err != nil {
			return fmt.Errorf("drop index %s: %w", redundantPredictionIndex, err)
		}
		return nil
	}
	if err := db.Migrator().DropIndex("forecast_prediction", redundantPredictionIndex); err != nil {
		return fmt.Errorf("drop index %s: %w", redundantPredictionIndex, err)
	}
	return nil
}
