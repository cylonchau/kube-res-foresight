package apis

import "time"

// TelegramBotAPINodeDTO is the admin-facing representation of a Bot API
// node. Secrets (TokenHash/EnrollmentTokenHash/IssuedTokenEncrypted/
// APIHashEncrypted) are never included.
type TelegramBotAPINodeDTO struct {
	ID              int64      `json:"id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	Enabled         bool       `json:"enabled"`
	Hostname        string     `json:"hostname"`
	OperatingSystem string     `json:"operating_system"`
	Architecture    string     `json:"architecture"`
	Version         string     `json:"version"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	ClientIP        string     `json:"client_ip"`

	DesiredRunning bool   `json:"desired_running"`
	Port           int    `json:"port"`
	LocalMode      bool   `json:"local_mode"`
	Image          string `json:"image"`
	APIID          string `json:"api_id"`
	HasAPIHash     bool   `json:"has_api_hash"`

	ObservedRunning   bool   `json:"observed_running"`
	ObservedContainer string `json:"observed_container"`
	ObservedImage     string `json:"observed_image"`
	LastError         string `json:"last_error"`

	Remark string `json:"remark"`
}

// TelegramBotAPINodeSystemInfo is self-reported by the agent at register
// and heartbeat time.
type TelegramBotAPINodeSystemInfo struct {
	Hostname        string `json:"hostname"`
	OperatingSystem string `json:"operating_system"`
	Architecture    string `json:"architecture"`
	Version         string `json:"version"`
}

// TelegramBotAPINodeRegisterReq is sent by the agent on startup and on every
// pending-status poll. NodeID is empty on the very first call; the agent
// persists whatever ID the server assigns and sends it back on subsequent
// calls so the server can tell a re-registration from a brand-new node.
type TelegramBotAPINodeRegisterReq struct {
	NodeID int64  `json:"node_id"`
	Name   string `json:"name" binding:"required"`
	// Token is the agent-generated enrollment credential on first contact
	// (>=32 random hex chars), also sent as the Authorization bearer.
	Token string `json:"token"`
	TelegramBotAPINodeSystemInfo
}

// TelegramBotAPINodeSession is the register response: Token is populated
// once (bootstrap-token path, or right after admin approval) and must be
// persisted by the agent as its permanent bearer credential.
type TelegramBotAPINodeSession struct {
	NodeID int64  `json:"node_id"`
	Token  string `json:"token,omitempty"`
	Status string `json:"status"`
}

// TelegramBotAPINodeHeartbeatReq reports observed container state.
type TelegramBotAPINodeHeartbeatReq struct {
	ObservedRunning   bool   `json:"observed_running"`
	ObservedContainer string `json:"observed_container"`
	ObservedImage     string `json:"observed_image"`
	LastError         string `json:"last_error"`
	TelegramBotAPINodeSystemInfo
}

// TelegramBotAPINodeConfigSnapshot is what the agent pulls to know what its
// local container should look like. Version is a content hash so the agent
// can skip reconciliation when nothing changed.
type TelegramBotAPINodeConfigSnapshot struct {
	Version        string `json:"version"`
	DesiredRunning bool   `json:"desired_running"`
	Port           int    `json:"port"`
	LocalMode      bool   `json:"local_mode"`
	Image          string `json:"image"`
	APIID          string `json:"api_id"`
	APIHash        string `json:"api_hash,omitempty"`
}

// TelegramBotAPINodeDesiredConfigReq is the admin-facing update to a node's
// desired state. APIHash is only changed when provided non-empty, mirroring
// the "leave blank to keep unchanged" convention used for bot tokens.
type TelegramBotAPINodeDesiredConfigReq struct {
	DesiredRunning *bool   `json:"desired_running"`
	Port           *int    `json:"port"`
	LocalMode      *bool   `json:"local_mode"`
	Image          *string `json:"image"`
	APIID          *string `json:"api_id"`
	APIHash        *string `json:"api_hash"`
	Remark         *string `json:"remark"`
}

// TelegramBotAPINodeListReq is the admin list query.
type TelegramBotAPINodeListReq struct {
	ListOptions
	Keyword *string `form:"keyword" json:"keyword"`
	Status  *string `form:"status" json:"status"`
}

// TelegramBotAPINodeSetEnabledReq enables/disables a node.
type TelegramBotAPINodeSetEnabledReq struct {
	Enabled *bool `json:"enabled" binding:"required"`
}
