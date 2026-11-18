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
