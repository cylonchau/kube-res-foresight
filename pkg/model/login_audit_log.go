package model

import "time"

// LoginAuditLog is an append-only record of every login attempt (success
// and failure), kept distinct from LoginFailureRecord (pkg/model, see
// login_security.go) which is a rolling-window lockout counter that gets
// overwritten/pruned and therefore can't answer "how many failed logins
// happened in the last 7 days" — this table exists specifically so the
// login-log page has a real history to show instead of one
// derived from data that's already gone by the time the report runs.
type LoginAuditLog struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	Username string `gorm:"type:varchar(128);index" json:"username" column_setting:"true"`
	ClientIP string `gorm:"type:varchar(64)" json:"client_ip" column_setting:"true"`
	Success  bool   `gorm:"not null;index" json:"success" column_setting:"true"`
	// Method is how the attempt authenticated: password, ldap or otp (the second step of a two-factor login).
	Method string `gorm:"type:varchar(16)" json:"method" column_setting:"true"`
	// Reason is why an attempt failed (invalid_credentials, captcha_required, user_disabled, otp_invalid);
	// "ok" for a success. The user is only ever told "invalid credentials"; the administrator sees the reason.
	Reason    string `gorm:"type:varchar(32);index" json:"reason" column_setting:"true"`
	UserAgent string `gorm:"type:varchar(255)" json:"user_agent" column_setting:"true"`
}

func (LoginAuditLog) TableName() string { return "login_audit_log" }

func init() {
	RegisterModel(&LoginAuditLog{})
}
