package rdb

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// CostPriceDAO stores the price rules of the cost.
type CostPriceDAO struct {
	db *gorm.DB
}

func NewCostPriceDAO(db *gorm.DB) *CostPriceDAO { return &CostPriceDAO{db: db} }

// All returns every rule, the ones a person would read first at the top (highest priority, then oldest).
func (dao *CostPriceDAO) All(ctx context.Context) ([]*model.CostPriceRule, error) {
	var list []*model.CostPriceRule
	err := dao.db.WithContext(ctx).Order("priority DESC, id ASC").Find(&list).Error
	return list, err
}

// Enabled returns the rules that price nodes.
func (dao *CostPriceDAO) Enabled(ctx context.Context) ([]*model.CostPriceRule, error) {
	var list []*model.CostPriceRule
	err := dao.db.WithContext(ctx).Where("enabled = ?", true).Order("priority DESC, id ASC").Find(&list).Error
	return list, err
}

// Get returns one rule.
func (dao *CostPriceDAO) Get(ctx context.Context, id uint) (*model.CostPriceRule, error) {
	var r model.CostPriceRule
	if err := dao.db.WithContext(ctx).First(&r, id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// Save creates (ID 0) or updates a rule. The name is unique.
func (dao *CostPriceDAO) Save(ctx context.Context, r *model.CostPriceRule) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if taken, err := nameTaken(tx, &model.CostPriceRule{}, r.Name, r.ID); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		// Enabled has a database default of true: write the chosen value, false included
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		return tx.Model(r).UpdateColumn("enabled", r.Enabled).Error
	})
}

// Delete removes a rule.
func (dao *CostPriceDAO) Delete(ctx context.Context, id uint) error {
	res := dao.db.WithContext(ctx).Delete(&model.CostPriceRule{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
