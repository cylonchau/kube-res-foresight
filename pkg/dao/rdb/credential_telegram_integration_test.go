package rdb

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
	"time"
)

func integrationDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	return db
}

func boolPtr(v bool) *bool       { return &v }
func stringPtr(v string) *string { return &v }
func intPtr(v int) *int          { return &v }

func TestSecretKeyDAODeveloperCredentialLifecycle(t *testing.T) {
	db := integrationDB(t, &model.Permission{}, &model.SecretKey{})
	ctx := context.Background()
	dao := NewSecretKeyDAO(db)
	permission := model.Permission{Name: "Read", Code: "read", Type: "BUTTON", Status: true}
	require.NoError(t, db.Create(&permission).Error)

	created, err := dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "collector", ServerEnabled: true, PermissionIDs: []uint{permission.ID, permission.ID, 0}, Remark: "runtime"})
	require.NoError(t, err)
	assert.NotEmpty(t, created.AccessKey)
	assert.NotEmpty(t, created.AccessSecret)
	assert.Equal(t, []uint{permission.ID}, created.PermissionIDs)
	credential, err := dao.AuthenticateServerCredential(ctx, created.AccessKey, created.AccessSecret)
	require.NoError(t, err)
	assert.Equal(t, created.AccessKey, credential.AccessKey)
	_, err = dao.AuthenticateServerCredential(ctx, created.AccessKey, "wrong")
	assert.ErrorIs(t, err, ErrDeveloperCredentialInvalid)

	status := "true"
	rows, total, err := dao.List(ctx, &apis.SecretKeyListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "collect"}, Status: &status})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].ServerEnabled)

	newName, remark := "collector-updated", "rotated"
	updated, err := dao.Update(ctx, uint64(created.ID), &apis.SecretKeyUpdateReq{Name: &newName, Remark: &remark, RotateSecret: true})
	require.NoError(t, err)
	assert.NotEmpty(t, updated.AccessSecret)
	assert.Equal(t, newName, updated.Name)
	_, err = dao.AuthenticateServerCredential(ctx, created.AccessKey, created.AccessSecret)
	assert.Error(t, err)
	_, err = dao.AuthenticateServerCredential(ctx, updated.AccessKey, updated.AccessSecret)
	require.NoError(t, err)

	_, err = dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "bad", ServerEnabled: true})
	assert.ErrorContains(t, err, "permission_ids")
	_, err = dao.Create(ctx, &apis.SecretKeyCreateReq{Name: "bad-permission", ServerEnabled: true, PermissionIDs: []uint{999}})
	assert.ErrorContains(t, err, "invalid")
	_, err = dao.Update(ctx, uint64(created.ID), &apis.SecretKeyUpdateReq{Name: stringPtr(" ")})
	assert.ErrorContains(t, err, "name")
	require.NoError(t, dao.Delete(ctx, uint64(created.ID)))
	_, err = dao.GetByID(ctx, uint64(created.ID))
	assert.Error(t, err)
}

func TestTelegramClientDAOFullPersistenceFlow(t *testing.T) {
	db := integrationDB(t, &model.TelegramClientAccount{}, &model.TelegramClientSubscription{}, &model.TelegramClientMessage{})
	ctx := context.Background()
	dao := NewTelegramClientDAO(db)

	account, err := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "news", PhoneNumber: "+8613800000000", APIID: 12345, APIHash: "api-hash"})
	require.NoError(t, err)
	assert.True(t, account.HasAPIHash)
	plain, err := dao.DecryptAPIHash(account)
	require.NoError(t, err)
	assert.Equal(t, "api-hash", plain)
	_, err = dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "", APIID: 0, APIHash: "x"})
	assert.Error(t, err)

	require.NoError(t, dao.StoreSession(ctx, account.ID, []byte("session-data")))
	session, err := dao.LoadSession(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, []byte("session-data"), session)
	expires := time.Now().Add(time.Minute)
	require.NoError(t, dao.SaveCodeHash(ctx, account.ID, "code-hash", expires))
	require.NoError(t, dao.MarkAuthorized(ctx, account.ID, 88, "tg_news", "News", "Bot"))
	require.NoError(t, dao.SetConnectionResult(ctx, account.ID, true, ""))

	accounts, total, err := dao.ListAccounts(ctx, &apis.TelegramClientAccountListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "news"}, Enabled: boolPtr(true), Authorized: boolPtr(true)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, accounts, 1)
	assert.True(t, accounts[0].HasSession)
	runnable, err := dao.ListRunnableAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, runnable, 1)

	sub := &model.TelegramClientSubscription{AccountID: account.ID, PeerID: 777, PeerType: "channel", Username: "market_news", Title: "Market News", Keywords: "btc", Enabled: true}
	require.NoError(t, dao.CreateSubscription(ctx, sub))
	foundSub, err := dao.GetEnabledSubscription(ctx, account.ID, 777)
	require.NoError(t, err)
	assert.Equal(t, sub.ID, foundSub.ID)
	subs, total, err := dao.ListSubscriptions(ctx, &apis.TelegramClientSubscriptionListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "Market"}, AccountID: account.ID, Enabled: boolPtr(true)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, subs, 1)
	assert.NotNil(t, subs[0].Account)
	require.NoError(t, dao.UpdateSubscription(ctx, sub.ID, "btc,eth", boolPtr(false)))

	messageAt := time.Now().UTC().Truncate(time.Second)
	message := &model.TelegramClientMessage{AccountID: account.ID, SubscriptionID: sub.ID, PeerID: 777, TelegramMessageID: 1, SenderID: 9, SenderName: "analyst", Text: "BTC update", MessageAt: messageAt}
	require.NoError(t, dao.SaveMessage(ctx, message))
	require.NoError(t, dao.SaveMessage(ctx, &model.TelegramClientMessage{AccountID: account.ID, SubscriptionID: sub.ID, PeerID: 777, TelegramMessageID: 1, Text: "duplicate", MessageAt: messageAt}))
	messages, total, err := dao.ListMessages(ctx, &apis.TelegramClientMessageListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "analyst"}, AccountID: account.ID, SubscriptionID: sub.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, messages, 1)
	assert.Equal(t, "analyst", messages[0].SenderName)
	assert.Equal(t, model.TelegramMsgPending, messages[0].Status, "a received message waits in the cache")

	newName, newPhone, newHash, newID := "alerts", "+8613900000000", "new-hash", 54321
	updated, err := dao.UpdateAccount(ctx, account.ID, &apis.TelegramClientAccountUpdateReq{Name: &newName, PhoneNumber: &newPhone, APIID: &newID, APIHash: &newHash, Enabled: boolPtr(false)})
	require.NoError(t, err)
	assert.Equal(t, newName, updated.Name)
	assert.False(t, updated.Authorized)
	_, err = dao.UpdateAccount(ctx, account.ID, &apis.TelegramClientAccountUpdateReq{APIID: intPtr(0)})
	assert.Error(t, err)

	require.NoError(t, dao.MarkUnauthorized(ctx, account.ID))
	require.NoError(t, dao.DeleteSubscription(ctx, sub.ID))
	require.NoError(t, dao.DeleteAccount(ctx, account.ID))
	_, err = dao.GetAccount(ctx, account.ID)
	assert.Error(t, err)
}
