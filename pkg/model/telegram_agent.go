package model

import (
	"gorm.io/gorm"
	"time"
)

// Telegram bot connection modes. Only polling is implemented by the Telegram
// Agent today; the column exists so a webhook consumer can be added without a
// schema change.
const (
	TelegramModePolling = "polling"
	TelegramModeWebhook = "webhook"
)

// TelegramBot runtime states written by the Telegram Agent.
const (
	TelegramBotStopped = "stopped"
	TelegramBotRunning = "running"
	TelegramBotError   = "error"
)

// TelegramBot is one Bot API bot (1..N per system). It is independent of any
// Client Account or channel subscription. The token is encrypted at rest and
// is never returned by the API.
type TelegramBot struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt        time.Time  `json:"created_at" column_setting:"true"`
	UpdatedAt        time.Time  `json:"updated_at" column_setting:"true"`
	Name             string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"name" column_setting:"true"`
	Username         string     `gorm:"type:varchar(64);index" json:"username" column_setting:"true"`
	TokenEncrypted   string     `gorm:"type:text;not null" json:"-" column_setting:"false"`
	Mode             string     `gorm:"type:char(8);not null;default:'polling'" json:"mode" column_setting:"true"`
	APIBaseURL       string     `gorm:"type:varchar(255)" json:"api_base_url" column_setting:"false"`
	MiniAppURL       string     `gorm:"type:varchar(255)" json:"mini_app_url" column_setting:"false"`
	Enabled          bool       `gorm:"not null;default:true;index" json:"enabled" column_setting:"true"`
	Status           string     `gorm:"type:char(8);not null;default:'stopped'" json:"status" column_setting:"true"`
	LastError        string     `gorm:"type:text" json:"last_error" column_setting:"true"`
	BotUserID        int64      `json:"bot_user_id" column_setting:"false"`
	StartedAt        *time.Time `json:"started_at" column_setting:"true"`
	SeenAt           *time.Time `json:"seen_at" column_setting:"false"`
	CommandsHash     string     `gorm:"type:char(16)" json:"-" column_setting:"false"`
	CommandsSyncedAt *time.Time `json:"commands_synced_at" column_setting:"true"`
	// Connected is derived: the Telegram Agent reported it running recently.
	Connected bool `gorm:"-" json:"connected" column_setting:"false"`
	HasToken  bool `gorm:"-" json:"has_token" column_setting:"false"`
	// CommandCount is filled by the API for the list view.
	CommandCount int64 `gorm:"-" json:"command_count" column_setting:"false"`
}

func (TelegramBot) TableName() string { return "telegram_bot" }

// Telegram Agent request kinds. The admin API never talks to Telegram: it
// queues one of these and the Telegram Agent process executes it.
const (
	AgentReqSendCode       = "account.send_code"
	AgentReqVerifyCode     = "account.verify_code"
	AgentReqConnect        = "account.connect"
	AgentReqDisconnect     = "account.disconnect"
	AgentReqLogout         = "account.logout"
	AgentReqSubscribe      = "subscription.join"
	AgentReqUnsubscribe    = "subscription.leave"
	AgentReqSyncBotCommand = "bot.sync_commands"
	AgentReqSendMessage    = "account.send_message"
)

// Telegram Agent request states.
const (
	AgentReqPending = "pending"
	AgentReqRunning = "running"
	AgentReqDone    = "done"
	AgentReqFailed  = "failed"
)

// TelegramAgentRequest is the control channel between the Web/API process and
// the Telegram Agent. The payload (login codes, 2FA passwords) is encrypted
// and cleared as soon as the request has been handled.
type TelegramAgentRequest struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Kind             string     `gorm:"type:varchar(24);not null;index" json:"kind"`
	TargetID         int64      `gorm:"not null;default:0" json:"target_id"`
	PayloadEncrypted string     `gorm:"type:text" json:"-"`
	Status           string     `gorm:"type:char(8);not null;default:'pending';index" json:"status"`
	Error            string     `gorm:"type:text" json:"error"`
	ResultJSON       string     `gorm:"type:text" json:"result_json"`
	ClaimedAt        *time.Time `json:"claimed_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

func (TelegramAgentRequest) TableName() string { return "telegram_agent_request" }

// Forward rule target kinds: every target is an existing system capability.
const (
	ForwardTargetNotify   = "notify"   // enqueue a notify job (channel + recipient)
	ForwardTargetJob      = "job"      // enqueue a job of TargetRef (LLM rating, tagging, ...)
	ForwardTargetWorkflow = "workflow" // start the pipeline TargetRef
	ForwardTargetRedis    = "redis"    // push the message to the Redis queue TargetRef
	ForwardTargetKafka    = "kafka"    // produce the message to the Kafka topic TargetRef
	// ForwardTargetChannel writes the message to a forward channel (TelegramForwardChannel), which
	// names the place it is written, its consumer and the module that finally handles it. It
	// replaces job, workflow, redis and kafka, which only survive as the kinds old rules were saved
	// with before they were moved to channels.
	ForwardTargetChannel = "channel"
)

// Forward classes. Both are "forwarding a message", to a different kind of target.
const (
	ForwardClassTask = "task" // to a Job, Workflow, Redis queue or Kafka topic; later tasks filter and process it
	ForwardClassUser = "user" // to other users, through Notify
)

// ForwardClassOf says which class a target kind belongs to.
func ForwardClassOf(kind string) string {
	if kind == ForwardTargetNotify {
		return ForwardClassUser
	}
	return ForwardClassTask
}

// ForwardKindsOf lists the target kinds of a class.
func ForwardKindsOf(class string) []string {
	if class == ForwardClassUser {
		return []string{ForwardTargetNotify}
	}
	return []string{ForwardTargetChannel}
}

// TelegramForwardRule sends every matching received message to another system
// capability. A subscription only receives; this is the separate Action.
type TelegramForwardRule struct {
	ID              int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt       time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt       time.Time `json:"updated_at" column_setting:"true"`
	Name            string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"name" column_setting:"true"`
	AccountID       int64     `gorm:"not null;default:0;index" json:"account_id" column_setting:"true"`          // 0 = every account
	SubscriptionID  int64     `gorm:"not null;default:0;index" json:"subscription_id" column_setting:"true"`     // 0 = every subscription
	SourceType      string    `gorm:"type:char(8);not null;default:''" json:"source_type" column_setting:"true"` // "" = every source type
	Pattern         string    `gorm:"type:varchar(256)" json:"pattern" column_setting:"true"`
	TargetKind      string    `gorm:"type:char(8);not null" json:"target_kind" column_setting:"true"`
	TargetClass     string    `gorm:"-" json:"target_class" column_setting:"true"` // task | user, derived from TargetKind
	TargetRef       string    `gorm:"type:varchar(64);not null" json:"target_ref" column_setting:"true"`
	TargetRecipient string    `gorm:"type:varchar(64)" json:"target_recipient" column_setting:"true"`
	// ChannelID is the forward channel a task rule writes to (0 for a rule that notifies users).
	ChannelID int64 `gorm:"not null;default:0;index" json:"channel_id" column_setting:"true"`
	// ChannelName is the name of that channel, filled in lists.
	ChannelName   string `gorm:"-" json:"channel_name"`
	SendAccountID int64  `gorm:"not null;default:0" json:"send_account_id" column_setting:"true"` // user target: the Telegram account that sends; 0 = the first connected one
	Enabled       bool   `gorm:"not null;default:true;index" json:"enabled" column_setting:"true"`
	Remark        string `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`
}

func (TelegramForwardRule) TableName() string { return "telegram_forward_rule" }

// AfterFind fills the derived class.
func (r *TelegramForwardRule) AfterFind(*gorm.DB) error {
	r.TargetClass = ForwardClassOf(r.TargetKind)
	return nil
}

// TelegramAgentStatus is the heartbeat one TG Agent writes about itself: what
// it runs, how its message cache looks and how its jobs did. The platform only
// reads it.
type TelegramAgentStatus struct {
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	AgentID   string     `gorm:"type:varchar(64);not null;uniqueIndex" json:"agent_id"`
	Version   string     `gorm:"type:varchar(32)" json:"version"`
	StartedAt time.Time  `json:"started_at"`
	SeenAt    *time.Time `json:"seen_at"`
	Accounts  uint16     `json:"accounts"`  // accounts connected by this agent
	Bots      uint16     `json:"bots"`      // bots running in this process
	Pending   uint32     `json:"pending"`   // cached messages waiting to be consumed
	Forwarded uint64     `json:"forwarded"` // messages delivered since start
	Failed    uint64     `json:"failed"`    // deliveries that failed since start
	LastError string     `gorm:"type:text" json:"last_error"`
	JobsJSON  string     `gorm:"type:text" json:"jobs_json"` // JSON array of job states
	Online    bool       `gorm:"-" json:"online"`
}

func (TelegramAgentStatus) TableName() string { return "telegram_agent_status" }

func init() {
	RegisterModel(&TelegramAgentStatus{})
	RegisterModel(&TelegramBot{})
	RegisterModel(&TelegramAgentRequest{})
	RegisterModel(&TelegramForwardRule{})
}
