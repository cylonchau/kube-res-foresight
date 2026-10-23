package model

import (
	"time"
)

// BotCommandDef defines a dynamic or well-known command handled by the Telegram Bot.
type BotCommandDef struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`
	// BotID binds the command to one Telegram bot; 0 applies to every bot.
	// A command is unique per bot, so /start can exist on several bots.
	BotID             int64  `gorm:"not null;default:0;uniqueIndex:idx_bot_command_bot_cmd" json:"bot_id" column_setting:"true"`
	Command           string `gorm:"type:varchar(64);not null;uniqueIndex:idx_bot_command_bot_cmd" json:"command" column_setting:"true"`
	Name              string `gorm:"type:varchar(128);not null" json:"name" column_setting:"true"`
	Pattern           string `gorm:"type:varchar(256)" json:"pattern" column_setting:"false"`
	Description       string `gorm:"type:text" json:"description" column_setting:"true"`
	RequiredRolesJSON string `gorm:"type:text" json:"required_roles_json" column_setting:"false"`
	// ActionType selects what MatchCommand-ing this command actually does —
	// "workflow" (default, existing behavior: starts WorkflowKey's pipeline),
	// "reply" (renders ParamsJSON.template_key and replies directly, no
	// instance created), "webhook" (POSTs ParamsJSON, see
	// pkg/workflow.postWebhook), or "handler" (invokes a backend-registered
	// pkg/workflow.CommandHandler named ParamsJSON.handler_name — see
	// Engine.RegisterCommandHandler).
	ActionType  string `gorm:"type:varchar(32);not null;default:'workflow'" json:"action_type" column_setting:"true"`
	WorkflowKey string `gorm:"type:varchar(64)" json:"workflow_key" column_setting:"true"`
	// ParamsJSON holds ActionType-specific config — same "type string +
	// generic KV bag" convention as OutputActionDef.Params, so no dedicated
	// column per action type. See ActionType's doc comment for the expected
	// keys per type.
	ParamsJSON string `gorm:"type:text" json:"params_json" column_setting:"false"`
	// RequireBinding gates whether MatchCommand requires an existing
	// TelegramBinding before dispatching — false for commands whose whole
	// purpose is to create that binding (e.g. a "handler" command wired to
	// confirm_bind), which would otherwise be unreachable since the binding
	// check would always turn them away first.
	RequireBinding bool `gorm:"not null;default:true" json:"require_binding" column_setting:"true"`
	// RequiredParamsJSON is a JSON object of {var_key: human_label} pairs
	// MatchCommand's extracted vars must contain (non-empty) before
	// ExecuteCommand runs, so a command with missing input gets one clear reply
	// instead of silently starting a workflow instance with the field
	// missing (see Engine.requiredParamLabels). Empty means no requirement.
	RequiredParamsJSON string `gorm:"type:text" json:"required_params_json" column_setting:"false"`
	// RelatedTemplateKeysJSON is a JSON array of model.NotifyTemplate.Key
	// values the admin has associated with this command for editing
	// convenience — e.g. /report's success body plus its two guard
	// messages, or every step of a multi-stage workflow.
	// This is admin-managed data, not something this codebase infers or
	// hardcodes: how many templates a given command actually touches (one,
	// several, or none tracked here at all) varies per command and isn't
	// knowable in general from ActionType/WorkflowKey/ParamsJSON alone, so
	// the admin console's command editor lets the admin pick any number of
	// existing NotifyTemplate rows here and edit their bodies inline,
	// instead of a fixed mapping baked into the frontend. Purely a curation
	// aid for that UI — MatchCommand/ExecuteCommand never read this field;
	// which template(s) a command's own logic actually renders at runtime
	// is decided entirely by ParamsJSON.template_key / the handler's own
	// code / the workflow's own action definitions, same as before.
	RelatedTemplateKeysJSON string `gorm:"type:text" json:"related_template_keys_json" column_setting:"false"`
	Enabled                 bool   `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
}

func (BotCommandDef) TableName() string { return "bot_command_def" }

// WorkflowDef defines a state machine pipeline with its states, actions, and transitions.
type WorkflowDef struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt    time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt    time.Time `json:"updated_at" column_setting:"true"`
	WorkflowKey  string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"workflow_key" column_setting:"true"`
	Driver       string    `gorm:"type:varchar(64);not null;default:'telegram'" json:"driver" column_setting:"true"` // telegram, msteams, slack, generic
	Name         string    `gorm:"type:varchar(128);not null" json:"name" column_setting:"true"`
	Description  string    `gorm:"type:text" json:"description" column_setting:"true"`
	InitialState string    `gorm:"type:varchar(64);not null" json:"initial_state" column_setting:"true"`
	PipelineJSON string    `gorm:"type:text;not null" json:"pipeline_json" column_setting:"false"`
	Enabled      bool      `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
}

func (WorkflowDef) TableName() string { return "workflow_def" }

// WorkflowInstance records an execution instance of a workflow pipeline.
type WorkflowInstance struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt    time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt    time.Time `json:"updated_at" column_setting:"true"`
	WorkflowKey  string    `gorm:"type:varchar(64);not null;index" json:"workflow_key" column_setting:"true"`
	CurrentState string    `gorm:"type:varchar(64);not null" json:"current_state" column_setting:"true"`
	Status       string    `gorm:"type:varchar(32);not null;default:'running'" json:"status" column_setting:"true"` // running, completed, rejected, failed
	// BusinessType/BusinessID is a polymorphic reference; no single table always owns it, so
	// it can't be a real FK.
	BusinessType string `gorm:"type:varchar(64);not null;index" json:"business_type" column_setting:"true"`
	BusinessID   int64  `gorm:"not null;index" json:"business_id" column_setting:"true"`
	// This is an audit trail: SET NULL, not CASCADE — deleting the requester
	// User must not erase the instance's own history.
	RequesterUserID *uint `json:"requester_user_id" column_setting:"true"`
	RequesterUser   *User `gorm:"foreignKey:RequesterUserID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	// RequesterTelegramUserID is the raw Telegram platform user ID, not a
	// local table's row, so it isn't a real FK.
	RequesterTelegramUserID int64      `gorm:"index" json:"requester_telegram_user_id" column_setting:"true"`
	RequesterName           string     `gorm:"type:varchar(128)" json:"requester_name" column_setting:"true"`
	ContextDataJSON         string     `gorm:"type:text" json:"context_data_json" column_setting:"false"`
	HistoryJSON             string     `gorm:"type:text" json:"history_json" column_setting:"false"`
	FinishedAt              *time.Time `json:"finished_at" column_setting:"true"`
}

func (WorkflowInstance) TableName() string { return "workflow_instance" }

// WorkflowPendingReply tracks a Telegram user who was just prompted (via
// ForceReply) to type a note for a transition that declared RequireNote —
// consumed by that user's next text message (pkg/telegram/bot.go's
// handleText), or ignored once ExpiresAt has passed. One row per user: a
// new prompt replaces whatever was pending before.
type WorkflowPendingReply struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// TelegramUserID is the raw Telegram platform user ID, not a local
	// table's row, so it isn't a real FK.
	TelegramUserID int64             `gorm:"uniqueIndex;not null" json:"telegram_user_id"`
	InstanceID     int64             `gorm:"not null" json:"instance_id"`
	Instance       *WorkflowInstance `gorm:"foreignKey:InstanceID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	Event          string            `gorm:"type:varchar(64);not null" json:"event"`
	CreatedAt      time.Time         `json:"created_at"`
	ExpiresAt      time.Time         `gorm:"index" json:"expires_at"`
}

func (WorkflowPendingReply) TableName() string { return "workflow_pending_reply" }

func init() {
	RegisterModel(&BotCommandDef{})
	RegisterModel(&WorkflowDef{})
	RegisterModel(&WorkflowInstance{})
	RegisterModel(&WorkflowPendingReply{})
}
