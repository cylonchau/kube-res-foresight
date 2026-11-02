package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func loginGuardTestRouter(t *testing.T) (*AuthRouter, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.LoginFailureRecord{}, &model.LoginCaptchaChallenge{}))
	return &AuthRouter{
		SecurityDAO: rdb.NewLoginSecurityDAO(db),
	}, db
}

func TestLoginCaptchaIsBoundAndSingleUse(t *testing.T) {
	router, _ := loginGuardTestRouter(t)
	const (
		captchaID = "captcha-test"
		loginKey  = "admin|127.0.0.1"
		code      = "AB234"
	)
	var saltBytes [16]byte
	if _, err := rand.Read(saltBytes[:]); err != nil {
		t.Fatal(err)
	}
	salt := hex.EncodeToString(saltBytes[:])
	ctx := context.Background()
	err := router.SecurityDAO.StoreCaptcha(ctx, &model.LoginCaptchaChallenge{
		ID:        captchaID,
		LoginKey:  loginKey,
		Salt:      salt,
		Hash:      captchaDigest(salt, code),
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)

	if !router.verifyLoginCaptcha(ctx, loginKey, captchaID, code) {
		t.Fatal("expected captcha verification to succeed")
	}
	if router.verifyLoginCaptcha(ctx, loginKey, captchaID, code) {
		t.Fatal("expected captcha to be single use")
	}
}

func TestExpiredLoginCaptchaIsRejected(t *testing.T) {
	router, _ := loginGuardTestRouter(t)
	const captchaID = "expired-captcha-test"
	var saltBytes [16]byte
	salt := hex.EncodeToString(saltBytes[:])
	ctx := context.Background()
	err := router.SecurityDAO.StoreCaptcha(ctx, &model.LoginCaptchaChallenge{
		ID:        captchaID,
		LoginKey:  "admin|127.0.0.1",
		Salt:      salt,
		Hash:      captchaDigest(salt, "AB234"),
		ExpiresAt: time.Now().Add(-time.Second),
	})
	require.NoError(t, err)

	if router.verifyLoginCaptcha(ctx, "admin|127.0.0.1", captchaID, "AB234") {
		t.Fatal("expected expired captcha verification to fail")
	}
}
