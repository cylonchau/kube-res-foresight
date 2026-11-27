package rdb

import (
	"context"

	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// NodePoolDAO stores the node pools.
type NodePoolDAO struct{ db *gorm.DB }

// NewNodePoolDAO makes the DAO.
func NewNodePoolDAO(db *gorm.DB) *NodePoolDAO { return &NodePoolDAO{db: db} }

// NodePoolFilter narrows a list of pools.
type NodePoolFilter struct {
	Keyword string
	Kind    string
	Cluster string
	Enabled *bool
	Page    int
	Size    int
}

// Create stores a pool whose name is not taken in its cluster.
func (dao *NodePoolDAO) Create(ctx context.Context, p *model.NodePool) error {
	n, err := dao.takenCount(ctx, p)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Create(p).Error
}

func (dao *NodePoolDAO) takenCount(ctx context.Context, p *model.NodePool) (int64, error) {
	var n int64
	q := dao.db.WithContext(ctx).Model(&model.NodePool{}).Where("cluster = ? AND name = ?", p.Cluster, p.Name)
	if p.ID != 0 {
		q = q.Where("id <> ?", p.ID)
	}
	err := q.Count(&n).Error
	return n, err
}

// Update saves a pool.
func (dao *NodePoolDAO) Update(ctx context.Context, p *model.NodePool) error {
	n, err := dao.takenCount(ctx, p)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrNameTaken
	}
	return dao.db.WithContext(ctx).Save(p).Error
}

// Get reads one pool.
func (dao *NodePoolDAO) Get(ctx context.Context, id uint) (*model.NodePool, error) {
	var p model.NodePool
	if err := dao.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// List reads a page of pools, with the total.
func (dao *NodePoolDAO) List(ctx context.Context, f NodePoolFilter) ([]*model.NodePool, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.NodePool{})
	if f.Keyword != "" {
		q = q.Where("name LIKE ? OR remark LIKE ?", "%"+f.Keyword+"%", "%"+f.Keyword+"%")
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Cluster != "" {
		q = q.Where("cluster = ?", f.Cluster)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	size := f.Size
	if size <= 0 || size > 500 {
		size = 20
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	var out []*model.NodePool
	err := q.Order("priority DESC, name ASC").Offset((page - 1) * size).Limit(size).Find(&out).Error
	return out, total, err
}

// Enabled lists every pool that is switched on (what the recommendation chooses from).
func (dao *NodePoolDAO) Enabled(ctx context.Context) ([]*model.NodePool, error) {
	var out []*model.NodePool
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("priority DESC, name ASC").Find(&out).Error
	return out, err
}

// Delete removes pools for good.
func (dao *NodePoolDAO) Delete(ctx context.Context, ids []uint) error {
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.NodePool{}).Error
}
