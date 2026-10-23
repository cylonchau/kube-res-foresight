package model

import (
	"gorm.io/gorm"
	"time"
)

type UserColumnConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time      `json:"createdAt" column_setting:"true"`
	UpdatedAt time.Time      `json:"updatedAt" column_setting:"true"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	UserID    uint           `gorm:"not null;uniqueIndex:idx_user_view,priority:1" json:"userId" column_setting:"true"`
	User      *User          `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ViewName  string         `gorm:"type:varchar(100);not null;uniqueIndex:idx_user_view,priority:2" json:"viewName" column_setting:"true"`
	Config    string         `gorm:"type:text" json:"config" column_setting:"true"` // JSON string: [ { key: 'username', visible: true, width: 100, sort: 1 }, ... ]
}

func init() {
	RegisterModel(&UserColumnConfig{})
}
