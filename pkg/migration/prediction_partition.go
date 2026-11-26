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

// legacyPredictionTable is where the ordinary forecast table stays after the partitioned one has taken its name. It is not dropped by the migration: it holds every row
// as it was, and dropping it frees its space (DROP TABLE forecast_prediction_legacy) once the partitioned table has been looked at.
const legacyPredictionTable = rdb.PredictionTable + "_legacy"

// predictionColumns are the columns of the forecast table, in the order the copy writes them.
const predictionColumns = "id, created_at, module_id, metric, prediction_time, target_time, algorithm_id, model_version, quantile_permille, value, unit"

// partitionPredictions turns the forecast table into one partitioned by day of prediction_time, on PostgreSQL only; every other database keeps the ordinary table. The
// table is the biggest of the platform and what deleting its rows one by one leaves (dead rows, a bloated table and indexes that do not shrink) is what made it 50 GB for a
// few million rows: with a partition a day, the retention is a DROP TABLE of the day, and what is read by time reads the days it names.
//
// The conversion is one transaction, so it either happens whole or not at all (PostgreSQL's DDL is transactional): the ordinary table is renamed to forecast_prediction_legacy
// (its indexes with it), a partitioned table of the same columns takes the name, with the primary key and the indexes the model declares (the primary key and the unique index
// carry the partition column, which PostgreSQL requires), the partitions of the days to keep and of the next few are made, and the rows that are still within the retention
// are copied in, the older ones at the resolution the clean-up keeps them (see timing.PredictionCoarsenMinutes). The platform must not be writing the table meanwhile: the
// table is locked for the whole conversion, so a process that writes waits for it. An already converted table, or any other database, does nothing. The legacy table is
// dropped only when it was empty (a new installation).
//
// A failure leaves the ordinary table as it was and is reported by the caller, which goes on: the platform works on the ordinary table.
func partitionPredictions(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return nil
	}
	m := db.Migrator()
	if !m.HasTable(rdb.PredictionTable) {
		return nil
	}
	var kinds []string
	if err := db.Raw(`SELECT relkind::text FROM pg_class WHERE oid = to_regclass(?)`, rdb.PredictionTable).Scan(&kinds).Error; err != nil {
		return fmt.Errorf("read the kind of %s: %w", rdb.PredictionTable, err)
	}
	if len(kinds) == 1 && kinds[0] == "p" {
		return nil // already partitioned
	}
	if m.HasTable(legacyPredictionTable) {
		return fmt.Errorf("%s exists: a conversion was made before; drop it (DROP TABLE %s) or move it away, then run the migration again", legacyPredictionTable, legacyPredictionTable)
	}

	now := time.Now().UTC()
	retention := time.Duration(sysParam(db, timing.PredictionRetentionDays)) * 24 * time.Hour
	coarsenAfter := time.Duration(sysParam(db, timing.PredictionCoarsenAfter)) * time.Hour
	every := time.Duration(sysParam(db, timing.PredictionCoarsenMinutes)) * time.Minute
	keepFrom := now.Add(-retention)
	fullFrom := now.Add(-coarsenAfter)

	fmt.Printf("upgrade: converting %s to a table partitioned by day; its rows are copied, which takes minutes on a large table, and writes to it wait meanwhile\n", rdb.PredictionTable)
	start := time.Now()
	var copied int64
	var legacyEmpty bool
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("LOCK TABLE " + rdb.PredictionTable + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		var oldest sql.NullTime
		if err := tx.Raw(`SELECT MIN(prediction_time) FROM `+rdb.PredictionTable+` WHERE created_at >= ?`, keepFrom).Row().Scan(&oldest); err != nil {
			return err
		}
		firstDay := rdb.DayStart(now)
		if oldest.Valid && oldest.Time.Before(firstDay) {
			firstDay = rdb.DayStart(oldest.Time)
		}
		if limit := rdb.DayStart(keepFrom); firstDay.Before(limit) {
			firstDay = limit
		}

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
		// the same columns and defaults (the id still takes its numbers from the same sequence), partitioned by the time the forecast was made
		if err := tx.Exec("CREATE TABLE " + rdb.PredictionTable + " (LIKE " + legacyPredictionTable + " INCLUDING DEFAULTS) PARTITION BY RANGE (prediction_time)").Error; err != nil {
			return err
		}
		if sequence != "" {
			if err := tx.Exec("ALTER SEQUENCE " + sequence + " OWNED BY " + rdb.PredictionTable + ".id").Error; err != nil {
				return err
			}
		}
		for _, ddl := range []string{
			"ALTER TABLE " + rdb.PredictionTable + " ADD PRIMARY KEY (id, prediction_time)",
			"CREATE UNIQUE INDEX idx_prediction ON " + rdb.PredictionTable + " (module_id, metric, prediction_time, target_time, algorithm_id, model_version, quantile_permille)",
			"CREATE INDEX idx_prediction_target ON " + rdb.PredictionTable + " (module_id, metric, target_time, algorithm_id, prediction_time, quantile_permille)",
			"CREATE INDEX idx_forecast_prediction_created_at ON " + rdb.PredictionTable + " (created_at)",
		} {
			if err := tx.Exec(ddl).Error; err != nil {
				return err
			}
		}
		for day := firstDay; !day.After(rdb.DayStart(now.AddDate(0, 0, rdb.PartitionsAhead))); day = day.AddDate(0, 0, 1) {
			if err := tx.Exec(rdb.PartitionDDL(day)).Error; err != nil {
				return err
			}
		}

		// the rows within the retention, a day at a time: every step of the last day, then the ones on the grid of the resolution kept for older forecasts
		for day := firstDay; !day.After(rdb.DayStart(now)); day = day.AddDate(0, 0, 1) {
			next := day.AddDate(0, 0, 1)
			q := "INSERT INTO " + rdb.PredictionTable + " (" + predictionColumns + ") SELECT " + predictionColumns + " FROM " + legacyPredictionTable +
				" WHERE prediction_time >= ? AND prediction_time < ? AND created_at >= ?"
			args := []any{day, next, keepFrom}
			if every > 0 {
				q += " AND (prediction_time >= ? OR MOD(EXTRACT(EPOCH FROM target_time)::bigint, ?::bigint) = 0)"
				args = append(args, fullFrom, int64(every/time.Second))
			}
			res := tx.Exec(q+" ON CONFLICT DO NOTHING", args...)
			if res.Error != nil {
				return fmt.Errorf("copy the forecasts of %s: %w", day.Format("2006-01-02"), res.Error)
			}
			copied += res.RowsAffected
			fmt.Printf("upgrade:   %s: %d forecasts\n", day.Format("2006-01-02"), res.RowsAffected)
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
		return fmt.Errorf("convert %s to a partitioned table (nothing was changed): %w", rdb.PredictionTable, err)
	}
	rdb.ResetPartitionState()
	_ = db.Exec("ANALYZE " + rdb.PredictionTable).Error
	fmt.Printf("upgrade: %s is partitioned by day now; %d forecasts copied in %s\n", rdb.PredictionTable, copied, time.Since(start).Round(time.Second))
	if !legacyEmpty {
		fmt.Printf("upgrade: the table it was before is %s and still holds its space; look at the new one, then DROP TABLE %s to free it\n", legacyPredictionTable, legacyPredictionTable)
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
