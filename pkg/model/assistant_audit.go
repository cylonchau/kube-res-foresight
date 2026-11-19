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

// AssistantAudit is the record of one question put to a language model: who asked, from where, which model answered in which mode, the question
// and the answer, the steps (the tools it read, with what they returned, cut short) and the ones it refused, how long it took and what it cost in
// tokens. An administrator who holds the menu AI audit reads the conversation here (the question is kept as the user typed it, the answer as the
// user read it). The hourly clean-up deletes the records older than the system parameter assistant.audit_retention_days.
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

	// Question and Answer are what the user asked and what they were answered (cut at MaxAuditText); Steps is the JSON of the steps with what each returned.
	Question string `gorm:"type:text" json:"-"`
	Answer   string `gorm:"type:text" json:"-"`
	Steps    string `gorm:"type:text" json:"-"`

	// Tools is the JSON of the tools that were read ([{name, ok}]); Refused the names the model asked for that the user may not use.
	Tools   string `gorm:"type:text" json:"-"`
	Refused string `gorm:"type:text" json:"-"`
}

func (AssistantAudit) TableName() string { return "assistant_audit" }
