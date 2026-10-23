package model

import (
	"gorm.io/gorm"
	"time"
)

const SecretKeyTypeDeveloperAccess = "developer_access"

type SecretKey struct {
	ID             int64          `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt      time.Time      `json:"created_at" column_setting:"true"`
	UpdatedAt      time.Time      `json:"updated_at" column_setting:"true"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	Name           string         `gorm:"type:varchar(128);unique;not null;index" json:"name" column_setting:"true"`
	KeyType        string         `gorm:"type:varchar(64);not null;default:'developer_access';index" json:"key_type" column_setting:"true"`
	AccessKeyHash  *string        `gorm:"type:char(64);uniqueIndex" json:"-" column_setting:"false"`
	EncryptedValue string         `gorm:"type:text;not null" json:"-" column_setting:"false"`
	MaskedValue    string         `gorm:"type:varchar(128);not null" json:"masked_value" column_setting:"true"`
	Status         bool           `gorm:"default:true" json:"status" column_setting:"true"`
	Remark         string         `gorm:"type:text" json:"remark" column_setting:"true"`

	AccessKey     string     `gorm:"-" json:"access_key,omitempty" column_setting:"false"`
	AccessSecret  string     `gorm:"-" json:"access_secret,omitempty" column_setting:"false"`
	ServerEnabled bool       `gorm:"-" json:"server_enabled" column_setting:"false"`
	PermissionIDs []uint     `gorm:"-" json:"permission_ids" column_setting:"false"`
	ExpiresAt     *time.Time `gorm:"-" json:"expires_at,omitempty" column_setting:"false"`
}

func (SecretKey) TableName() string { return "secret_key" }

func init() {
	RegisterModel(&SecretKey{})
}
