package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestUserDAO(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.TelegramBinding{}))

	dao := NewUserDAO(db)
	ctx := context.Background()

	// 1. Create
	user := &model.User{
		Username:     "testuser",
		PasswordHash: "hashed",
		NickName:     "Test",
		Status:       true,
	}
	require.NoError(t, dao.Create(ctx, user))
	assert.NotZero(t, user.ID)

	// 2. GetByID
	fetched, err := dao.GetByID(ctx, user.ID)
	assert.NoError(t, err)
	assert.NotNil(t, fetched)
	assert.Equal(t, "testuser", fetched.Username)
	assert.Equal(t, "Test", fetched.NickName)

	// 3. Update
	fetched.NickName = "Updated Nick"
	require.NoError(t, dao.Update(ctx, fetched))

	updated, err := dao.GetByID(ctx, user.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated Nick", updated.NickName)
}
