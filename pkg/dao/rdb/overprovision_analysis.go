package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// OverprovisionDAO stores the results of the over-provisioning analysis.
type OverprovisionDAO struct {
	db *gorm.DB
}

func NewOverprovisionDAO(db *gorm.DB) *OverprovisionDAO { return &OverprovisionDAO{db: db} }

// CreateBatch keeps the results of one run in one transaction.
func (dao *OverprovisionDAO) CreateBatch(ctx context.Context, rows []*model.OverprovisionAnalysis) error {
	if len(rows) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).CreateInBatches(rows, 100).Error
}

// Latest returns the newest result of each module and resource over a window.
func (dao *OverprovisionDAO) Latest(ctx context.Context, windowMinutes int) ([]*model.OverprovisionAnalysis, error) {
	var out []*model.OverprovisionAnalysis
	newest := dao.db.Model(&model.OverprovisionAnalysis{}).Select("MAX(id)").Where("window_minutes = ?", windowMinutes).Group("module_id, resource")
	err := dao.db.WithContext(ctx).Where("id IN (?)", newest).Order("module_id, resource").Find(&out).Error
	return out, err
}

// History returns the results of one resource of one module over a window, newest first.
func (dao *OverprovisionDAO) History(ctx context.Context, moduleID uint, resource string, windowMinutes, limit int) ([]*model.OverprovisionAnalysis, error) {
	if limit < 1 || limit > 500 {
		limit = 60
	}
	q := dao.db.WithContext(ctx).Where("module_id = ?", moduleID)
	if resource != "" {
		q = q.Where("resource = ?", resource)
	}
	if windowMinutes > 0 {
		q = q.Where("window_minutes = ?", windowMinutes)
	}
	var out []*model.OverprovisionAnalysis
	err := q.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// PruneBefore deletes at most limit results older than cutoff and returns how many went.
func (dao *OverprovisionDAO) PruneBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var ids []uint
	if err := dao.db.WithContext(ctx).Model(&model.OverprovisionAnalysis{}).Where("created_at < ?", cutoff).Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.OverprovisionAnalysis{})
	return res.RowsAffected, res.Error
}

// DeleteModule removes the results of a module that is gone.
func (dao *OverprovisionDAO) DeleteModule(ctx context.Context, moduleID uint) error {
	return dao.db.WithContext(ctx).Where("module_id = ?", moduleID).Delete(&model.OverprovisionAnalysis{}).Error
}
