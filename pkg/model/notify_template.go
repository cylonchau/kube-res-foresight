package model

import "time"

// NotifyTemplate is an admin-editable message template for one notification
// kind (see the Key constants below). Body/Title support {placeholder}
// substitution (plain string replacement, not a code-executing template
// engine — templates are admin input, not worth the complexity/risk of a
// real template language for a handful of named placeholders).
// ButtonsJSON holds a serialized []TemplateButton (see pkg/notify/template).
type NotifyTemplate struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Key           string `gorm:"type:varchar(64);not null;uniqueIndex" json:"key" column_setting:"true"`
	Name          string `gorm:"type:varchar(128);not null" json:"name" column_setting:"true"`
	Channel       string `gorm:"type:varchar(32);not null;default:'telegram'" json:"channel" column_setting:"true"`
	TitleTemplate string `gorm:"type:text" json:"title_template" column_setting:"false"`
	BodyTemplate  string `gorm:"type:text;not null" json:"body_template" column_setting:"false"`
	ButtonsJSON   string `gorm:"type:text" json:"buttons_json" column_setting:"false"`
	Enabled       bool   `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
}

func (NotifyTemplate) TableName() string { return "notify_template" }

// Well-known template keys used by generic account binding and bot replies.
const (
	TemplateKeyBindingWelcome        = "binding_welcome"
	TemplateKeyBindFailedMsg         = "bind_failed_msg"
	TemplateKeyStartWelcomeMsg       = "start_welcome_msg"
	TemplateKeyBindConfirmSuccessMsg = "bind_confirm_success_msg"
	TemplateKeyActionUnauthorized    = "action_unauthorized"
	TemplateKeyActionFailed          = "action_failed"
	TemplateKeyActionApproved        = "action_approved"
	TemplateKeyActionRejected        = "action_rejected"
	TemplateKeyBindingRequired       = "binding_required"
	TemplateKeyCommandFailed         = "command_failed"
)

func init() {
	RegisterModel(&NotifyTemplate{})
}
