package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InventoryDAO stores what the operator reported of the cluster and of the workloads of the modules (P10 A1).
type InventoryDAO struct {
	db *gorm.DB
}

func NewInventoryDAO(db *gorm.DB) *InventoryDAO { return &InventoryDAO{db: db} }

// InsertReport records that a report was taken; false when the same record id was already there (the report is a repeat and nothing else is to be stored).
func (dao *InventoryDAO) InsertReport(ctx context.Context, r *model.InventoryReport) (bool, error) {
	res := dao.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "record_id"}}, DoNothing: true}).Create(r)
	return res.RowsAffected > 0, res.Error
}

// AddNodes stores the nodes of one report.
func (dao *InventoryDAO) AddNodes(ctx context.Context, rows []model.ClusterNodeSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).CreateInBatches(rows, 100).Error
}

// LatestNodes is the nodes of the newest report that had any, and when it was made: the cluster as it is.
// clusters limits it to the rows of those cluster names (P12: a platform holds several clusters); nil is every cluster.
func (dao *InventoryDAO) LatestNodes(ctx context.Context, clusters []string) ([]model.ClusterNodeSnapshot, time.Time, error) {
	scope := func(q *gorm.DB) *gorm.DB {
		if clusters != nil {
			return q.Where("cluster IN ?", clusters)
		}
		return q
	}
	var at []time.Time
	if err := scope(dao.db.WithContext(ctx).Model(&model.ClusterNodeSnapshot{})).Order("observed_at DESC").Limit(1).Pluck("observed_at", &at).Error; err != nil || len(at) == 0 {
		return nil, time.Time{}, err
	}
	var rows []model.ClusterNodeSnapshot
	err := scope(dao.db.WithContext(ctx).Where("observed_at = ?", at[0])).Order("node").Find(&rows).Error
	return rows, at[0], err
}

// NodeTrendPoint is how many nodes there were, and how many were ready, at one report.
type NodeTrendPoint struct {
	ObservedAt time.Time `json:"observed_at"`
	Total      int       `json:"total"`
	Ready      int       `json:"ready"`
	AllocCPU   int64     `json:"alloc_cpu_milli"`
	AllocMem   int64     `json:"alloc_memory_bytes"`
}

// NodeTrend is the count of nodes over the reports since the time, oldest first.
func (dao *InventoryDAO) NodeTrend(ctx context.Context, since time.Time, clusters []string) ([]NodeTrendPoint, error) {
	var rows []struct {
		ObservedAt time.Time
		Total      int
		Ready      int
		AllocCPU   int64
		AllocMem   int64
	}
	q := dao.db.WithContext(ctx).Model(&model.ClusterNodeSnapshot{})
	if clusters != nil {
		q = q.Where("cluster IN ?", clusters)
	}
	err := q.
		Select("observed_at, COUNT(*) AS total, SUM(CASE WHEN ready THEN 1 ELSE 0 END) AS ready, SUM(alloc_cpu_milli) AS alloc_cpu, SUM(alloc_memory_bytes) AS alloc_mem").
		Where("observed_at >= ?", since.UTC()).Group("observed_at").Order("observed_at ASC").Scan(&rows).Error
	out := make([]NodeTrendPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, NodeTrendPoint{ObservedAt: r.ObservedAt, Total: r.Total, Ready: r.Ready, AllocCPU: r.AllocCPU, AllocMem: r.AllocMem})
	}
	return out, err
}

// GetWorkload is the workload of a module as it was last seen, nil when it never was.
func (dao *InventoryDAO) GetWorkload(ctx context.Context, moduleID uint) (*model.WorkloadConfig, error) {
	var w model.WorkloadConfig
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Limit(1).Find(&w).Error
	if err != nil || w.ID == 0 {
		return nil, err
	}
	return &w, nil
}

// Workloads are the workloads of every module as they were last seen.
func (dao *InventoryDAO) Workloads(ctx context.Context) ([]model.WorkloadConfig, error) {
	var rows []model.WorkloadConfig
	err := dao.db.WithContext(ctx).Order("module_id").Find(&rows).Error
	return rows, err
}

// SaveWorkload writes the current workload of a module: a new row, or the row of the module changed.
func (dao *InventoryDAO) SaveWorkload(ctx context.Context, w *model.WorkloadConfig) error {
	return dao.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "module_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"cluster", "namespace", "kind", "name", "desired", "ready", "available", "updated", "generation", "containers", "pods", "hpa",
			"missing", "error", "observed_at", "updated_at"}),
	}).Create(w).Error
}

// AddChange writes one change of the configuration of a workload.
func (dao *InventoryDAO) AddChange(ctx context.Context, c *model.WorkloadConfigChange) error {
	return dao.db.WithContext(ctx).Create(c).Error
}

// AddWorkloadSnapshots stores the replicas of the workloads at one report.
func (dao *InventoryDAO) AddWorkloadSnapshots(ctx context.Context, rows []model.WorkloadSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).CreateInBatches(rows, 100).Error
}

// Changes are the changes of the configuration of a module, the newest first, at most limit.
func (dao *InventoryDAO) Changes(ctx context.Context, moduleID uint, limit int) ([]model.WorkloadConfigChange, error) {
	if limit < 1 || limit > 500 {
		limit = 50
	}
	var rows []model.WorkloadConfigChange
	err := dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Order("observed_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// WorkloadTrend is the replicas of a module over the reports since the time, oldest first.
func (dao *InventoryDAO) WorkloadTrend(ctx context.Context, moduleID uint, since time.Time) ([]model.WorkloadSnapshot, error) {
	var rows []model.WorkloadSnapshot
	err := dao.db.WithContext(ctx).Where("module_id = ? AND observed_at >= ?", moduleID, since.UTC()).Order("observed_at ASC").Find(&rows).Error
	return rows, err
}

// PrunedInventory is how many rows a clean-up deleted.
type PrunedInventory struct {
	Nodes     int64 `json:"nodes"`
	Snapshots int64 `json:"snapshots"`
	Changes   int64 `json:"changes"`
	Reports   int64 `json:"reports"`
}

// Prune deletes the snapshots (nodes, replicas, report ids) older than snapshotBefore and the changes older than changeBefore, in batches of the ids so no
// statement holds a lock for long.
func (dao *InventoryDAO) Prune(ctx context.Context, snapshotBefore, changeBefore time.Time) (PrunedInventory, error) {
	var out PrunedInventory
	del := func(table any, column string, before time.Time, into *int64) error {
		for ctx.Err() == nil {
			var ids []uint
			if err := dao.db.WithContext(ctx).Model(table).Where(column+" < ?", before.UTC()).Limit(5000).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(table)
			if res.Error != nil {
				return res.Error
			}
			*into += res.RowsAffected
			if len(ids) < 5000 {
				return nil
			}
		}
		return ctx.Err()
	}
	if err := del(&model.ClusterNodeSnapshot{}, "observed_at", snapshotBefore, &out.Nodes); err != nil {
		return out, err
	}
	if err := del(&model.WorkloadSnapshot{}, "observed_at", snapshotBefore, &out.Snapshots); err != nil {
		return out, err
	}
	if err := del(&model.InventoryReport{}, "observed_at", snapshotBefore, &out.Reports); err != nil {
		return out, err
	}
	err := del(&model.WorkloadConfigChange{}, "observed_at", changeBefore, &out.Changes)
	return out, err
}
