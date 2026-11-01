package model

import "time"

func init() {
	RegisterModel(&AssistantConnection{})
	RegisterModel(&AssistantSkill{})
}

// ProviderOpenAI is the one kind of language model service the assistant speaks to: the OpenAI chat completions protocol, which the OpenAI
// API, gateways such as OpenRouter, vLLM, Ollama, DeepSeek and the compatible mode of Qwen all offer.
const ProviderOpenAI = "openai"

// AssistantConnection is a language model the AI assistant may use (the menu AI configuration, LLM management). Several may be enabled;
// the default one answers the questions and the skills that name no model of their own. Rows are hard-deleted so the unique name is free
// again. The key and the values of the custom headers are stored encrypted and never returned.
type AssistantConnection struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name        string `gorm:"type:varchar(100);not null;uniqueIndex" json:"name" column_setting:"true"`
	Description string `gorm:"column:remark;type:varchar(255)" json:"description" column_setting:"true"`
	Provider    string `gorm:"type:varchar(20);not null;default:'openai'" json:"provider" column_setting:"true"`
	Endpoint    string `gorm:"type:varchar(500);not null" json:"endpoint" column_setting:"true"`
	Model       string `gorm:"type:varchar(100);not null" json:"model" column_setting:"true"`
	APIKey      string `gorm:"type:text;not null;default:''" json:"-" column_setting:"false"`
	Enabled     bool   `gorm:"not null;default:false" json:"enabled" column_setting:"true"`
	// IsDefault marks the connection that is used when a question or a skill names none; only one is the default, and it is enabled.
	IsDefault bool `gorm:"not null;default:false" json:"is_default" column_setting:"true"`

	// Advanced settings. TimeoutSeconds bounds one request to the model; SkipTLSVerify and ProxyURL change how it is reached; Headers (JSON
	// list of name and value, encrypted) and CustomParams (JSON object merged into the request body) are for services that want more.
	TimeoutSeconds int      `gorm:"not null;default:90" json:"timeout_seconds" column_setting:"false"`
	SkipTLSVerify  bool     `gorm:"column:skip_tls_verify;not null;default:false" json:"skip_tls_verify" column_setting:"false"`
	ProxyURL       string   `gorm:"column:proxy_url;type:varchar(500)" json:"proxy_url" column_setting:"false"`
	Headers        string   `gorm:"type:text" json:"-" column_setting:"false"`
	CustomParams   string   `gorm:"type:text" json:"custom_params" column_setting:"false"`
	Temperature    *float64 `json:"temperature" column_setting:"false"`
	// MaxTokens is the most tokens a reply may have (0: not sent, the service decides); ContextLength the size of the context of the model in
	// tokens (0: unknown, the conversation is not shortened); MaxToolRounds how many times the model may ask for data in one question.
	MaxTokens     int `gorm:"column:max_reply_tokens;not null;default:0" json:"max_tokens" column_setting:"false"`
	ContextLength int `gorm:"not null;default:0" json:"context_length" column_setting:"false"`
	MaxToolRounds int `gorm:"not null;default:6" json:"max_tool_rounds" column_setting:"false"`
}

func (AssistantConnection) TableName() string { return "assistant_connection" }

// Argument modes of a skill.
const (
	SkillArgNone     = "none"
	SkillArgRequired = "required"
	SkillArgOptional = "optional"
)

// AssistantSkill is a command of the chat (/name). The built-in ones are defined in the code and have a row only to hold what an
// administrator may change about them (whether they are on, and which model they use); a custom one is a prompt written by an
// administrator, put to the language model with the tools of the assistant.
type AssistantSkill struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	// Name is the command without the slash: lower-case letters, digits, - and _.
	Name        string `gorm:"type:varchar(32);not null;uniqueIndex" json:"name" column_setting:"true"`
	Title       string `gorm:"type:varchar(100)" json:"title" column_setting:"true"`
	Description string `gorm:"type:varchar(255)" json:"description" column_setting:"true"`
	Builtin     bool   `gorm:"not null;default:false" json:"builtin" column_setting:"true"`
	// Prompt is the question put to the model; "{{args}}" stands for what the user typed after the command. Empty for a built-in skill.
	Prompt  string `gorm:"type:text" json:"prompt" column_setting:"false"`
	ArgMode string `gorm:"type:varchar(10);not null;default:'none'" json:"arg_mode" column_setting:"false"`
	ArgHint string `gorm:"type:varchar(50)" json:"arg_hint" column_setting:"false"`
	// LLMID is the connection the skill asks; 0 is the default one.
	LLMID   uint `gorm:"column:llm_id;not null;default:0" json:"llm_id" column_setting:"true"`
	Enabled bool `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
}

func (AssistantSkill) TableName() string { return "assistant_skill" }
