package model

import "time"

func init() {
	RegisterModel(&AssistantAudit{})
}

// Where a question to the assistant came from.
const (
	AIAuditChat  = "chat"
	AIAuditSkill = "skill"
	AIAuditTask  = "task"
)

// AssistantAudit is the record of one question put to a language model: who asked, from where, which model answered in which mode, which tools
// the assistant read and which it refused, how long it took and what it cost in tokens. It holds no text of the question or the answer and no
// tool result: an administrator reads these records to see how the assistant is used, never what a user said to it. The hourly clean-up deletes
// the records older than the system parameter assistant.audit_retention_days.
type AssistantAudit struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`

	// UserID is 0 for a scheduled run, which has no user; UserName then says whose task it was when it is known.
	UserID   uint   `gorm:"not null;default:0;index" json:"user_id"`
	UserName string `gorm:"type:varchar(128)" json:"user_name"`
	Source   string `gorm:"type:varchar(10);not null;default:'chat'" json:"source"`
	// ConversationID is the stored conversation the question was asked in, 0 when it was not asked in one.
	ConversationID uint   `gorm:"not null;default:0" json:"conversation_id"`
	Page           string `gorm:"type:varchar(80)" json:"page"`

	ConnectionID uint   `gorm:"not null;default:0" json:"connection_id"`
	Model        string `gorm:"type:varchar(100)" json:"model"`
	ToolMode     string `gorm:"type:varchar(10)" json:"tool_mode"`
	// Redacted says the names of the platform were replaced by aliases in what was sent to the model (assistant.redact_names).
	Redacted bool `gorm:"not null;default:false" json:"redacted"`

	// Status is success or failed; Error is why it failed, cut short (the text of the failure, not of the question).
	Status     string `gorm:"type:varchar(10);not null;index" json:"status"`
	Error      string `gorm:"type:varchar(500)" json:"error"`
	DurationMS int64  `gorm:"not null;default:0" json:"duration_ms"`

	LLMCalls         int `gorm:"not null;default:0" json:"llm_calls"`
	PromptTokens     int `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int `gorm:"not null;default:0" json:"completion_tokens"`

	// Tools is the JSON of the tools that were read ([{name, ok}]); Refused the names the model asked for that the user may not use.
	Tools   string `gorm:"type:text" json:"-"`
	Refused string `gorm:"type:text" json:"-"`
}

func (AssistantAudit) TableName() string { return "assistant_audit" }
