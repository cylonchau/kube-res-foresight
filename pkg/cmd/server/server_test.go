package server

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/config"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func setupJWTSecretDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}))

	prevDB, prevCfg, prevKey := model.DB, config.CONFIG, utils.SecretKey
	model.DB = db
	config.CONFIG = &config.Config{}
	t.Cleanup(func() {
		model.DB, config.CONFIG, utils.SecretKey = prevDB, prevCfg, prevKey
	})
	return db
}

// The regression this guards: with no server.jwt_secret configured, the
// server used to mint a fresh random key on every boot, so restarting it
// invalidated every issued token and logged all users out.
func TestInitJWTSecretSurvivesRestart(t *testing.T) {
	db := setupJWTSecretDB(t)
	ctx := context.Background()

	require.NoError(t, initJWTSecret(ctx))
	first := string(utils.SecretKey)
	require.NotEmpty(t, first)

	// A token minted before the "restart"...
	token, err := utils.GenerateToken(7, "alice", []string{"admin"}, nil, 24)
	require.NoError(t, err)

	// ...simulate a restart: the key is cleared, init runs again against the
	// same database.
	utils.SecretKey = nil
	require.NoError(t, initJWTSecret(ctx))
	assert.Equal(t, first, string(utils.SecretKey), "restart must reuse the persisted key")

	// ...must still verify afterwards.
	claims, err := utils.ParseToken(token)
	require.NoError(t, err, "a token issued before the restart must still be valid after it")
	assert.Equal(t, uint(7), claims.UserID)

	// And exactly one row was persisted, not one per boot.
	var count int64
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", jwtSecretConfigKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestInitJWTSecretPrefersExplicitConfig(t *testing.T) {
	db := setupJWTSecretDB(t)
	ctx := context.Background()

	// A previously persisted key must lose to an explicitly configured one,
	// so an operator rotating server.jwt_secret actually rotates it.
	require.NoError(t, rdb.NewSysConfigDAO(db).UpdateValue(ctx, jwtSecretConfigKey, "persisted-key"))
	config.CONFIG.Server.JWTSecret = "configured-key"

	require.NoError(t, initJWTSecret(ctx))
	assert.Equal(t, "configured-key", string(utils.SecretKey))
}

func TestInitJWTSecretWithoutDatabase(t *testing.T) {
	setupJWTSecretDB(t)
	model.DB = nil

	require.NoError(t, initJWTSecret(context.Background()))
	assert.NotEmpty(t, utils.SecretKey, "must still produce a usable key so the server can serve requests")
}
