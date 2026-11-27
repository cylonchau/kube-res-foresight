package rdb

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// tombstoneFor is the tombstone of a module that was registered by itself; nil for a module somebody made by hand.
func tombstoneFor(m *model.ForecastModule) *model.RegistrationTombstone {
	if !m.AutoRegistered {
		return nil
	}
	return &model.RegistrationTombstone{Cluster: m.Cluster, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload, ModuleName: m.Name}
}

// writeTombstone remembers the deletion of a module that was registered by itself, in the transaction that deletes it. A database that has no such table yet (it was not migrated) is
// left as it was: the deletion is not refused for it.
func writeTombstone(tx *gorm.DB, m *model.ForecastModule) error {
	t := tombstoneFor(m)
	if t == nil || !tx.Migrator().HasTable(&model.RegistrationTombstone{}) {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(t).Error
}

// Tombstoned says whether the workload was registered by itself, deleted by a person, and not yet allowed to be registered again.
func (dao *ModuleDAO) Tombstoned(ctx context.Context, cluster, namespace, kind, workload string) (bool, error) {
	if !dao.db.Migrator().HasTable(&model.RegistrationTombstone{}) {
		return false, nil
	}
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.RegistrationTombstone{}).
		Where("cluster = ? AND namespace = ? AND workload_kind = ? AND workload = ?", cluster, namespace, kind, workload).Count(&n).Error
	return n > 0, err
}

// Tombstones are the deleted self-registered modules that are not registered again, newest first.
func (dao *ModuleDAO) Tombstones(ctx context.Context) ([]model.RegistrationTombstone, error) {
	out := []model.RegistrationTombstone{}
	if !dao.db.Migrator().HasTable(&model.RegistrationTombstone{}) {
		return out, nil
	}
	err := dao.db.WithContext(ctx).Order("id DESC").Find(&out).Error
	return out, err
}

// DeleteTombstone lets the workload be registered again (the next report of the operator that carries it makes the module).
func (dao *ModuleDAO) DeleteTombstone(ctx context.Context, id uint) error {
	res := dao.db.WithContext(ctx).Delete(&model.RegistrationTombstone{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
