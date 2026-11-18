package rdb

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MaxCountedRows bounds the exact count a list asks the database for. Counting every row of a big table (the forecasts, the operator's reports)
// with a filter that no index covers reads all of them, for a number nobody looks at beyond the first pages; so a count stops at this many rows
// and the list says "at least". The pages are read by LIMIT and are not affected.
const MaxCountedRows = 100000

// boundedCount counts the rows of the query up to MaxCountedRows (a count of MaxCountedRows means "at least that many"). It counts a LIMITed
// sub-select, so the database stops reading at the limit instead of counting the table.
func boundedCount(q *gorm.DB) (int64, error) { return boundedCountTo(q, MaxCountedRows) }

// boundedCountTo is boundedCount with the limit given (the tests use a small one).
func boundedCountTo(q *gorm.DB, limit int) (int64, error) {
	var n int64
	sub := q.Session(&gorm.Session{}).Select("1").Limit(limit)
	err := q.Session(&gorm.Session{NewDB: true}).Table("(?) AS counted", sub).Count(&n).Error
	return n, err
}

// batchClock is the clock the budget of a batched delete is measured by; the tests replace it (a coarse clock does not move within one batch).
var batchClock = time.Now

// deleteInBatches removes the rows that match, a batch of ids at a time, for at most the budget; what is left is for the next run. One DELETE of
// millions of rows is one transaction that runs for minutes, holds its locks and its log until the end and leaves the table full of dead rows,
// which slows every query on it; a batch of ten thousand is short. The ids are read first because MySQL does not allow a DELETE to select
// from its own table with a LIMIT. It returns how many rows it removed.
func deleteInBatches(ctx context.Context, db *gorm.DB, table any, where string, args []any, batch int, budget time.Duration) (int64, error) {
	if batch <= 0 {
		batch = 10000
	}
	deadline := batchClock().Add(budget)
	var total int64
	for {
		var ids []uint
		if err := db.WithContext(ctx).Model(table).Where(where, args...).Limit(batch).Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.WithContext(ctx).Where("id IN ?", ids).Delete(table)
		total += res.RowsAffected
		if res.Error != nil {
			return total, res.Error
		}
		if len(ids) < batch || ctx.Err() != nil || (budget > 0 && batchClock().After(deadline)) {
			return total, nil
		}
	}
}
