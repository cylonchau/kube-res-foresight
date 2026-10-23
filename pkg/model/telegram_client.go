package model

import (
	"gorm.io/gorm"
	"time"
)

// Client account connection states, written by the Telegram Agent.
const (
	TelegramClientConnected    = "connected"
	TelegramClientDisconnected = "disconnected"
	TelegramClientError        = "error"
)

// Subscription membership states, written by the Telegram Agent.
const (
	TelegramSubPending = "pending"
	TelegramSubJoined  = "joined"
	TelegramSubLeft    = "left"
	TelegramSubError   = "error"
)

// TelegramClientAccount is an MTProto user account used to read messages
// from chats the operator explicitly subscribes to. API hashes and session
// data are encrypted before they reach these columns.
type TelegramClientAccount struct {
	ID                int64          `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt         time.Time      `json:"created_at" column_setting:"true"`
	UpdatedAt         time.Time      `json:"updated_at" column_setting:"true"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`
	Name              string         `gorm:"type:varchar(128);not null" json:"name" column_setting:"true"`
	PhoneNumber       string         `gorm:"type:varchar(32);not null;uniqueIndex" json:"phone_number" column_setting:"true"`
	APIID             int            `gorm:"not null" json:"api_id" column_setting:"true"`
	APIHashEncrypted  string         `gorm:"type:text;not null" json:"-" column_setting:"false"`
	SessionEncrypted  string         `gorm:"type:text" json:"-" column_setting:"false"`
	PhoneCodeHash     string         `gorm:"type:text" json:"-" column_setting:"false"`
	CodeExpiresAt     *time.Time     `json:"-" column_setting:"false"`
	TelegramUserID    int64          `gorm:"index" json:"telegram_user_id" column_setting:"true"`
	TelegramUsername  string         `gorm:"type:varchar(128)" json:"telegram_username" column_setting:"true"`
	TelegramFirstName string         `gorm:"type:varchar(128)" json:"telegram_first_name" column_setting:"true"`
	TelegramLastName  string         `gorm:"type:varchar(128)" json:"telegram_last_name" column_setting:"true"`
	Authorized        bool           `gorm:"not null;default:false;index" json:"authorized" column_setting:"true"`
	Enabled           bool           `gorm:"not null;default:true;index" json:"enabled" column_setting:"true"`
	Connected         bool           `gorm:"-" json:"connected" column_setting:"false"`
	// ConnectionStatus is written by the Telegram Agent (the only process that
	// holds MTProto sessions); Connected is derived from it and SeenAt.
	ConnectionStatus string `gorm:"type:char(12);not null;default:'disconnected'" json:"connection_status" column_setting:"true"`
	// AgentID is the TG Agent the account is bound to ("" = not bound yet; the
	// first agent that connects it claims it).
	AgentID         string     `gorm:"type:varchar(64);index" json:"agent_id" column_setting:"true"`
	SeenAt          *time.Time `json:"-" column_setting:"false"`
	LastConnectedAt *time.Time `json:"last_connected_at" column_setting:"true"`
	LastMessageAt   *time.Time `json:"last_message_at" column_setting:"true"`
	LastError       string     `gorm:"type:text" json:"last_error" column_setting:"true"`
	// SendBlockedUntil is set when Telegram refused the account's messages with PEER_FLOOD (too many messages to
	// people it has no chat with). Until then the account is not used to send and nothing is asked of Telegram,
	// because trying again only prolongs the limit; receiving is not affected.
	SendBlockedUntil  *time.Time `json:"send_blocked_until" column_setting:"true"`
	SendBlockedReason string     `gorm:"type:varchar(255)" json:"send_blocked_reason" column_setting:"false"`
	HasAPIHash        bool       `gorm:"-" json:"has_api_hash" column_setting:"false"`
	HasSession        bool       `gorm:"-" json:"has_session" column_setting:"false"`
}

func (TelegramClientAccount) TableName() string { return "telegram_client_account" }

// SendBlocked says whether Telegram has limited the account's sending and the pause has not ended.
func (a TelegramClientAccount) SendBlocked(now time.Time) bool {
	return a.SendBlockedUntil != nil && a.SendBlockedUntil.After(now)
}

// TelegramClientSubscription selects a single group/channel whose incoming
// messages should be retained. PeerID is unique only inside one account.
type TelegramClientSubscription struct {
	ID         int64                  `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt  time.Time              `json:"created_at" column_setting:"true"`
	UpdatedAt  time.Time              `json:"updated_at" column_setting:"true"`
	DeletedAt  gorm.DeletedAt         `gorm:"index" json:"-" column_setting:"false"`
	AccountID  int64                  `gorm:"not null;index;uniqueIndex:idx_tg_client_subscription_peer" json:"account_id" column_setting:"true"`
	Account    *TelegramClientAccount `gorm:"foreignKey:AccountID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"account,omitempty" column_setting:"false"`
	PeerID     int64                  `gorm:"not null;uniqueIndex:idx_tg_client_subscription_peer" json:"peer_id" column_setting:"true"`
	AccessHash int64                  `json:"-" column_setting:"false"`
	PeerType   string                 `gorm:"type:varchar(32);not null" json:"peer_type" column_setting:"true"`
	Username   string                 `gorm:"type:varchar(128);index" json:"username" column_setting:"true"`
	Title      string                 `gorm:"type:varchar(255)" json:"title" column_setting:"true"`
	Keywords   string                 `gorm:"type:text" json:"keywords" column_setting:"true"`
	Enabled    bool                   `gorm:"not null;default:true;index" json:"enabled" column_setting:"true"`
	// State is the membership the Telegram Agent last reported:
	// pending, joined, left or error.
	State         string     `gorm:"type:char(8);not null;default:'pending'" json:"state" column_setting:"true"`
	LastError     string     `gorm:"type:text" json:"last_error" column_setting:"true"`
	LastMessageAt *time.Time `json:"last_message_at" column_setting:"true"`
	// LastMessageID is the read position: the newest Telegram message of this chat the agent has
	// taken in (cached for forwarding, or dropped by the keyword filter). After a stop or a network
	// break the agent continues from here, so nothing sent meanwhile is lost and nothing is taken
	// twice. 0 means no position yet; the agent sets one when it first sees the subscription.
	LastMessageID int `gorm:"not null;default:0" json:"last_message_id" column_setting:"true"`
	// LatestMessageID is the newest message id the agent has seen in the chat (live, or when it last looked
	// after a start). The gap to LastMessageID is the backlog still to be taken in.
	LatestMessageID int        `gorm:"not null;default:0" json:"latest_message_id" column_setting:"true"`
	LatestSeenAt    *time.Time `json:"latest_seen_at" column_setting:"false"`
}

func (TelegramClientSubscription) TableName() string { return "telegram_client_subscription" }

// Cached message states. A message is a temporary cache entry, not business
// data: it is received, waits to be consumed by the forward rules, and is then
// deleted by the Telegram Agent's maintenance job.
const (
	TelegramMsgPending  = "pending"
	TelegramMsgConsumed = "consumed"
	TelegramMsgFailed   = "failed"
)

// Message source types.
const (
	TelegramSourceChannel = "channel"
	TelegramSourceGroup   = "group"
	TelegramSourcePrivate = "private"
	TelegramSourceSystem  = "system"
)

// TelegramClientMessage is one cached message received by a client account. It
// is deleted after it has been consumed (or when it expires); there is no
// message history.
type TelegramClientMessage struct {
	ID                int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt         time.Time `json:"created_at" column_setting:"true"`
	AccountID         int64     `gorm:"not null;index;uniqueIndex:idx_tg_client_message" json:"account_id" column_setting:"true"`
	SubscriptionID    int64     `gorm:"not null;default:0;index" json:"subscription_id" column_setting:"true"` // 0 for private and system messages
	PeerID            int64     `gorm:"not null;index;uniqueIndex:idx_tg_client_message" json:"peer_id" column_setting:"true"`
	TelegramMessageID int       `gorm:"not null;uniqueIndex:idx_tg_client_message" json:"telegram_message_id" column_setting:"true"`
	SourceType        string    `gorm:"type:char(8);not null;default:'channel';index" json:"source_type" column_setting:"true"`
	Source            string    `gorm:"type:varchar(128)" json:"source" column_setting:"true"`
	SenderID          int64     `gorm:"index" json:"sender_id" column_setting:"true"`
	SenderName        string    `gorm:"type:varchar(255)" json:"sender_name" column_setting:"true"`
	// Body is the message text, compressed (see CompressText). Lists never load
	// it; Text is filled only when a message is read or forwarded.
	Body          []byte     `gorm:"type:blob" json:"-" column_setting:"false"`
	Size          uint16     `gorm:"not null;default:0" json:"size" column_setting:"true"` // original text size in bytes
	Text          string     `gorm:"-" json:"text,omitempty" column_setting:"false"`
	MediaType     string     `gorm:"type:char(5)" json:"media_type" column_setting:"true"`
	MessageAt     time.Time  `gorm:"index" json:"message_at" column_setting:"true"`
	Status        string     `gorm:"type:char(8);not null;default:'pending';index" json:"status" column_setting:"true"`
	Attempts      uint8      `gorm:"not null;default:0" json:"attempts" column_setting:"true"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at" column_setting:"false"`
	DoneRules     string     `gorm:"type:text" json:"-" column_setting:"false"` // JSON ids of the rules already delivered
	LastError     string     `gorm:"type:text" json:"last_error" column_setting:"true"`
	ConsumedAt    *time.Time `json:"consumed_at" column_setting:"true"`
}

func (TelegramClientMessage) TableName() string { return "telegram_client_message" }

func init() {
	RegisterModel(&TelegramClientAccount{})
	RegisterModel(&TelegramClientSubscription{})
	RegisterModel(&TelegramClientMessage{})
}
