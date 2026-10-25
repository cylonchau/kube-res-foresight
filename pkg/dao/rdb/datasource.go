package rdb

import (
	"context"
	"errors"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
)

// ErrDatasourceInUse blocks deleting a datasource that still has metric mappings.
var ErrDatasourceInUse = errors.New("datasource still has metric mappings")

// ErrNameTaken reports a unique-name conflict detected before writing.
var ErrNameTaken = errors.New("name already exists")

type DatasourceDAO struct {
	db *gorm.DB
}

func NewDatasourceDAO(db *gorm.DB) *DatasourceDAO { return &DatasourceDAO{db: db} }

func (dao *DatasourceDAO) GetByID(ctx context.Context, id uint) (*model.Datasource, error) {
	var ds model.Datasource
	if err := dao.db.WithContext(ctx).First(&ds, id).Error; err != nil {
		return nil, err
	}
	return &ds, nil
}

func (dao *DatasourceDAO) nameTaken(ctx context.Context, name string, exceptID uint) (bool, error) {
	var count int64
	q := dao.db.WithContext(ctx).Model(&model.Datasource{}).Where("name = ?", name)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	err := q.Count(&count).Error
	return count > 0, err
}

func (dao *DatasourceDAO) Create(ctx context.Context, ds *model.Datasource) error {
	if taken, err := dao.nameTaken(ctx, ds.Name, 0); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Create(ds).Error
}

func (dao *DatasourceDAO) Update(ctx context.Context, ds *model.Datasource) error {
	if taken, err := dao.nameTaken(ctx, ds.Name, ds.ID); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Save(ds).Error
}

// List returns one page plus the total; MappingCounts maps datasource id to its mapping count.
func (dao *DatasourceDAO) List(ctx context.Context, req *apis.DatasourceListReq, enabled *bool) ([]*model.Datasource, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.Datasource{})
	if req.Keyword != "" {
		q = q.Where("name LIKE ? OR base_url LIKE ?", like(req.Keyword), like(req.Keyword))
	}
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := normalizePage(int(req.Page), int(req.PageSize))
	var list []*model.Datasource
	err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// ListEnabled returns every enabled datasource for selectors.
func (dao *DatasourceDAO) ListEnabled(ctx context.Context) ([]*model.Datasource, error) {
	var list []*model.Datasource
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("name ASC").Find(&list).Error
	return list, err
}

// MappingCounts returns the number of metric mappings per datasource id.
func (dao *DatasourceDAO) MappingCounts(ctx context.Context, ids []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	var rows []struct {
		DatasourceID uint
		Total        int64
	}
	err := dao.db.WithContext(ctx).Model(&model.MetricMapping{}).
		Select("datasource_id, COUNT(*) AS total").Where("datasource_id IN ?", ids).
		Group("datasource_id").Scan(&rows).Error
	for _, row := range rows {
		counts[row.DatasourceID] = row.Total
	}
	return counts, err
}

// RecordTest persists the outcome of a connectivity probe.
func (dao *DatasourceDAO) RecordTest(ctx context.Context, id uint, status, version string, latencyMs int, lastError string, at time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.Datasource{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status": status, "server_version": version, "last_latency_ms": latencyMs,
		"last_error": trimTo(lastError, 512), "last_checked_at": at,
	}).Error
}

// Delete removes datasources that have no mappings. It fails with ErrDatasourceInUse
// without deleting anything when any of the ids is still referenced.
func (dao *DatasourceDAO) Delete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var used int64
		if err := tx.Model(&model.MetricMapping{}).Where("datasource_id IN ?", ids).Count(&used).Error; err != nil {
			return err
		}
		if used > 0 {
			return ErrDatasourceInUse
		}
		return tx.Where("id IN ?", ids).Delete(&model.Datasource{}).Error
	})
}
