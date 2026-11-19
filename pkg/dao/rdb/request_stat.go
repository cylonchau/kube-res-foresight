package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RequestStatDAO keeps the hours of request statistics the processes write (P11 B0).
type RequestStatDAO struct {
	db *gorm.DB
}

func NewRequestStatDAO(db *gorm.DB) *RequestStatDAO { return &RequestStatDAO{db: db} }

// Upsert writes the hours of one process: a row that is there (the same boot, hour, method and route) is replaced with the new absolute figures.
func (dao *RequestStatDAO) Upsert(ctx context.Context, rows []model.RequestStat) error {
	if len(rows) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "boot_id"}, {Name: "hour"}, {Name: "method"}, {Name: "route"}},
		DoUpdates: clause.AssignmentColumns([]string{"updated_at", "count", "status_4xx", "status_5xx", "timeouts", "abandoned", "sum_ms", "max_ms", "db_ms", "db_calls",
			"prom_ms", "prom_calls", "buckets", "examples"}),
	}).CreateInBatches(rows, 100).Error
}

// Since returns the rows of the hours from since on.
func (dao *RequestStatDAO) Since(ctx context.Context, since time.Time) ([]model.RequestStat, error) {
	var rows []model.RequestStat
	err := dao.db.WithContext(ctx).Where("hour >= ?", since.UTC().Truncate(time.Hour)).Find(&rows).Error
	return rows, err
}

// PruneBefore deletes the hours before cutoff, and returns how many rows went.
func (dao *RequestStatDAO) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("hour < ?", cutoff.UTC()).Delete(&model.RequestStat{})
	return res.RowsAffected, res.Error
}

// ProcessBootDAO keeps the runs of the platform process.
type ProcessBootDAO struct {
	db *gorm.DB
}

func NewProcessBootDAO(db *gorm.DB) *ProcessBootDAO { return &ProcessBootDAO{db: db} }

// Start writes a new run. A run of the same instance that never wrote its stop is the one this process replaces: the container was restarted without
// the process finishing (it was killed or crashed), so that run is marked unexpected, and ended when it was last seen.
func (dao *ProcessBootDAO) Start(ctx context.Context, boot *model.ProcessBoot) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ProcessBoot{}).Where("instance = ? AND stopped_at IS NULL AND boot_id <> ?", boot.Instance, boot.BootID).
			UpdateColumns(map[string]any{"unexpected": true, "stopped_at": gorm.Expr("last_seen")}).Error; err != nil {
			return err
		}
		return tx.Create(boot).Error
	})
}

// Beat says the run is alive, with its memory and the limit it runs under.
func (dao *ProcessBootDAO) Beat(ctx context.Context, bootID string, at time.Time, heap, sys, limit int64) error {
	return dao.db.WithContext(ctx).Model(&model.ProcessBoot{}).Where("boot_id = ?", bootID).
		UpdateColumns(map[string]any{"last_seen": at.UTC(), "heap_bytes": heap, "sys_bytes": sys, "mem_limit": limit}).Error
}

// Stop writes that the run was told to stop and finished.
func (dao *ProcessBootDAO) Stop(ctx context.Context, bootID string, at time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.ProcessBoot{}).Where("boot_id = ?", bootID).
		UpdateColumns(map[string]any{"stopped_at": at.UTC(), "graceful": true, "last_seen": at.UTC()}).Error
}

// SweepLost marks the runs that have not been seen for staleAfter and wrote no stop as unexpected: their Pod is gone (a Pod that is replaced gets a new
// instance name, so the restart-in-place rule of Start does not find it). It returns how many it marked.
func (dao *ProcessBootDAO) SweepLost(ctx context.Context, now time.Time, staleAfter time.Duration) (int64, error) {
	res := dao.db.WithContext(ctx).Model(&model.ProcessBoot{}).Where("stopped_at IS NULL AND last_seen < ?", now.UTC().Add(-staleAfter)).
		UpdateColumns(map[string]any{"unexpected": true, "stopped_at": gorm.Expr("last_seen")})
	return res.RowsAffected, res.Error
}

// Since returns the runs that were running at or after since, the newest first.
func (dao *ProcessBootDAO) Since(ctx context.Context, since time.Time) ([]model.ProcessBoot, error) {
	var rows []model.ProcessBoot
	err := dao.db.WithContext(ctx).Where("stopped_at IS NULL OR stopped_at >= ?", since.UTC()).Order("started_at DESC").Limit(200).Find(&rows).Error
	return rows, err
}

// PruneBefore deletes the runs that stopped before cutoff.
func (dao *ProcessBootDAO) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res := dao.db.WithContext(ctx).Where("stopped_at IS NOT NULL AND stopped_at < ?", cutoff.UTC()).Delete(&model.ProcessBoot{})
	return res.RowsAffected, res.Error
}
