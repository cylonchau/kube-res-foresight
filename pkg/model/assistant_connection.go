package model

import "time"

func init() {
	RegisterModel(&AssistantConnection{})
}

// AssistantConnection is a language model the AI assistant may use: the address of a service that speaks the OpenAI chat completions
// protocol, the model name, the key, and the limits of one question. Only one connection is enabled at a time; the assistant uses it. Rows
// are hard-deleted so the unique name is free again. The key is stored encrypted and never returned.
type AssistantConnection struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name     string `gorm:"type:varchar(100);not null;uniqueIndex" json:"name" column_setting:"true"`
	Endpoint string `gorm:"type:varchar(500);not null" json:"endpoint" column_setting:"true"`
	Model    string `gorm:"type:varchar(100);not null" json:"model" column_setting:"true"`
	APIKey   string `gorm:"type:text;not null;default:''" json:"-" column_setting:"false"`
	Enabled  bool   `gorm:"not null;default:false" json:"enabled" column_setting:"true"`
	// TimeoutSeconds bounds one question in all (every call to the model and every read inside it); MaxToolRounds how many times the model
	// may ask for data before it must answer; MaxReplyTokens the length of one reply.
	TimeoutSeconds int    `gorm:"not null;default:60" json:"timeout_seconds" column_setting:"false"`
	MaxToolRounds  int    `gorm:"not null;default:6" json:"max_tool_rounds" column_setting:"false"`
	MaxReplyTokens int    `gorm:"not null;default:1500" json:"max_reply_tokens" column_setting:"false"`
	Remark         string `gorm:"type:varchar(255)" json:"remark" column_setting:"true"`
}

func (AssistantConnection) TableName() string { return "assistant_connection" }
