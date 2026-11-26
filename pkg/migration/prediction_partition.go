package migration

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// legacyPredictionTable is where the ordinary forecast table stays after the partitioned one has taken its name. It is not dropped by the conversion: it holds every row
// as it was, and dropping it frees its space (DROP TABLE forecast_prediction_legacy) once the partitioned table has been looked at.
const legacyPredictionTable = rdb.PredictionTable + "_legacy"

// stagingPredictionTable is the partitioned table while it is being filled: the ordinary table goes on serving the platform meanwhile, and the staging table takes its name
// in one short step at the end.
const stagingPredictionTable = rdb.PredictionTable + "_partitioned"

// predictionColumns are the columns of the forecast table, in the order the copy writes them.
const predictionColumns = "id, created_at, module_id, metric, prediction_time, target_time, algorithm_id, model_version, quantile_permille, value, unit"

// autoConvertBytes is the size up to which the schema migration converts the forecast table by itself: a small table (a new installation, a trial) is converted in
// seconds. A bigger one is converted by the command `server --partition-predictions`, which is run on purpose, in its own time, and not by the migration Job that has a
// time limit and runs before every upgrade.
const autoConvertBytes = 512 << 20

// partitionPredictions is the step of the schema migration: a forecast table that is small is converted, a big one is left for the command.
func partitionPredictions(db *gorm.DB) error {
	if !partitionable(db) {
		return nil
	}
	var size int64
	if err := db.Raw(`SELECT pg_table_size(to_regclass(?))`, rdb.PredictionTable).Row().Scan(&size); err != nil {
		return fmt.Errorf("read the size of %s: %w", rdb.PredictionTable, err)
	}
	if size > autoConvertBytes {
		fmt.Printf("upgrade: %s is an ordinary table of %d MiB; it is partitioned by the command `server --partition-predictions`, which copies it without stopping the platform; run it when it suits (nothing was changed)\n", rdb.PredictionTable, size>>20)
		return nil
	}
	return ConvertPredictionsToPartitioned(db)
}

// PartitionPredictionsNow is the command `server --partition-predictions`.
func PartitionPredictionsNow() error {
	if model.DB == nil {
		return fmt.Errorf("no database")
	}
	if model.DB.Dialector == nil || model.DB.Dialector.Name() != "postgres" {
		fmt.Println("partition: only PostgreSQL partitions the forecast table; nothing to do")
		return nil
	}
	if !partitionable(model.DB) {
		fmt.Printf("partition: %s is partitioned already (or there is no such table); nothing to do\n", rdb.PredictionTable)
		return nil
	}
	return ConvertPredictionsToPartitioned(model.DB)
}

// partitionable says whether the forecast table is an ordinary table of a PostgreSQL that can be converted.
func partitionable(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" || !db.Migrator().HasTable(rdb.PredictionTable) {
		return false
	}
	var kinds []string
	if err := db.Raw(`SELECT relkind::text FROM pg_class WHERE oid = to_regclass(?)`, rdb.PredictionTable).Scan(&kinds).Error; err != nil {
		return false
	}
	return len(kinds) == 1 && kinds[0] == "r"
}

// ConvertPredictionsToPartitioned turns the forecast table into one partitioned by day of prediction_time, on PostgreSQL. The table is the biggest of the platform and what
// deleting its rows one by one leaves (dead rows, a bloated table and indexes that do not shrink) is what made it 50 GB for a few million rows: with a partition a day, the
// retention is a DROP TABLE of the day, and what is read by time reads the days it names.
//
// It does not stop the platform while it copies. There are three steps:
//  1. A partitioned table of the same columns is made under another name, with the primary key and the indexes the model declares (the primary key and the unique index carry the
//     partition column, which PostgreSQL requires) and the partitions of the days to keep and of the next few. The ordinary table is not locked: the platform goes on reading and
//     writing it.
//  2. The rows within the retention are copied in one statement (a single read of the old table, the rows routed to their days by PostgreSQL): those up to the highest id at the
//     start, the ones older than a day at the resolution kept for old forecasts (see timing.PredictionCoarsenMinutes). It reads with a snapshot and takes no lock that stops a writer.
//  3. In one short transaction (a lock wait of 30 seconds at most, else it fails and nothing is changed), the table is locked, the rows written since the copy began are copied,
//     the ordinary table is renamed to forecast_prediction_legacy (its indexes with it) and the partitioned one takes the name, the indexes and the id sequence. The lock is held for
//     the time of the few rows copied and a few renames.
//
// A failure at any step leaves the ordinary table as it was (the staging table is dropped when the conversion is started again). The legacy table is dropped only when it was
// empty (a new installation); otherwise it stays, with every row, for the administrator to drop.
func ConvertPredictionsToPartitioned(db *gorm.DB) error {
	if !partitionable(db) {
		return nil
	}
	if db.Migrator().HasTable(legacyPredictionTable) {
		return fmt.Errorf("%s exists: a conversion was made before; drop it (DROP TABLE %s) or move it away, then convert again", legacyPredictionTable, legacyPredictionTable)
	}
	now := time.Now().UTC()
	retention := time.Duration(sysParam(db, timing.PredictionRetentionDays)) * 24 * time.Hour
	coarsenAfter := time.Duration(sysParam(db, timing.PredictionCoarsenAfter)) * time.Hour
	every := time.Duration(sysParam(db, timing.PredictionCoarsenMinutes)) * time.Minute
	keepFrom := now.Add(-retention)
	fullFrom := now.Add(-coarsenAfter)
	firstDay := rdb.DayStart(keepFrom)
	lastDay := rdb.DayStart(now.AddDate(0, 0, rdb.PartitionsAhead))

	fmt.Printf("partition: converting %s to a table partitioned by day; the platform goes on writing it meanwhile\n", rdb.PredictionTable)
	start := time.Now()

	// 1. the staging table
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, ddl := range []string{
			"DROP TABLE IF EXISTS " + stagingPredictionTable, // what an earlier attempt left
			"CREATE TABLE " + stagingPredictionTable + " (LIKE " + rdb.PredictionTable + " INCLUDING DEFAULTS) PARTITION BY RANGE (prediction_time)",
			"ALTER TABLE " + stagingPredictionTable + " ADD PRIMARY KEY (id, prediction_time)",
			"CREATE UNIQUE INDEX idx_prediction_new ON " + stagingPredictionTable + " (module_id, metric, prediction_time, target_time, algorithm_id, model_version, quantile_permille)",
			"CREATE INDEX idx_prediction_target_new ON " + stagingPredictionTable + " (module_id, metric, target_time, algorithm_id, prediction_time, quantile_permille)",
			"CREATE INDEX idx_forecast_prediction_created_at_new ON " + stagingPredictionTable + " (created_at)",
		} {
			if err := tx.Exec(ddl).Error; err != nil {
				return err
			}
		}
		for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
			if err := tx.Exec(rdb.PartitionDDLOf(stagingPredictionTable, day)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("make the partitioned table (nothing was changed): %w", err)
	}

	// copyRows copies the rows within the retention that satisfy `extra`: every step of the last day, then the ones on the grid of the resolution kept for older forecasts. Only the
	// rows that have a partition (a day of the window) are copied.
	copyRows := func(tx *gorm.DB, extra string, extraArgs ...any) (int64, error) {
		q := "INSERT INTO " + stagingPredictionTable + " (" + predictionColumns + ") SELECT " + predictionColumns + " FROM " + rdb.PredictionTable +
			" WHERE created_at >= ? AND prediction_time >= ? AND prediction_time < ?"
		args := []any{keepFrom, firstDay, lastDay.AddDate(0, 0, 1)}
		if every > 0 {
			q += " AND (prediction_time >= ? OR MOD(EXTRACT(EPOCH FROM target_time)::bigint, ?::bigint) = 0)"
			args = append(args, fullFrom, int64(every/time.Second))
		}
		q += " AND " + extra + " ON CONFLICT DO NOTHING"
		res := tx.Exec(q, append(args, extraArgs...)...)
		return res.RowsAffected, res.Error
	}

	// 2. the copy: one statement, no lock that stops a writer
	var highest int64
	if err := db.Raw(`SELECT COALESCE(MAX(id), 0) FROM ` + rdb.PredictionTable).Row().Scan(&highest); err != nil {
		return fmt.Errorf("read the highest id of %s: %w", rdb.PredictionTable, err)
	}
	var copied int64
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL statement_timeout = 0").Error; err != nil {
			return err
		}
		n, err := copyRows(tx, "id <= ?", highest)
		copied = n
		return err
	})
	if err != nil {
		return fmt.Errorf("copy the forecasts (the ordinary table is as it was): %w", err)
	}
	fmt.Printf("partition: %d forecasts copied in %s; taking the place of %s now\n", copied, time.Since(start).Round(time.Second), rdb.PredictionTable)

	// 3. the swap, in one short lock
	var legacyEmpty bool
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '30s'").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL statement_timeout = 0").Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE " + rdb.PredictionTable + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		// the forecasts written while the copy ran
		n, err := copyRows(tx, "id > ?", highest)
		if err != nil {
			return err
		}
		copied += n

		var serial sql.NullString
		if err := tx.Raw(`SELECT pg_get_serial_sequence(?, 'id')`, rdb.PredictionTable).Row().Scan(&serial); err != nil {
			return err
		}
		sequence := serial.String // empty when the id has no sequence

		if err := tx.Exec("ALTER TABLE " + rdb.PredictionTable + " RENAME TO " + legacyPredictionTable).Error; err != nil {
			return err
		}
		var indexes []string
		if err := tx.Raw(`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ?`, legacyPredictionTable).Scan(&indexes).Error; err != nil {
			return err
		}
		for _, name := range indexes {
			if err := tx.Exec(fmt.Sprintf(`ALTER INDEX "%s" RENAME TO "%s_legacy"`, name, name)).Error; err != nil {
				return err
			}
		}
		if sequence != "" {
			if err := tx.Exec("ALTER SEQUENCE " + sequence + " OWNED BY NONE").Error; err != nil {
				return err
			}
		}
		for _, ddl := range []string{
			"ALTER TABLE " + stagingPredictionTable + " RENAME TO " + rdb.PredictionTable,
			"ALTER TABLE " + rdb.PredictionTable + " RENAME CONSTRAINT " + stagingPredictionTable + "_pkey TO " + rdb.PredictionTable + "_pkey",
			"ALTER INDEX idx_prediction_new RENAME TO idx_prediction",
			"ALTER INDEX idx_prediction_target_new RENAME TO idx_prediction_target",
			"ALTER INDEX idx_forecast_prediction_created_at_new RENAME TO idx_forecast_prediction_created_at",
		} {
			if err := tx.Exec(ddl).Error; err != nil {
				return err
			}
		}
		if sequence != "" {
			if err := tx.Exec("ALTER SEQUENCE " + sequence + " OWNED BY " + rdb.PredictionTable + ".id").Error; err != nil {
				return err
			}
		}
		// the days the clock has reached while the copy ran have their partition too
		for day := rdb.DayStart(now); !day.After(rdb.DayStart(time.Now().UTC().AddDate(0, 0, rdb.PartitionsAhead))); day = day.AddDate(0, 0, 1) {
			if err := tx.Exec(rdb.PartitionDDL(day)).Error; err != nil {
				return err
			}
		}
		var has []bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM ` + legacyPredictionTable + `)`).Scan(&has).Error; err != nil {
			return err
		}
		legacyEmpty = len(has) == 1 && !has[0]
		if legacyEmpty {
			return tx.Exec("DROP TABLE " + legacyPredictionTable).Error
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("take the place of %s (nothing was changed; the copy is dropped when the conversion is started again): %w", rdb.PredictionTable, err)
	}
	rdb.ResetPartitionState()
	_ = db.Exec("ANALYZE " + rdb.PredictionTable).Error
	fmt.Printf("partition: %s is partitioned by day now; %d forecasts in %s\n", rdb.PredictionTable, copied, time.Since(start).Round(time.Second))
	if !legacyEmpty {
		fmt.Printf("partition: the table it was before is %s and still holds its space; look at the new one, then DROP TABLE %s to free it\n", legacyPredictionTable, legacyPredictionTable)
	}
	return nil
}

// sysParam reads a system parameter of the time limits, the default when it is not stored.
func sysParam(db *gorm.DB, p timing.Param) int {
	var cfg model.SysConfig
	if err := db.Where("config_key = ?", p.Key).Limit(1).Find(&cfg).Error; err != nil || strings.TrimSpace(cfg.ConfigKey) == "" {
		return p.Default
	}
	return p.Parse(cfg.ConfigValue)
}
