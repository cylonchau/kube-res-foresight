package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"time"
)

// LoginSecurityDAO backs the login-lockout/captcha state that used to live in
// process-local sync.Maps, so it stays consistent when the API runs as
// multiple instances behind a load balancer.
type LoginSecurityDAO struct{ db *gorm.DB }

func NewLoginSecurityDAO(db *gorm.DB) *LoginSecurityDAO { return &LoginSecurityDAO{db: db} }

// RegisterFailure records a failed login attempt for key and returns the
// resulting count within the current window, starting a fresh window if the
// previous one already expired.
func (dao *LoginSecurityDAO) RegisterFailure(ctx context.Context, key string, window time.Duration) (int, error) {
	now := time.Now()
	dao.pruneExpired(ctx, now)

	var rec model.LoginFailureRecord
	err := dao.db.WithContext(ctx).Where("attempt_key = ?", key).First(&rec).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		rec = model.LoginFailureRecord{AttemptKey: key, Count: 1, ExpiresAt: now.Add(window)}
		if err := dao.db.WithContext(ctx).Create(&rec).Error; err != nil {
			return 0, err
		}
		return rec.Count, nil
	case err != nil:
		return 0, err
	}

	if now.After(rec.ExpiresAt) {
		rec.Count = 0
	}
	rec.Count++
	rec.ExpiresAt = now.Add(window)
	if err := dao.db.WithContext(ctx).Save(&rec).Error; err != nil {
		return 0, err
	}
	return rec.Count, nil
}

// CaptchaRequired reports whether key is currently past the failure threshold.
func (dao *LoginSecurityDAO) CaptchaRequired(ctx context.Context, key string, threshold int) (bool, error) {
	var rec model.LoginFailureRecord
	err := dao.db.WithContext(ctx).Where("attempt_key = ?", key).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().After(rec.ExpiresAt) {
		return false, nil
	}
	return rec.Count >= threshold, nil
}

// ClearFailures resets the failure counter for key, e.g. after a successful login.
func (dao *LoginSecurityDAO) ClearFailures(ctx context.Context, key string) error {
	return dao.db.WithContext(ctx).Where("attempt_key = ?", key).Delete(&model.LoginFailureRecord{}).Error
}

// StoreCaptcha persists a newly generated captcha challenge.
func (dao *LoginSecurityDAO) StoreCaptcha(ctx context.Context, challenge *model.LoginCaptchaChallenge) error {
	dao.pruneExpired(ctx, time.Now())
	return dao.db.WithContext(ctx).Create(challenge).Error
}

// ConsumeCaptcha loads and deletes the challenge identified by id (captchas
// are single-use). ok is false if the id is unknown or already consumed.
func (dao *LoginSecurityDAO) ConsumeCaptcha(ctx context.Context, id string) (challenge *model.LoginCaptchaChallenge, ok bool, err error) {
	var rec model.LoginCaptchaChallenge
	if err = dao.db.WithContext(ctx).Where("id = ?", id).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err = dao.db.WithContext(ctx).Delete(&rec).Error; err != nil {
		return nil, false, err
	}
	return &rec, true, nil
}

func (dao *LoginSecurityDAO) pruneExpired(ctx context.Context, now time.Time) {
	dao.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.LoginFailureRecord{})
	dao.db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.LoginCaptchaChallenge{})
}
