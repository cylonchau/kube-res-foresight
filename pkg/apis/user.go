package apis

import "time"

type UserDTO struct {
	ID         uint      `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Username   string    `json:"username"`
	NickName   string    `json:"nickName"`
	Avatar     string    `json:"avatar"`
	Status     bool      `json:"status"`
	Roles      []RoleDTO `json:"roles,omitempty"`
	OtpEnabled bool      `json:"otpEnabled"`
	// Source says where the account is authenticated: local or ldap.
	Source   string           `json:"source"`
	Contacts []UserContactDTO `json:"contacts"`

	LastLoginAt    *time.Time              `json:"lastLoginAt"`
	LastLoginIP    string                  `json:"lastLoginIp"`
	IsOnline       bool                    `json:"isOnline"`
	ActiveSessions []UserActiveSessionItem `json:"activeSessions"`
}

// UserActiveSessionItem is one active login session: which device the user is online with.
type UserActiveSessionItem struct {
	ClientIP   string     `json:"clientIp"`
	UserAgent  string     `json:"userAgent"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type UserListReq struct {
	ListOptions
	Username string `form:"username" json:"username"`
	NickName string `form:"nick_name" json:"nick_name"`
	Status   string `form:"status" json:"status"`
	Source   string `form:"source" json:"source"`
}

// UserContactDTO is one contact of a user: any type name and its value.
type UserContactDTO struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type UserCreateReq struct {
	Username string           `json:"username" binding:"required"`
	Password string           `json:"password" binding:"required"`
	NickName string           `json:"nick_name"`
	Status   bool             `json:"status"`
	RoleIDs  []uint           `json:"role_ids"`
	Contacts []UserContactDTO `json:"contacts"`
}

type UserUpdateReq struct {
	Password string `json:"password"`
	NickName string `json:"nick_name"`
	Status   *bool  `json:"status"`
	RoleIDs  []uint `json:"role_ids"`
	// Contacts replaces the contacts of the user; nil leaves them as they are.
	Contacts []UserContactDTO `json:"contacts"`
}
