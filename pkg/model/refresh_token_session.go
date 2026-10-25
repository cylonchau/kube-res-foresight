package model

import "time"

type RefreshTokenSession struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"false"`
	UserID    uint   `gorm:"index;index:idx_refresh_session_family_active;not null" json:"user_id" column_setting:"false"`
	User      *User  `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	TokenHash string `gorm:"type:char(64);uniqueIndex;not null" json:"-" column_setting:"false"`
	FamilyID  string `gorm:"type:char(32);index;index:idx_refresh_session_family_active;not null" json:"-" column_setting:"false"`
	// ClientType records which surface this session was issued to ("web" or
	// "tma") — set once at issuance (issueTokenPair)
	// and never changed. It's what lets single-session-per-client-type
	// enforcement tells the surfaces apart,
	// since they otherwise share the exact same JWT/refresh-token mechanism.
	ClientType string     `gorm:"type:varchar(16);index" json:"client_type" column_setting:"false"`
	ExpiresAt  time.Time  `gorm:"index;not null" json:"expires_at" column_setting:"false"`
	RevokedAt  *time.Time `gorm:"index;index:idx_refresh_session_family_active" json:"revoked_at" column_setting:"false"`
	// RevokedReason distinguishes *why* a session was revoked: empty for the
	// ordinary paths (logout, password change, rotation), "kicked" when a
	// same-client-type login forced it out. JWTAuthMiddleware reads this to
	// show "logged in elsewhere" instead of a generic expired-token message.
	RevokedReason  string     `gorm:"type:varchar(32)" json:"-" column_setting:"false"`
	ReplacedByHash string     `gorm:"type:char(64)" json:"-" column_setting:"false"`
	ClientIP       string     `gorm:"type:varchar(64)" json:"client_ip" column_setting:"false"`
	UserAgent      string     `gorm:"type:varchar(512)" json:"user_agent" column_setting:"false"`
	LastUsedAt     *time.Time `json:"last_used_at" column_setting:"false"`
	CreatedAt      time.Time  `json:"created_at" column_setting:"false"`
}

func (RefreshTokenSession) TableName() string { return "refresh_token_session" }

func init() { RegisterModel(&RefreshTokenSession{}) }
