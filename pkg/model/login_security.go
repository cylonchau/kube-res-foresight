package model

import "time"

// LoginFailureRecord tracks consecutive failed login attempts per (username, IP)
// key so the captcha-required threshold survives across server instances instead
// of living in a single process's memory.
type LoginFailureRecord struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"false"`
	AttemptKey string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"-" column_setting:"false"`
	Count      int       `gorm:"not null;default:0" json:"-" column_setting:"false"`
	ExpiresAt  time.Time `gorm:"index;not null" json:"-" column_setting:"false"`
}

func (LoginFailureRecord) TableName() string { return "login_failure_record" }

func init() { RegisterModel(&LoginFailureRecord{}) }

// LoginCaptchaChallenge stores an issued login captcha so it can be verified
// from any server instance rather than only the one that generated it.
type LoginCaptchaChallenge struct {
	ID        string    `gorm:"type:char(32);primaryKey" json:"-" column_setting:"false"`
	LoginKey  string    `gorm:"type:varchar(255);index;not null" json:"-" column_setting:"false"`
	Salt      string    `gorm:"type:char(32);not null" json:"-" column_setting:"false"`
	Hash      string    `gorm:"type:char(64);not null" json:"-" column_setting:"false"`
	ExpiresAt time.Time `gorm:"index;not null" json:"-" column_setting:"false"`
}

func (LoginCaptchaChallenge) TableName() string { return "login_captcha_challenge" }

func init() { RegisterModel(&LoginCaptchaChallenge{}) }
