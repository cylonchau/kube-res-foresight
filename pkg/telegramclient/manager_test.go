package telegramclient

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func newTelegramClientTestDAO(t *testing.T) *rdb.TelegramClientDAO {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientAccount{}, &model.TelegramClientSubscription{}, &model.TelegramClientMessage{}))
	return rdb.NewTelegramClientDAO(db)
}

func TestManagerLocalLifecycleAndMessageHandling(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	account, err := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "reader", PhoneNumber: "+12025550124", APIID: 123, APIHash: "hash"})
	require.NoError(t, err)

	assert.False(t, manager.IsConnected(account.ID))
	assert.ErrorContains(t, manager.StartAccount(account.ID), "not started")
	require.NoError(t, manager.Start(ctx))
	assert.ErrorContains(t, manager.StartAccount(account.ID), "enabled and authorized")
	assert.ErrorContains(t, manager.VerifyCode(ctx, account.ID, "12345", ""), "expired")
	require.NoError(t, manager.Logout(ctx, account.ID))
	assert.ErrorIs(t, func() error { _, err := manager.Subscribe(ctx, account.ID, "channel", nil, false); return err }(), ErrNotConnected)
	manager.StopAccount(account.ID)

	sub := &model.TelegramClientSubscription{AccountID: account.ID, PeerID: 777, PeerType: "channel", Title: "News", Keywords: `["BTC"]`, Enabled: true}
	require.NoError(t, dao.CreateSubscription(ctx, sub))
	entities := tg.Entities{Users: map[int64]*tg.User{9: {ID: 9, FirstName: "Alice"}}}
	require.NoError(t, manager.handleMessage(ctx, account.ID, entities, &tg.Message{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 777}, FromID: &tg.PeerUser{UserID: 9}, Message: "BTC alert", Date: int(time.Now().Unix())}))
	rows, total, err := dao.ListMessages(ctx, &apis.TelegramClientMessageListReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "Alice", rows[0].SenderName)
	require.NoError(t, manager.handleMessage(ctx, account.ID, entities, &tg.Message{ID: 2, PeerID: &tg.PeerChannel{ChannelID: 777}, Message: "ETH only"}))
	require.NoError(t, manager.handleMessage(ctx, account.ID, entities, &tg.MessageEmpty{}))
	assert.Equal(t, "public", normalizeUsername(" https://t.me/@public/ "))
	assert.Equal(t, int64(7), peerID(&tg.PeerChat{ChatID: 7}))
	assert.Zero(t, peerID(&tg.PeerUser{UserID: 7}))
}

func TestTelegramClientCredentialsAndSessionAreEncrypted(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	ctx := context.Background()
	account, err := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "reader", PhoneNumber: "+12025550123", APIID: 12345, APIHash: "plain-api-hash"})
	require.NoError(t, err)
	assert.NotEqual(t, "plain-api-hash", account.APIHashEncrypted)
	decrypted, err := dao.DecryptAPIHash(account)
	require.NoError(t, err)
	assert.Equal(t, "plain-api-hash", decrypted)

	storage := &sessionStorage{dao: dao, accountID: account.ID}
	require.NoError(t, storage.StoreSession(ctx, []byte("sensitive-session")))
	loaded, err := storage.LoadSession(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("sensitive-session"), loaded)
	stored, err := dao.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	assert.NotEqual(t, "sensitive-session", stored.SessionEncrypted)
	assert.True(t, stored.HasSession)
}

func TestMatchesKeywords(t *testing.T) {
	assert.True(t, matchesKeywords(`[]`, "any message"))
	assert.True(t, matchesKeywords(`["Alert","BTC"]`, "system ALERT received"))
	assert.False(t, matchesKeywords(`["Alert","BTC"]`, "ordinary update"))
}
