package rdb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// The forecast table on PostgreSQL is partitioned by day of prediction_time: PostgreSQL routes every insert to the partition of its day and every read that
// names the time reads only the partitions it needs, and what the platform does itself is little: it creates the partitions of the next days (so an insert never
// finds none) and drops the ones past the retention, which is a DROP TABLE of a whole day, instant and without the dead rows, the vacuum and the index bloat that
// deleting the same rows one by one leaves. Every other database keeps the ordinary table. The migration converts an existing table (see package migration).
const (
	// PredictionTable is the forecast table; a partition is PredictionTable + "_p" + the day (UTC) as yyyymmdd.
	PredictionTable = "forecast_prediction"
	// PartitionsAhead is how many days after today have their partition made, so a clock or a pause cannot leave an insert without one.
	PartitionsAhead = 3
	partitionPrefix = PredictionTable + "_p"
	partitionLayout = "20060102"
	// partitionLockWait is how long dropping a partition waits for the table's lock before it leaves it for the next clean-up: it must not stop the inserts behind a long read.
	partitionLockWait = "10s"
	// rowDayMargin is how far from the day of its creation time a forecast's day is taken to lie when a read or a delete is bounded to the partitions of a stretch of
	// creation time (a forecast is created seconds after the run starts; a day is far more than that).
	rowDayMargin = 24 * time.Hour
)

// DayStart is midnight UTC of the day of t.
func DayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// PartitionName is the name of the partition of the day of t.
func PartitionName(t time.Time) string { return partitionPrefix + DayStart(t).Format(partitionLayout) }

// PartitionDay reads the day a partition name stands for; false when the name is not one of a partition of the forecast table.
func PartitionDay(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, partitionPrefix) {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(partitionLayout, strings.TrimPrefix(name, partitionPrefix), time.UTC)
	return day, err == nil
}

// PartitionDDL creates the partition of the day of t when it is not there.
func PartitionDDL(t time.Time) string { return PartitionDDLOf(PredictionTable, t) }

// PartitionDDLOf creates the partition of the day of t of the partitioned table `parent` (the migration builds the new table under another name before it takes the place of
// the old one).
func PartitionDDLOf(parent string, t time.Time) string {
	from := DayStart(t)
	to := from.AddDate(0, 0, 1)
	const layout = "2006-01-02 15:04:05+00"
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')", PartitionName(from), parent, from.Format(layout), to.Format(layout))
}

// partitionState is what this process knows of the table. A table found partitioned stays so; one found ordinary is looked at again after a while, because the migration
// of another process may convert it.
var partitionState = struct {
	sync.Mutex
	partitioned bool
	checkedAt   time.Time
	ensured     map[string]struct{}
}{ensured: map[string]struct{}{}}

const partitionRecheck = 5 * time.Minute

func (dao *PredictionDAO) postgres() bool {
	return dao.db != nil && dao.db.Dialector != nil && dao.db.Dialector.Name() == "postgres"
}

// IsPartitioned says whether the forecast table is the partitioned one (PostgreSQL only).
func (dao *PredictionDAO) IsPartitioned(ctx context.Context) bool {
	if !dao.postgres() {
		return false
	}
	partitionState.Lock()
	defer partitionState.Unlock()
	if partitionState.partitioned {
		return true
	}
	if !partitionState.checkedAt.IsZero() && time.Since(partitionState.checkedAt) < partitionRecheck {
		return false
	}
	var kinds []string
	if err := dao.db.WithContext(ctx).Raw(`SELECT relkind::text FROM pg_class WHERE oid = to_regclass(?)`, PredictionTable).Scan(&kinds).Error; err != nil {
		return false // not known: the ordinary paths work on a partitioned table as well, only slower
	}
	partitionState.checkedAt = time.Now()
	partitionState.partitioned = len(kinds) == 1 && kinds[0] == "p"
	return partitionState.partitioned
}

// ResetPartitionState forgets what this process knows of the table (tests, and a process that has just converted it).
func ResetPartitionState() {
	partitionState.Lock()
	defer partitionState.Unlock()
	partitionState.partitioned, partitionState.checkedAt, partitionState.ensured = false, time.Time{}, map[string]struct{}{}
}

// EnsurePartitions makes the partitions of the days from `from` to `to` (inclusive) that are not there. It is idempotent and safe while other processes do the same: a
// partition that another process made first is found.
func (dao *PredictionDAO) EnsurePartitions(ctx context.Context, from, to time.Time) error {
	if !dao.IsPartitioned(ctx) {
		return nil
	}
	for day := DayStart(from); !day.After(DayStart(to)); day = day.AddDate(0, 0, 1) {
		name := PartitionName(day)
		partitionState.Lock()
		_, done := partitionState.ensured[name]
		partitionState.Unlock()
		if done {
			continue
		}
		if err := dao.db.WithContext(ctx).Exec(PartitionDDL(day)).Error; err != nil {
			// a concurrent CREATE of the same partition can fail on the catalog even with IF NOT EXISTS: the partition is there, which is what was asked
			var exists []bool
			if dao.db.WithContext(ctx).Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error != nil || len(exists) != 1 || !exists[0] {
				return fmt.Errorf("make the forecast partition %s: %w", name, err)
			}
		}
		partitionState.Lock()
		partitionState.ensured[name] = struct{}{}
		partitionState.Unlock()
	}
	return nil
}

// ensureRowPartitions makes the partitions the forecasts about to be inserted fall into.
func (dao *PredictionDAO) ensureRowPartitions(ctx context.Context, first, last time.Time) error {
	if !dao.IsPartitioned(ctx) {
		return nil
	}
	return dao.EnsurePartitions(ctx, first, last)
}

// Partitions lists the days that have a partition, oldest first.
func (dao *PredictionDAO) Partitions(ctx context.Context) ([]time.Time, error) {
	var names []string
	err := dao.db.WithContext(ctx).Raw(`SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = to_regclass(?) ORDER BY c.relname`, PredictionTable).Scan(&names).Error
	if err != nil {
		return nil, err
	}
	days := make([]time.Time, 0, len(names))
	for _, name := range names {
		if day, ok := PartitionDay(name); ok {
			days = append(days, day)
		}
	}
	return days, nil
}

// DropExpiredPartitions drops the partitions whose whole day is before cutoff and returns how many it dropped. A partition it cannot get the lock of within a few seconds
// stays for the next clean-up.
func (dao *PredictionDAO) DropExpiredPartitions(ctx context.Context, cutoff time.Time) (int, error) {
	days, err := dao.Partitions(ctx)
	if err != nil {
		return 0, err
	}
	dropped := 0
	for _, day := range days {
		if day.AddDate(0, 0, 1).After(cutoff) {
			continue
		}
		err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '" + partitionLockWait + "'").Error; err != nil {
				return err
			}
			return tx.Exec("DROP TABLE IF EXISTS " + PartitionName(day)).Error
		})
		if err != nil {
			return dropped, fmt.Errorf("drop the forecast partition %s: %w", PartitionName(day), err)
		}
		partitionState.Lock()
		delete(partitionState.ensured, PartitionName(day))
		partitionState.Unlock()
		dropped++
	}
	return dropped, nil
}
