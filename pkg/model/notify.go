// Package model defines database entities.
package model

import "time"

func init() {
	RegisterModel(&NotificationDelivery{})
}

// NotificationDelivery tracks delivery status, acknowledgment, and escalation for notifications.
type NotificationDelivery struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	NotificationID  string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"notification_id"`
	DedupKey        string    `gorm:"type:varchar(64);not null;index" json:"dedup_key"`
	Channel         string    `gorm:"type:varchar(24);not null" json:"channel"`
	Recipient       string    `gorm:"type:varchar(64);not null" json:"recipient"`
	Title           string    `gorm:"type:varchar(128);not null" json:"title"`
	Content         string    `gorm:"type:text;not null" json:"content"`
	Severity        string    `gorm:"type:varchar(16);not null;default:'INFO';index" json:"severity"`
	Status          string    `gorm:"type:varchar(16);not null;default:'PENDING';index" json:"status"`
	EscalateChannel string    `gorm:"type:varchar(24);not null;default:''" json:"escalate_channel"`
	SentAt          time.Time `json:"sent_at"`
	AckedAt         time.Time `json:"acked_at"`
	RetryCount      int       `gorm:"not null;default:0" json:"retry_count"`
	LastError       string    `gorm:"type:varchar(255);not null;default:''" json:"last_error"`
}

func (NotificationDelivery) TableName() string { return "notification_delivery" }
