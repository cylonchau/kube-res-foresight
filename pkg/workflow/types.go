package workflow

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"time"
)

// Pipeline represents the configuration of a workflow state machine.
type Pipeline struct {
	WorkflowKey   string              `json:"workflow_key"`
	Driver        string              `json:"driver,omitempty"` // "telegram", "msteams", "slack", "generic"
	Name          string              `json:"name"`
	InitialState  string              `json:"initial_state"`
	States        map[string]StateDef `json:"states"`
	OutputActions []OutputActionDef   `json:"output_actions,omitempty"`
}

// StateDef defines a single state inside the pipeline.
type StateDef struct {
	Name          string                   `json:"name"`
	IsTerminal    bool                     `json:"is_terminal"`
	EntryActions  []ActionDef              `json:"entry_actions,omitempty"`
	Transitions   map[string]TransitionDef `json:"transitions,omitempty"`
	OutputActions []OutputActionDef        `json:"output_actions,omitempty"`
}

// ManualFieldDef defines a required user input field during manual execution.
type ManualFieldDef struct {
	Key         string   `json:"key"`      // e.g. "assignee", "priority", "note"
	Label       string   `json:"label"`    // e.g. "处理人", "优先级", "备注"
	Type        string   `json:"type"`     // "text", "number", "select", "textarea"
	Required    bool     `json:"required"` // true / false
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`

	// Step is the increment (and therefore the decimal precision) of a
	// "number" field — "0.01" for a price in yuan, "1" for a whole count.
	// Declared here rather than assumed by the console so a pipeline that
	// collects decimal values and one that collects whole counts both get
	// the right input without the frontend hardcoding either. Empty means
	// the console's default (integers).
	Step string `json:"step,omitempty"`

	// OptionsSource names a backend-registered OptionSource (see
	// options.go) to populate a "select" field from live data instead of
	// the static Options list above — e.g. "active_users", so the
	// operator picks a real item by name rather than typing its
	// database ID. Ignored when Options is non-empty.
	OptionsSource string `json:"options_source,omitempty"`

	// Repeatable means this field can collect more than one value. The admin console
	// renders it as N inputs (seeded from however many values the ticket's
	// context already has for Key, comma-separated) with an add/remove
	// control once there's more than one; a request for just one value
	// still renders as the same plain single input a non-repeatable field
	// would. DispatchEvent's extraVars stays map[string]string — the
	// collected values are joined back into one comma-separated string
	// under Key, so no wire-format change is needed.
	Repeatable bool `json:"repeatable,omitempty"`

	// Group, when set, ties this Repeatable field to every other
	// Repeatable field sharing the same Group value into ONE shared row
	// set — e.g. related fields grouped as "items" render as a single
	// add/remove-able table, instead of independently
	// growable lists whose Nth entries could drift out of correspondence.
	// This keeps the values in each row aligned.
	// Ignored when Repeatable is false; a Repeatable field with no Group
	// renders as its own single-column list, unpaired.
	Group string `json:"group,omitempty"`
}

// OutputActionDef defines an output operation executed when reaching a state or concluding a workflow.
type OutputActionDef struct {
	Type          string            `json:"type"`           // "webhook_post", "notify_channel", "custom_handler"
	Name          string            `json:"name"`           // e.g. "外部系统 Webhook"
	ExecutionMode string            `json:"execution_mode"` // "auto" | "manual"
	Params        map[string]string `json:"params,omitempty"`
	Comment       string            `json:"comment,omitempty"`
}

// ActionDef defines an action executed upon entering a state.
type ActionDef struct {
	Type        string            `json:"type"` // "notify", "toast", "side_effect"
	TemplateKey string            `json:"template_key,omitempty"`
	Recipient   string            `json:"recipient,omitempty"` // "$.requester", "role:APPROVER", "role:OPS", "user:<id>"
	Text        string            `json:"text,omitempty"`
	Buttons     []ActionButtonDef `json:"buttons,omitempty"`
	Vars        map[string]string `json:"vars,omitempty"`
	Handler     string            `json:"handler,omitempty"`
	Comment     string            `json:"comment,omitempty"`
}

// ActionButtonDef defines a button attached to a notification in a state action.
type ActionButtonDef struct {
	Label      string `json:"label"`
	ActionType string `json:"action_type"` // "callback", "webapp", "url"
	Event      string `json:"event,omitempty"`
	TargetURL  string `json:"target_url,omitempty"`
}

// TransitionDef defines a transition from one state to target_state upon event.
type TransitionDef struct {
	TargetState string `json:"target_state"`
	GuardRole   string `json:"guard_role,omitempty"` // e.g. "APPROVER", "OPS", "ADMIN"
	Comment     string `json:"comment,omitempty"`    // pipeline-author-facing note about this transition

	// Label is this action's human-facing name — shown on the admin
	// console's action button and on the pipeline flowchart's edge (e.g.
	// "同意" / "拒绝") in place of the raw event key. It is the same action
	// a chat platform offers as a callback button, so the console and the
	// Telegram card stay in sync by construction rather than by convention.
	//
	// Empty is fine: the console falls back to the label of the notify
	// button in this state's entry_actions that fires the same event, and
	// only then to the event key itself — so a pipeline authored before
	// this field existed still renders a proper label without a migration.
	Label string `json:"label,omitempty"`

	// RequireNote, when true, means DispatchEvent's note argument must be
	// non-empty for this transition to fire (e.g. a rejection reason).
	// pkg/telegram's callback handler enforces this conversationally via
	// ForceReply — see CheckTransition/DispatchEvent's ErrNoteRequired.
	RequireNote bool `json:"require_note,omitempty"`
	// NotePrompt is the chat prompt shown when RequireNote is true; a
	// generic default is used if empty.
	NotePrompt string `json:"note_prompt,omitempty"`

	// ManualFields are structured fields this transition's caller must
	// supply via DispatchEvent's extraVars (Required ones are validated) —
	// rendered as a real form by the web admin/TMA, not collected via chat.
	ManualFields []ManualFieldDef `json:"manual_fields,omitempty"`
}

// HistoryEntry records a state transition audit item.
type HistoryEntry struct {
	FromState string    `json:"from_state"`
	ToState   string    `json:"to_state"`
	Event     string    `json:"event"`
	Operator  string    `json:"operator"`
	Note      string    `json:"note,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Button and ChatDriver — the Sender abstraction — live in sender_types.go
// and sender_trait.go, not here: see those files.

// TemplateResolver loads a NotifyTemplate by key.
type TemplateResolver interface {
	GetByKey(ctx context.Context, key string) (*model.NotifyTemplate, error)
}

// BindingResolver loads Telegram bindings.
type BindingResolver interface {
	GetBindingByTelegramID(ctx context.Context, tgID int64) (*model.TelegramBinding, error)
	ListActiveByRole(ctx context.Context, roleCode string) ([]model.TelegramBinding, error)
	ListActiveAdmins(ctx context.Context) ([]model.TelegramBinding, error)
}

// WorkflowStore persists workflow definitions and instances.
type WorkflowStore interface {
	GetWorkflowDef(ctx context.Context, key string) (*model.WorkflowDef, error)
	GetCommand(ctx context.Context, cmd string) (*model.BotCommandDef, error)
	ListCommands(ctx context.Context) ([]model.BotCommandDef, error)
	CreateInstance(ctx context.Context, inst *model.WorkflowInstance) error
	GetInstance(ctx context.Context, id int64) (*model.WorkflowInstance, error)
	GetInstanceByBusiness(ctx context.Context, bType string, bID int64) (*model.WorkflowInstance, error)
	UpdateInstanceState(ctx context.Context, id int64, fromState, state, status, historyJSON, contextJSON string, finishedAt *time.Time) error
}
