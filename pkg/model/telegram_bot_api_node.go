package model

import (
	"gorm.io/gorm"
	"time"
)

// TelegramBotAPINode is a self-registering agent node that runs a
// self-hosted telegram-bot-api server (https://github.com/tdlib/telegram-bot-api)
// as a single local Docker container. It follows a self-registration
// lifecycle (pending -> approved/online -> offline/disabled -> rejected)
// and authenticates agent calls with a bearer token.
type TelegramBotAPINode struct {
	ID        int64          `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time      `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time      `json:"updated_at" column_setting:"true"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`

	Name    string `gorm:"type:varchar(128);not null;uniqueIndex" json:"name" column_setting:"true"`
	Status  string `gorm:"type:varchar(20);not null;index" json:"status" column_setting:"true"` // pending/approved/online/offline/disabled/rejected
	Enabled bool   `gorm:"not null;default:true" json:"enabled" column_setting:"true"`

	TokenHash            string `gorm:"type:varchar(64)" json:"-" column_setting:"false"`
	EnrollmentTokenHash  string `gorm:"type:varchar(64)" json:"-" column_setting:"false"`
	IssuedTokenEncrypted string `gorm:"type:text" json:"-" column_setting:"false"`

	Hostname        string     `gorm:"type:varchar(255)" json:"hostname" column_setting:"true"`
	OperatingSystem string     `gorm:"type:varchar(64)" json:"operating_system" column_setting:"true"`
	Architecture    string     `gorm:"type:varchar(64)" json:"architecture" column_setting:"true"`
	Version         string     `gorm:"type:varchar(64)" json:"version" column_setting:"true"`
	LastSeenAt      *time.Time `gorm:"index" json:"last_seen_at" column_setting:"true"`
	ClientIP        string     `gorm:"type:varchar(64)" json:"client_ip" column_setting:"true"`

	// Desired state, set by an admin. The agent polls for this and
	// reconciles its local container to match.
	DesiredRunning   bool   `gorm:"not null;default:false" json:"desired_running" column_setting:"true"`
	Port             int    `gorm:"not null;default:8081" json:"port" column_setting:"true"`
	LocalMode        bool   `gorm:"not null;default:false" json:"local_mode" column_setting:"true"`
	Image            string `gorm:"type:varchar(255);not null;default:'aiogram/telegram-bot-api:latest'" json:"image" column_setting:"true"`
	APIID            string `gorm:"type:varchar(64)" json:"api_id" column_setting:"true"`
	APIHashEncrypted string `gorm:"type:text" json:"-" column_setting:"false"`

	// Observed state, reported by the agent on every heartbeat.
	ObservedRunning   bool   `gorm:"not null;default:false" json:"observed_running" column_setting:"true"`
	ObservedContainer string `gorm:"type:varchar(128)" json:"observed_container" column_setting:"false"`
	ObservedImage     string `gorm:"type:varchar(255)" json:"observed_image" column_setting:"true"`
	LastError         string `gorm:"type:text" json:"last_error" column_setting:"true"`

	Remark string `gorm:"type:text" json:"remark" column_setting:"true"`
}

func (TelegramBotAPINode) TableName() string { return "telegram_bot_api_node" }

func init() {
	RegisterModel(&TelegramBotAPINode{})
}
