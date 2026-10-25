package rdb

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"gorm.io/gorm"
)

type UserDAO struct {
	db *gorm.DB
}

func NewUserDAO(db *gorm.DB) *UserDAO {
	return &UserDAO{db: db}
}

// GetByUsername queries user by username preloading roles and permissions
func (dao *UserDAO) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := dao.db.WithContext(ctx).
		Preload("Roles.Permissions").
		Where("username = ?", username).
		First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Create creates a new user
func (dao *UserDAO) Create(ctx context.Context, user *model.User) error {
	return dao.db.WithContext(ctx).Create(user).Error
}

// GetFirstActiveWithPermissions returns the first active user (by primary
// key order), preloading roles and permissions.
// login path to pick a default account in local/dev environments.
func (dao *UserDAO) GetFirstActiveWithPermissions(ctx context.Context) (*model.User, error) {
	var user model.User
	err := dao.db.WithContext(ctx).Preload("Roles.Permissions").Where("status = ?", true).First(&user).Error
	return &user, err
}

// GetByID queries user by ID
func (dao *UserDAO) GetByID(ctx context.Context, id uint) (*model.User, error) {
	var user model.User
	err := dao.db.WithContext(ctx).Preload("Roles.Permissions").First(&user, id).Error
	return &user, err
}

// List handles retrieval of user list with pagination and search
func (dao *UserDAO) List(ctx context.Context, username, nickName string, status *bool, offset, limit int) ([]*model.User, int64, error) {
	var users []*model.User
	var total int64
	db := dao.db.WithContext(ctx).Model(&model.User{})

	if username != "" {
		db = db.Where("username LIKE ?", "%"+username+"%")
	}
	if nickName != "" {
		db = db.Where("nick_name LIKE ?", "%"+nickName+"%")
	}
	if status != nil {
		db = db.Where("status = ?", *status)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := db.Preload("Roles").Offset(offset).Limit(limit).Find(&users).Error
	return users, total, err
}

// Update updates user information
func (dao *UserDAO) Update(ctx context.Context, user *model.User) error {
	return dao.db.WithContext(ctx).Save(user).Error
}

// Delete deletes a user by ID
func (dao *UserDAO) Delete(ctx context.Context, id uint) error {
	return dao.db.WithContext(ctx).Delete(&model.User{}, id).Error
}

// BatchDelete deletes users in batch
func (dao *UserDAO) BatchDelete(ctx context.Context, ids []uint) error {
	return dao.db.WithContext(ctx).Delete(&model.User{}, ids).Error
}
