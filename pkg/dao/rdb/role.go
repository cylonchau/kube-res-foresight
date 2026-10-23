package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
)

type RoleDAO struct {
	db *gorm.DB
}

func NewRoleDAO(db *gorm.DB) *RoleDAO {
	return &RoleDAO{db: db}
}

func (dao *RoleDAO) Create(ctx context.Context, role *model.Role) error {
	return dao.db.WithContext(ctx).Create(role).Error
}

func (dao *RoleDAO) GetByID(ctx context.Context, id uint) (*model.Role, error) {
	var role model.Role
	err := dao.db.WithContext(ctx).Preload("Permissions").First(&role, id).Error
	return &role, err
}

func (dao *RoleDAO) List(ctx context.Context, roleName, roleCode string, status *bool, offset, limit int) ([]*model.Role, int64, error) {
	var roles []*model.Role
	var total int64
	db := dao.db.WithContext(ctx).Model(&model.Role{})

	if roleName != "" {
		db = db.Where("name LIKE ?", "%"+roleName+"%")
	}
	if roleCode != "" {
		db = db.Where("code LIKE ?", "%"+roleCode+"%")
	}
	if status != nil {
		db = db.Where("status = ?", *status)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := db.Preload("Permissions").Offset(offset).Limit(limit).Find(&roles).Error
	return roles, total, err
}

func (dao *RoleDAO) ListAll(ctx context.Context) ([]*model.Role, error) {
	var roles []*model.Role
	err := dao.db.WithContext(ctx).Find(&roles).Error
	return roles, err
}

func (dao *RoleDAO) Update(ctx context.Context, role *model.Role) error {
	return dao.db.WithContext(ctx).Save(role).Error
}

func (dao *RoleDAO) UpdatePermissions(ctx context.Context, roleID uint, permissionIDs []uint) error {
	var permissions []model.Permission
	if len(permissionIDs) > 0 {
		if err := dao.db.WithContext(ctx).Where("id IN ?", permissionIDs).Find(&permissions).Error; err != nil {
			return err
		}
	}
	var role model.Role
	role.ID = roleID
	return dao.db.WithContext(ctx).Model(&role).Association("Permissions").Replace(permissions)
}

func (dao *RoleDAO) Delete(ctx context.Context, id uint) error {
	return dao.db.WithContext(ctx).Delete(&model.Role{}, id).Error
}

func (dao *RoleDAO) BatchDelete(ctx context.Context, ids []uint) error {
	return dao.db.WithContext(ctx).Delete(&model.Role{}, ids).Error
}
