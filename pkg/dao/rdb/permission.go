package rdb

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"strings"
)

type PermissionDAO struct {
	db *gorm.DB
}

func NewPermissionDAO(db *gorm.DB) *PermissionDAO {
	return &PermissionDAO{db: db}
}

// GetAll queries all permissions and menus
func (dao *PermissionDAO) GetAll(ctx context.Context) ([]*model.Permission, error) {
	var perms []*model.Permission
	err := dao.db.WithContext(ctx).Order("sort asc").Find(&perms).Error
	return perms, err
}

// GetByType queries permissions by type
func (dao *PermissionDAO) GetByType(ctx context.Context, pType string) ([]*model.Permission, error) {
	var perms []*model.Permission
	err := dao.db.WithContext(ctx).Where("type = ? AND status = ?", pType, true).Order("sort asc").Find(&perms).Error
	return perms, err
}

// AllowsDeveloperCredential reports whether any assigned, enabled permission
// explicitly maps to the current HTTP method and Gin route template.
func (dao *PermissionDAO) AllowsDeveloperCredential(ctx context.Context, permissionIDs []uint, method, route string) (bool, error) {
	if len(permissionIDs) == 0 {
		return false, nil
	}
	var permissions []model.Permission
	if err := dao.db.WithContext(ctx).
		Select("be_apis").Where("id IN ? AND status = ?", permissionIDs, true).
		Find(&permissions).Error; err != nil {
		return false, err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	for _, permission := range permissions {
		var specs []string
		if json.Unmarshal([]byte(permission.BeApis), &specs) != nil {
			continue
		}
		for _, spec := range specs {
			parts := strings.SplitN(strings.TrimSpace(spec), " ", 2)
			if len(parts) == 2 && strings.ToUpper(parts[0]) == method && parts[1] == route {
				return true, nil
			}
		}
	}
	return false, nil
}

// Create creates a new permission
func (dao *PermissionDAO) Create(ctx context.Context, perm *model.Permission) error {
	return dao.db.WithContext(ctx).Create(perm).Error
}

// Update updates an existing permission
func (dao *PermissionDAO) Update(ctx context.Context, perm *model.Permission) error {
	return dao.db.WithContext(ctx).Save(perm).Error
}

// Delete deletes a permission by ID
func (dao *PermissionDAO) Delete(ctx context.Context, id uint) error {
	return dao.db.WithContext(ctx).Delete(&model.Permission{}, id).Error
}
