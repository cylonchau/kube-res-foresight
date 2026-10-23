package utils

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"strings"
	"testing"
	"time"
)

func TestPasswordJWTOTPAndTokenUtilities(t *testing.T) {
	hash, err := HashPassword("secret")
	require.NoError(t, err)
	assert.True(t, ComparePassword(hash, "secret"))
	assert.False(t, ComparePassword(hash, "wrong"))
	previous := append([]byte(nil), SecretKey...)
	SetSecretKey([]byte("01234567890123456789012345678901"))
	t.Cleanup(func() { SetSecretKey(previous) })
	token, err := GenerateToken(7, "alice", []string{"ADMIN"}, []string{"home"}, 1)
	require.NoError(t, err)
	claims, err := ParseToken(token)
	require.NoError(t, err)
	assert.Equal(t, uint(7), claims.UserID)
	assert.Equal(t, "alice", claims.Username)
	access, err := GenerateAccessToken(7, "alice", []string{"ADMIN"}, []string{"home"}, "family", 1)
	require.NoError(t, err)
	claims, err = ParseToken(access)
	require.NoError(t, err)
	assert.Equal(t, "family", claims.SessionID)
	_, err = ParseToken("invalid")
	assert.Error(t, err)
	ticket, err := GenerateLoginTicket(7)
	require.NoError(t, err)
	uid, err := VerifyLoginTicket(ticket)
	require.NoError(t, err)
	assert.Equal(t, uint(7), uid)
	_, err = VerifyLoginTicket(token)
	assert.Error(t, err)

	secret, qr, err := GenerateOTPSecret("alice")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(qr, "data:image/png;base64,"))
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	assert.True(t, VerifyOTP(secret, code))
	assert.False(t, VerifyOTP(secret, "000000"))
	assert.Len(t, HashToken("token"), 64)
	assert.Len(t, RandomHex(8), 16)
}

func TestCasbinInitializationAndPolicySync(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}))
	previousDB, previousEnforcer := model.DB, Enforcer
	model.DB = db
	Enforcer = nil
	t.Cleanup(func() { model.DB = previousDB; Enforcer = previousEnforcer })
	assert.Error(t, SyncPolicies())
	permission := model.Permission{Name: "Read", Code: "read", Type: "BUTTON", BeApis: `["GET /api/v1/items","invalid"]`, Status: true}
	role := model.Role{Name: "Admin", Code: "ADMIN", Status: true, Permissions: []model.Permission{permission}}
	user := model.User{Username: "alice", PasswordHash: "x", Status: true, Roles: []model.Role{role}}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, InitCasbin())
	require.NoError(t, SyncPolicies())
	allowed, err := Enforcer.Enforce("alice", "/api/v1/items", "GET")
	require.NoError(t, err)
	assert.True(t, allowed)
}
