package model

import (
	"gorm.io/gorm"
	"time"
)

func init() {
	RegisterModel(&TelegramBinding{})
}

// TelegramBinding represents the 1:1 or 1:N binding between KubeForecast User and Telegram User.
//
// JSON tags are snake_case to match every other model in this codebase
// (and the admin DTOs actually served over the wire, apis.TelegramAdminBindingItem
// et al.) — pkg/dao/rdb/column_meta.go's GetModelColumnsByPage reflects
// directly over these tags to build the frontend's column-settings/expand-row
// metadata, so a mismatched tag here surfaces as an untranslatable raw key
// with no matching data in the admin bindings page.
type TelegramBinding struct {
	ID               int64          `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt        time.Time      `json:"created_at" column_setting:"true"`
	UpdatedAt        time.Time      `json:"updated_at" column_setting:"true"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	UserID           uint           `gorm:"not null;index" json:"user_id" column_setting:"false"`
	User             *User          `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"user,omitempty"`
	TelegramUserID   int64          `gorm:"not null;uniqueIndex" json:"telegram_user_id" column_setting:"true"`
	TelegramUsername string         `gorm:"type:varchar(100)" json:"telegram_username" column_setting:"true"`
	FirstName        string         `gorm:"type:varchar(100)" json:"first_name" column_setting:"true"`
	LastName         string         `gorm:"type:varchar(100)" json:"last_name" column_setting:"true"`
	PhotoURL         string         `gorm:"type:varchar(255)" json:"photo_url" column_setting:"false"`
	AuthDate         time.Time      `json:"auth_date" column_setting:"false"`
	Status           bool           `gorm:"default:true" json:"status" column_setting:"true"` // true: active, false: disabled
}
