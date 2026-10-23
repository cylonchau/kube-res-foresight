package model

import "time"

func init() {
	RegisterModel(&SystemMarker{})
}

// SystemMarker is bookkeeping the program keeps for itself: that a migration or a first-time setup already ran, when a collector last
// looked at something, which version of a dataset it holds. It is not a setting and nobody edits it, so it is kept apart from the system
// parameters (sys_config), which are what a person manages.
type SystemMarker struct {
	Key       string    `gorm:"primaryKey;type:varchar(128)" json:"key"`
	Value     string    `gorm:"type:text;not null;default:''" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SystemMarker) TableName() string { return "system_marker" }
