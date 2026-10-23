package model

import (
	"gorm.io/gorm"
	"strings"
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
	Language        string           `gorm:"type:varchar(20)" json:"language" column_setting:"true"`
	TelegramBinding *TelegramBinding `gorm:"foreignKey:UserID" json:"telegramBinding,omitempty" column_setting:"false"`
}

// DisplayName returns a user's preferred display name (NickName, falling
// back to Username). Use it for greetings and profile UI; for anyone
// appearing in a workflow — an approver, an operator, a requester — use
// ActorName instead, which is stable and unambiguous.
func DisplayName(u *User) string {
	if u == nil {
		return ""
	}
	if u.NickName != "" {
		return u.NickName
	}
	return u.Username
}

// ActorName renders the single canonical identity string for a person taking
// part in a workflow — used everywhere one appears: audit history, bot
// notifications, the console's ticket detail, the pipeline flowchart, the
// generic record list.
//
//	admin      // has a KubeForecast account, whether or not Telegram is linked
//	zhaosi     // only ever falls back to the Telegram identity when
//	           // there is no linked KubeForecast account at all
//
// The KubeForecast account username is the identity shown, never the nickname
// (it is unique, is what an admin can actually look someone up by, and does
// not change when they edit their profile — a nickname does all three
// wrongly, which is how "处理人：Admin Test User" ended up in notifications
// where a real account handle belonged) and never paired with the Telegram
// handle/ID — one identity, not two glued together with parentheses.
//
// binding may be nil, in which case u's own linked binding is used; passing
// one explicitly covers the chat-inbound path, where the binding is what was
// resolved from the incoming message and u hangs off it.
func ActorName(u *User, binding *TelegramBinding) string {
	if binding == nil && u != nil {
		binding = u.TelegramBinding
	}
	if u == nil && binding != nil {
		u = binding.User
	}

	if u != nil {
		if account := strings.TrimSpace(u.Username); account != "" {
			return account
		}
		if nick := strings.TrimSpace(u.NickName); nick != "" {
			return nick
		}
	}

	if binding != nil {
		if telegram := strings.TrimSpace(binding.TelegramUsername); telegram != "" {
			return telegram
		}
		return strings.TrimSpace(binding.FirstName + " " + binding.LastName)
	}

	return ""
}
