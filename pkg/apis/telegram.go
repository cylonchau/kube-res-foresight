package apis

import "time"

// TelegramLoginReq request sent by Telegram Mini App using initData
type TelegramLoginReq struct {
	InitData string `json:"init_data" binding:"required"`
	// Force, when true, revokes any other active TMA session for this
	// account before issuing tokens instead of returning ErrSessionConflict.
	Force bool `json:"force"`
}

// TelegramUserBrief basic user info returned on TMA login
type TelegramUserBrief struct {
	ID          uint     `json:"id"`
	Username    string   `json:"username"`
	NickName    string   `json:"nick_name"`
	Avatar      string   `json:"avatar"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// TelegramLoginResp response returned upon successful Telegram Mini App authentication
type TelegramLoginResp struct {
	AccessToken  string              `json:"access_token"`
	RefreshToken string              `json:"refresh_token,omitempty"`
	ExpiresIn    int64               `json:"expires_in"`
	User         TelegramUserBrief   `json:"user"`
	Binding      TelegramBindingInfo `json:"binding"`
}

// TelegramBindingInfo information about the linked Telegram account
type TelegramBindingInfo struct {
	TelegramUserID   int64     `json:"telegram_user_id"`
	TelegramUsername string    `json:"telegram_username"`
	FirstName        string    `json:"first_name"`
	LastName         string    `json:"last_name"`
	PhotoURL         string    `json:"photo_url"`
	BoundAt          time.Time `json:"bound_at"`
}

// TelegramBindGenerateReq request to generate a one-time binding token
type TelegramBindGenerateReq struct {
	// Empty or optional client notes
}

// TelegramBindGenerateResp response containing one-time bind token & link
type TelegramBindGenerateResp struct {
	Token     string    `json:"token"`
	DeepLink  string    `json:"deep_link"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TelegramBindConfirmReq request to confirm binding via Token (called by Bot or Mini App)
type TelegramBindConfirmReq struct {
	Token    string `json:"token" binding:"required"`
	InitData string `json:"init_data" binding:"required"`
}

// TelegramBindingStatusResp status of Telegram binding for current user
type TelegramBindingStatusResp struct {
	IsBound bool                 `json:"is_bound"`
	Binding *TelegramBindingInfo `json:"binding,omitempty"`
	BotUser string               `json:"bot_username,omitempty"`
}

// TelegramAdminBindingItem is one row of the admin bindings management list.
type TelegramAdminBindingItem struct {
	ID               int64                      `json:"id"`
	CreatedAt        time.Time                  `json:"created_at"`
	UserID           uint                       `json:"user_id"`
	Username         string                     `json:"username"`
	NickName         string                     `json:"nick_name"`
	TelegramUserID   int64                      `json:"telegram_user_id"`
	TelegramUsername string                     `json:"telegram_username"`
	FirstName        string                     `json:"first_name"`
	LastName         string                     `json:"last_name"`
	Status           bool                       `json:"status"`
	BoundAt          time.Time                  `json:"bound_at"`
	LastLoginAt      *time.Time                 `json:"last_login_at"`
	IsOnline         bool                       `json:"is_online"`
	ActiveSessions   []TelegramAdminSessionItem `json:"active_sessions"`
}

// TelegramAdminSessionItem is one active login session shown under a binding
// row — "which device(s) is this user online with". ClientIP/UserAgent are
// shown raw (no UA parsing into a device name, to avoid a bespoke/likely-wrong
// UA-sniffing dependency); LastUsedAt only updates on access-token refresh,
// not every request, so it's an approximation of real-time presence.
type TelegramAdminSessionItem struct {
	ClientIP   string     `json:"client_ip"`
	UserAgent  string     `json:"user_agent"`
	LastUsedAt *time.Time `json:"last_used_at"`
}
