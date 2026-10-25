package rdb

import (
	"context"

	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

type MetricMappingDAO struct {
	db *gorm.DB
}

func NewMetricMappingDAO(db *gorm.DB) *MetricMappingDAO { return &MetricMappingDAO{db: db} }

func (dao *MetricMappingDAO) GetByID(ctx context.Context, id uint) (*model.MetricMapping, error) {
	var m model.MetricMapping
	if err := dao.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (dao *MetricMappingDAO) nameTaken(ctx context.Context, datasourceID uint, name string, exceptID uint) (bool, error) {
	var count int64
	q := dao.db.WithContext(ctx).Model(&model.MetricMapping{}).Where("datasource_id = ? AND name = ?", datasourceID, name)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	err := q.Count(&count).Error
	return count > 0, err
}

func (dao *MetricMappingDAO) datasourceExists(ctx context.Context, id uint) error {
	var count int64
	if err := dao.db.WithContext(ctx).Model(&model.Datasource{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Create requires an existing datasource and a name unique within it.
func (dao *MetricMappingDAO) Create(ctx context.Context, m *model.MetricMapping) error {
	if err := dao.datasourceExists(ctx, m.DatasourceID); err != nil {
		return err
	}
	if taken, err := dao.nameTaken(ctx, m.DatasourceID, m.Name, 0); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Create(m).Error
}

func (dao *MetricMappingDAO) Update(ctx context.Context, m *model.MetricMapping) error {
	if err := dao.datasourceExists(ctx, m.DatasourceID); err != nil {
		return err
	}
	if taken, err := dao.nameTaken(ctx, m.DatasourceID, m.Name, m.ID); err != nil {
		return err
	} else if taken {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Save(m).Error
}

func (dao *MetricMappingDAO) List(ctx context.Context, req *apis.MetricMappingListReq, enabled *bool) ([]*model.MetricMapping, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.MetricMapping{})
	if req.Keyword != "" {
		q = q.Where("name LIKE ? OR query LIKE ?", like(req.Keyword), like(req.Keyword))
	}
	if req.DatasourceID != 0 {
		q = q.Where("datasource_id = ?", req.DatasourceID)
	}
	if req.Metric != "" {
		q = q.Where("metric = ?", req.Metric)
	}
	if req.Level != "" {
		q = q.Where("level = ?", req.Level)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := normalizePage(int(req.Page), int(req.PageSize))
	var list []*model.MetricMapping
	err := q.Order("id DESC").Offset(pageOffset(page, pageSize)).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// DatasourceNames returns id -> name for the given datasource ids.
func (dao *MetricMappingDAO) DatasourceNames(ctx context.Context, ids []uint) (map[uint]string, error) {
	names := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	var rows []model.Datasource
	if err := dao.db.WithContext(ctx).Select("id", "name").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

func (dao *MetricMappingDAO) Delete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.MetricMapping{}).Error
}
