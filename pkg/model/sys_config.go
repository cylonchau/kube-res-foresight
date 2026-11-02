package model

import (
	"gorm.io/gorm"
	"time"
)

func init() {
	RegisterModel(&SysConfig{})
}

type SysConfig struct {
	ID          uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt   time.Time      `json:"created_at" column_setting:"true"`
	UpdatedAt   time.Time      `json:"updated_at" column_setting:"true"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	ConfigKey   string         `gorm:"type:varchar(100);not null;uniqueIndex" json:"config_key" column_setting:"true"` // e.g., sys.auth.token_timeout_hours
	ConfigValue string         `gorm:"type:text;not null" json:"config_value" column_setting:"true"`                   // e.g., 24
	ConfigName  string         `gorm:"type:varchar(100);not null" json:"config_name" column_setting:"true"`            // e.g., Token Expiration Duration (hours)
	Remark      string         `gorm:"type:text" json:"remark" column_setting:"true"`
}

func (SysConfig) TableName() string {
	return "sys_config"
}
