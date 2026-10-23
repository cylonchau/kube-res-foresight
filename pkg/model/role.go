package model

import (
	"gorm.io/gorm"
	"time"
)

func init() {
	RegisterModel(&Role{})
}

type Role struct {
	ID          uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt   time.Time      `json:"createdAt" column_setting:"true"`
	UpdatedAt   time.Time      `json:"updatedAt" column_setting:"true"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	Name        string         `gorm:"type:varchar(50);not null" json:"name" column_setting:"true"`
	Code        string         `gorm:"type:varchar(100);not null;uniqueIndex" json:"code" column_setting:"true"` // e.g., ADMIN / OPERATOR
	Status      bool           `gorm:"default:true" json:"status" column_setting:"true"`
	Remark      string         `gorm:"type:varchar(255)" json:"remark" column_setting:"true"`
	Permissions []Permission   `gorm:"many2many:sys_role_permissions;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"permissions,omitempty"`
}
