package apis

import "time"

type UserDTO struct {
	ID              uint                `json:"id"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	Username        string              `json:"username"`
	NickName        string              `json:"nickName"`
	Avatar          string              `json:"avatar"`
	Status          bool                `json:"status"`
	Roles           []RoleDTO           `json:"roles,omitempty"`
	OtpEnabled      bool                `json:"otpEnabled"`
	Email           string              `json:"email"`
	Phone           string              `json:"phone"`
	TelegramBinding *TelegramBindingDTO `json:"telegramBinding,omitempty"`

	LastLoginAt    *time.Time              `json:"lastLoginAt"`
	LastLoginIP    string                  `json:"lastLoginIp"`
	IsOnline       bool                    `json:"isOnline"`
	ActiveSessions []UserActiveSessionItem `json:"activeSessions"`
}

// UserActiveSessionItem is one active login session — "which device(s) is
// this user online with". Mirrors apis.TelegramAdminSessionItem (same
// underlying RefreshTokenSession data) but camelCase-tagged to match this
// DTO family's existing convention.
type UserActiveSessionItem struct {
	ClientIP   string     `json:"clientIp"`
	UserAgent  string     `json:"userAgent"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// TelegramBindingDTO mirrors model.TelegramBinding without the circular User
// back-reference.
type TelegramBindingDTO struct {
	ID               int64     `json:"id"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	UserID           uint      `json:"userId"`
	TelegramUserID   int64     `json:"telegramUserId"`
	TelegramUsername string    `json:"telegramUsername"`
	FirstName        string    `json:"firstName"`
	LastName         string    `json:"lastName"`
	PhotoURL         string    `json:"photoUrl"`
	AuthDate         time.Time `json:"authDate"`
	Status           bool      `json:"status"`
}

type UserListReq struct {
	ListOptions
	Username string `form:"username" json:"username"`
	NickName string `form:"nick_name" json:"nick_name"`
	Status   string `form:"status" json:"status"`
}

type UserCreateReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	NickName string `json:"nick_name"`
	Status   bool   `json:"status"`
	RoleIDs  []uint `json:"role_ids"`
}

type UserUpdateReq struct {
	Password string `json:"password"`
	NickName string `json:"nick_name"`
	Status   *bool  `json:"status"`
	RoleIDs  []uint `json:"role_ids"`
}
