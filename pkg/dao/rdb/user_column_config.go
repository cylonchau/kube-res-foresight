package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

type UserColumnConfigDAO struct {
	db *gorm.DB
}

func NewUserColumnConfigDAO(db *gorm.DB) *UserColumnConfigDAO {
	return &UserColumnConfigDAO{db: db}
}

func (dao *UserColumnConfigDAO) Get(ctx context.Context, userID uint, viewName string) (*model.UserColumnConfig, error) {
	var config model.UserColumnConfig
	err := dao.db.WithContext(ctx).Where("user_id = ? AND view_name = ?", userID, viewName).First(&config).Error
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (dao *UserColumnConfigDAO) Update(ctx context.Context, config *model.UserColumnConfig) error {
	// Use Save to update or create if not exists (but we usually check first)
	return dao.db.WithContext(ctx).Save(config).Error
}

func (dao *UserColumnConfigDAO) Save(ctx context.Context, userID uint, viewName string, configStr string) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var config model.UserColumnConfig
		err := tx.Unscoped().Where("user_id = ? AND view_name = ?", userID, viewName).First(&config).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.UserColumnConfig{
				UserID:   userID,
				ViewName: viewName,
				Config:   configStr,
			}).Error
		}
		if err != nil {
			return err
		}

		return tx.Unscoped().Model(&config).Updates(map[string]interface{}{
			"config":     configStr,
			"deleted_at": nil,
		}).Error
	})
}
