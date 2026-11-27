package rdb

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestUserDAO(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.UserContact{}))

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

// Deleting a user removes the row and what hangs on it; the same name can be used again.
func TestDeletingAUserRemovesItFromTheDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.UserContact{}, &model.RefreshTokenSession{}, &model.UserColumnConfig{}))
	dao := NewUserDAO(db)
	ctx := context.Background()

	role := model.Role{Name: "Viewer", Code: "VIEWER", Status: true}
	require.NoError(t, db.Create(&role).Error)
	user := &model.User{Username: "bob", PasswordHash: "x", Status: true, Roles: []model.Role{role}}
	require.NoError(t, dao.Create(ctx, user))
	other := &model.User{Username: "eve", PasswordHash: "x", Status: true, Roles: []model.Role{role}}
	require.NoError(t, dao.Create(ctx, other))
	require.NoError(t, dao.ReplaceContacts(ctx, user.ID, []model.UserContact{{Type: "email", Value: "bob@example.org"}}))

	require.NoError(t, dao.Delete(ctx, user.ID))
	var rows int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("username = ?", "bob").Count(&rows).Error)
	assert.Zero(t, rows, "not even a marked row is left")
	require.NoError(t, db.Model(&model.UserContact{}).Where("user_id = ?", user.ID).Count(&rows).Error)
	assert.Zero(t, rows)
	require.NoError(t, db.Table("sys_user_roles").Where("user_id = ?", user.ID).Count(&rows).Error)
	assert.Zero(t, rows)
	require.NoError(t, db.Table("sys_user_roles").Where("user_id = ?", other.ID).Count(&rows).Error)
	assert.EqualValues(t, 1, rows, "the roles of the others stay")
	require.NoError(t, dao.Create(ctx, &model.User{Username: "bob", PasswordHash: "y", Status: true}), "the name is free again")

	// rows an earlier version only marked as deleted are removed by the same path
	require.NoError(t, db.Delete(&model.User{}, other.ID).Error)
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("username = ?", "eve").Count(&rows).Error)
	require.EqualValues(t, 1, rows)
	require.NoError(t, PurgeUsers(db, func(q *gorm.DB) *gorm.DB { return q.Where("deleted_at IS NOT NULL") }))
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("username = ?", "eve").Count(&rows).Error)
	assert.Zero(t, rows)
}
