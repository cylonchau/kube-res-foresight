package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestTelegramBindingDAO(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}, &model.TelegramBinding{}))

	ctx := context.Background()
	userDAO := NewUserDAO(db)
	bindingDAO := NewTelegramBindingDAO(db)

	user := &model.User{
		Username:     "alice",
		PasswordHash: "passhash",
		NickName:     "Alice Admin",
		Status:       true,
	}
	require.NoError(t, userDAO.Create(ctx, user))

	// 1. Generate Bind Token
	bindResp, err := bindingDAO.GenerateBindToken(ctx, user.ID, "KubeForecastTradingBot")
	require.NoError(t, err)
	require.NotNil(t, bindResp)
	assert.NotEmpty(t, bindResp.Token)
	assert.Contains(t, bindResp.DeepLink, "KubeForecastTradingBot?start="+bindResp.Token)

	// 2. Confirm Bind with Token
	tgUser := &model.TelegramUser{
		ID:        123456789,
		FirstName: "Alice",
		LastName:  "Smith",
		Username:  "alice_tg",
		PhotoURL:  "https://t.me/i/userpic/alice.jpg",
	}

	binding, err := bindingDAO.ConfirmBindWithToken(ctx, bindResp.Token, tgUser, time.Now())
	require.NoError(t, err)
	require.NotNil(t, binding)
	assert.Equal(t, user.ID, binding.UserID)
	assert.Equal(t, int64(123456789), binding.TelegramUserID)
	assert.Equal(t, "alice_tg", binding.TelegramUsername)
	assert.True(t, binding.Status)

	// 3. Token cannot be reused
	_, err = bindingDAO.ConfirmBindWithToken(ctx, bindResp.Token, tgUser, time.Now())
	assert.ErrorIs(t, err, ErrTokenExpiredOrBad)

	// 4. Query binding by Telegram User ID
	fetched, err := bindingDAO.GetBindingByTelegramID(ctx, 123456789)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, user.ID, fetched.UserID)
	assert.Equal(t, "alice", fetched.User.Username)

	// 5. Query binding by User ID
	userBinding, err := bindingDAO.GetBindingByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, userBinding)
	assert.Equal(t, int64(123456789), userBinding.TelegramUserID)

	// 6. Unbind
	require.NoError(t, bindingDAO.Unbind(ctx, user.ID))

	_, err = bindingDAO.GetBindingByTelegramID(ctx, 123456789)
	assert.ErrorIs(t, err, ErrBindingNotFound)
}

func TestTelegramBindingDAO_ListActiveListPaginatedAndAdminUnbind(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}, &model.TelegramBinding{}))

	ctx := context.Background()
	userDAO := NewUserDAO(db)
	bindingDAO := NewTelegramBindingDAO(db)

	var users []*model.User
	for i, name := range []string{"alice", "bob", "carol"} {
		u := &model.User{Username: name, PasswordHash: "hash", NickName: name, Status: true}
		require.NoError(t, userDAO.Create(ctx, u))
		users = append(users, u)

		binding := model.TelegramBinding{
			UserID:           u.ID,
			TelegramUserID:   int64(1000 + i),
			TelegramUsername: name + "_tg",
			Status:           true,
		}
		require.NoError(t, db.Create(&binding).Error)
		if i == 2 {
			// GORM applies gorm:"default:true" to a false zero-value on
			// Create, so disabling carol's binding has to happen afterward.
			require.NoError(t, db.Model(&binding).UpdateColumn("status", false).Error)
		}
	}

	// ListActive only returns active bindings.
	active, err := bindingDAO.ListActive(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 2, "carol's disabled binding must be excluded")

	// ListPaginated returns everyone (active or not), with User preloaded.
	all, total, err := bindingDAO.ListPaginated(ctx, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, all, 3)
	for _, b := range all {
		require.NotNil(t, b.User, "ListPaginated must preload the owning user")
	}

	// AdminUnbind removes by binding ID, regardless of who owns it.
	require.NoError(t, bindingDAO.AdminUnbind(ctx, all[0].ID))
	_, total, err = bindingDAO.ListPaginated(ctx, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
}

func TestTelegramBindingDAO_ListActiveAdmins(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Role{}, &model.Permission{}, &model.TelegramBinding{}))

	ctx := context.Background()
	userDAO := NewUserDAO(db)
	bindingDAO := NewTelegramBindingDAO(db)

	adminRole := model.Role{Name: "Admin", Code: "ADMIN", Status: true}
	require.NoError(t, db.Create(&adminRole).Error)
	otherRole := model.Role{Name: "Viewer", Code: "VIEWER", Status: true}
	require.NoError(t, db.Create(&otherRole).Error)

	admin := &model.User{Username: "admin1", PasswordHash: "h", NickName: "Admin One", Status: true, Roles: []model.Role{adminRole}}
	require.NoError(t, userDAO.Create(ctx, admin))
	viewer := &model.User{Username: "viewer1", PasswordHash: "h", NickName: "Viewer One", Status: true, Roles: []model.Role{otherRole}}
	require.NoError(t, userDAO.Create(ctx, viewer))
	unbound := &model.User{Username: "nobinding", PasswordHash: "h", Status: true, Roles: []model.Role{adminRole}}
	require.NoError(t, userDAO.Create(ctx, unbound))

	require.NoError(t, db.Create(&model.TelegramBinding{UserID: admin.ID, TelegramUserID: 1, Status: true}).Error)
	require.NoError(t, db.Create(&model.TelegramBinding{UserID: viewer.ID, TelegramUserID: 2, Status: true}).Error)

	admins, err := bindingDAO.ListActiveAdmins(ctx)
	require.NoError(t, err)
	require.Len(t, admins, 1, "only the bound admin-role user should be returned")
	assert.Equal(t, int64(1), admins[0].TelegramUserID)
}
