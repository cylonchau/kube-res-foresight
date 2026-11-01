package rdb

import (
	"context"
	"strings"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

// AssistantConnectionDAO stores the language model connections of the AI assistant.
type AssistantConnectionDAO struct {
	db *gorm.DB
}

func NewAssistantConnectionDAO(db *gorm.DB) *AssistantConnectionDAO {
	return &AssistantConnectionDAO{db: db}
}

func (dao *AssistantConnectionDAO) Get(ctx context.Context, id uint) (*model.AssistantConnection, error) {
	var c model.AssistantConnection
	if err := dao.db.WithContext(ctx).First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// Active is the connection the assistant uses: the enabled one (the oldest if a manual edit left several), nil when there is none.
func (dao *AssistantConnectionDAO) Active(ctx context.Context) (*model.AssistantConnection, error) {
	var c model.AssistantConnection
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("id ASC").Limit(1).Find(&c).Error
	if err != nil || c.ID == 0 {
		return nil, err
	}
	return &c, nil
}

// Count is how many connections there are.
func (dao *AssistantConnectionDAO) Count(ctx context.Context) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AssistantConnection{}).Count(&n).Error
	return n, err
}

// List returns the connections, newest first, filtered by a keyword (name, endpoint, model) and by whether they are enabled.
func (dao *AssistantConnectionDAO) List(ctx context.Context, keyword string, enabled *bool, page, pageSize int) ([]*model.AssistantConnection, int64, error) {
	q := dao.db.WithContext(ctx).Model(&model.AssistantConnection{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR endpoint LIKE ? OR model LIKE ?", like, like, like)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	var list []*model.AssistantConnection
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

func (dao *AssistantConnectionDAO) nameTaken(tx *gorm.DB, name string, exceptID uint) (bool, error) {
	var n int64
	q := tx.Model(&model.AssistantConnection{}).Where("name = ?", name)
	if exceptID != 0 {
		q = q.Where("id <> ?", exceptID)
	}
	err := q.Count(&n).Error
	return n > 0, err
}

// Save creates (ID 0) or updates a connection. The name is unique. Enabling one disables the others: the assistant has one model at a
// time. It is one transaction, so a failure leaves the connection that was in use in use.
func (dao *AssistantConnectionDAO) Save(ctx context.Context, c *model.AssistantConnection) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if taken, err := dao.nameTaken(tx, c.Name, c.ID); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		if err := tx.Save(c).Error; err != nil {
			return err
		}
		if c.Enabled {
			return tx.Model(&model.AssistantConnection{}).Where("id <> ?", c.ID).Update("enabled", false).Error
		}
		return nil
	})
}

// Delete removes connections for good.
func (dao *AssistantConnectionDAO) Delete(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.AssistantConnection{}).Error
}
