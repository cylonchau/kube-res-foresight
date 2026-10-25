package model

import (
	"gorm.io/gorm"
	"time"
)

func init() {
	RegisterModel(&User{})
}

type User struct {
	ID           uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt    time.Time      `json:"createdAt" column_setting:"true"`
	UpdatedAt    time.Time      `json:"updatedAt" column_setting:"true"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	Username     string         `gorm:"type:varchar(50);not null;uniqueIndex" json:"username" column_setting:"true"`
	PasswordHash string         `gorm:"type:varchar(255);not null" json:"-" column_setting:"false"`
	NickName     string         `gorm:"type:varchar(50)" json:"nickName" column_setting:"true"`
	Avatar       string         `gorm:"type:varchar(255)" json:"avatar" column_setting:"true"`
	Status       bool           `gorm:"default:true" json:"status" column_setting:"true"` // true: enabled, false: disabled
	Roles        []Role         `gorm:"many2many:sys_user_roles;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"roles,omitempty"`
	OtpSecret    string         `gorm:"type:varchar(100)" json:"-" column_setting:"false"`
	OtpEnabled   bool           `gorm:"default:false" json:"otpEnabled" column_setting:"true"`
	Email        string         `gorm:"type:varchar(100)" json:"email" column_setting:"true"`
	Phone        string         `gorm:"type:varchar(20)" json:"phone" column_setting:"true"`
	// Language is the user's explicitly chosen UI language (e.g. "zh-CN",
	// "en-US"). Empty means no preference has been set yet, in which case
	// clients fall back to system/browser language detection — this field
	// only exists so a deliberate choice follows the user across devices
	// and survives clearing local browser storage.
	// Source says where the account is authenticated: local (password hash in this database, always usable as the
	// break-glass path) or ldap (password checked against the directory). ExternalID is the directory DN.
	// ExternalDisabled marks an account the directory sync disabled, so it can be re-enabled automatically when
	// the directory enables it again, while an administrator's own disable (Status=false alone) always sticks.
	Source           string `gorm:"type:varchar(16);not null;default:'local';index" json:"source" column_setting:"true"`
	ExternalID       string `gorm:"type:varchar(512)" json:"externalId" column_setting:"false"`
	ExternalDisabled bool   `gorm:"not null;default:false" json:"externalDisabled" column_setting:"false"`
	Language         string `gorm:"type:varchar(20)" json:"language" column_setting:"true"`
}

// DisplayName returns a user's preferred display name (NickName, falling back to Username).
func DisplayName(u *User) string {
	if u == nil {
		return ""
	}
	if u.NickName != "" {
		return u.NickName
	}
	return u.Username
}

// User sources.
const (
	UserSourceLocal = "local"
	UserSourceLDAP  = "ldap"
)
