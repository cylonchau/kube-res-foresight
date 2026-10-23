package telegramclient

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestChannelReferencesAcceptUsernamesAndLinksButNotInvites(t *testing.T) {
	for input, want := range map[string]string{
		"newsfeed":                         "newsfeed",
		"@newsfeed":                        "newsfeed",
		"  https://t.me/newsfeed  ":        "newsfeed",
		"http://t.me/newsfeed/":            "newsfeed",
		"t.me/newsfeed/1234":               "newsfeed",
		"https://telegram.me/newsfeed?x=1": "newsfeed",
		"https://t.me/Some_Channel_9":      "Some_Channel_9",
	} {
		got, err := ParseChannelRef(input)
		require.NoError(t, err, input)
		assert.Equal(t, want, got, input)
	}
	for _, input := range []string{"", "   ", "@", "https://t.me/+AbCdEf", "https://t.me/joinchat/AAAA", "https://t.me/c/123/4", "has space", "bad-dash", "abcd", "this_name_is_way_too_long_for_telegram", "emoji😀"} {
		_, err := ParseChannelRef(input)
		assert.ErrorIs(t, err, ErrChannelRef, input)
	}
}

func collectEvents(manager *Manager) (func() []message.Event, *sync.Mutex) {
	var mu sync.Mutex
	var events []message.Event
	manager.SetSink(message.SinkFunc(func(e message.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }))
	return func() []message.Event { mu.Lock(); defer mu.Unlock(); return append([]message.Event(nil), events...) }, &mu
}

func cachedMessages(t *testing.T, dao *rdb.TelegramClientDAO) []model.TelegramClientMessage {
	t.Helper()
	req := &apis.TelegramClientMessageListReq{}
	req.Page, req.PageSize = 1, 50
	rows, _, err := dao.ListMessages(context.Background(), req)
	require.NoError(t, err)
	return rows
}

func TestSubscribedChannelMessagesAreCachedAndReportedAsStandardMessages(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	sub := &model.TelegramClientSubscription{AccountID: 3, PeerID: 777, PeerType: "channel", Username: "news", Title: "News", Enabled: true}
	require.NoError(t, dao.CreateSubscription(ctx, sub))
	events, _ := collectEvents(manager)
	at := time.Unix(1_700_000_000, 0)
	entities := tg.Entities{Users: map[int64]*tg.User{9: {ID: 9, FirstName: "Alice", LastName: "Liddell"}}}

	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 41, PeerID: &tg.PeerChannel{ChannelID: 777}, FromID: &tg.PeerUser{UserID: 9}, Message: "BTC listed", Date: int(at.Unix()), Media: &tg.MessageMediaEmpty{}}))
	// Redelivered by Telegram: cached once, and the sink is told again (forwarding is idempotent).
	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 41, PeerID: &tg.PeerChannel{ChannelID: 777}, Message: "BTC listed", Date: int(at.Unix())}))

	rows := cachedMessages(t, dao)
	require.Len(t, rows, 1)
	assert.Equal(t, model.TelegramMsgPending, rows[0].Status)
	assert.Equal(t, model.TelegramSourceChannel, rows[0].SourceType)
	assert.Equal(t, "news", rows[0].Source)
	assert.Equal(t, sub.ID, rows[0].SubscriptionID)

	got := events()
	require.NotEmpty(t, got)
	assert.Equal(t, message.EventChannelMessage, got[0].Type)
	m := got[0].Message
	assert.Equal(t, "telegram", m.Platform)
	assert.Equal(t, "3", m.Account)
	assert.Equal(t, "news", m.Source)
	assert.Equal(t, message.SourceChannel, m.SourceType)
	assert.Equal(t, "41", m.MessageID)
	assert.Equal(t, "Alice Liddell", m.Sender)
	assert.Equal(t, "BTC listed", m.Text)
	assert.True(t, m.Timestamp.Equal(at))
	assert.Equal(t, "media", m.Metadata["media_type"], "Telegram-only fields stay in metadata")
	assert.Equal(t, "777", m.Metadata["peer_id"])
	assert.Equal(t, "9", m.Metadata["sender_id"])
}

func TestPrivateSystemAndUnsubscribedMessagesAreDropped(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	events, _ := collectEvents(manager)
	entities := tg.Entities{Users: map[int64]*tg.User{
		55:     {ID: 55, FirstName: "Bob", Username: "bob"},
		777000: {ID: 777000, FirstName: "Telegram"},
	}}

	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 55}, FromID: &tg.PeerUser{UserID: 55}, Message: "hey", Date: 1}))
	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 777000}, Message: "Login code: 12345", Date: 2}))
	// No subscription, our own outgoing message and non-message updates are ignored.
	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 3, PeerID: &tg.PeerChannel{ChannelID: 999}, Message: "not subscribed", Date: 3}))
	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 4, Out: true, PeerID: &tg.PeerUser{UserID: 55}, Message: "mine", Date: 4}))
	require.NoError(t, manager.handleMessage(ctx, 3, entities, &tg.Message{ID: 5, PeerID: &tg.PeerChat{ChatID: 0}, Message: "no peer", Date: 5}))

	assert.Empty(t, cachedMessages(t, dao), "only subscribed channels and groups are cached")
	assert.Empty(t, events())
}

func TestGroupMessagesAndKeywordFilteredSubscriptions(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	require.NoError(t, dao.CreateSubscription(ctx, &model.TelegramClientSubscription{AccountID: 3, PeerID: 5, PeerType: "supergroup", Title: "Traders", Keywords: `["BTC"]`, Enabled: true}))

	require.NoError(t, manager.handleMessage(ctx, 3, tg.Entities{}, &tg.Message{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 5}, Message: "ETH only", Date: 1}))
	require.NoError(t, manager.handleMessage(ctx, 3, tg.Entities{}, &tg.Message{ID: 2, PeerID: &tg.PeerChannel{ChannelID: 5}, Message: "BTC moon", Date: 2}))
	rows := cachedMessages(t, dao)
	require.Len(t, rows, 1)
	assert.Equal(t, model.TelegramSourceGroup, rows[0].SourceType)
	assert.Equal(t, "Traders", rows[0].Source, "no username: the title names the source")
}

func TestStandardMessageFallsBackWhenSourceAndSenderAreUnknown(t *testing.T) {
	m := StandardMessage(model.TelegramClientMessage{ID: 9, AccountID: 1, PeerID: 99, TelegramMessageID: 7, SenderID: 12, SourceType: model.TelegramSourceGroup, Text: "hi", MessageAt: time.Unix(1, 0)})
	assert.Equal(t, "99", m.Source, "no source name: the peer id identifies it")
	assert.Equal(t, message.SourceGroup, m.SourceType)
	assert.Equal(t, "12", m.Sender, "no name: the sender id")
	assert.Equal(t, "9", m.Metadata["cache_id"])
}

func TestManagerBindsAndStartsOnlyItsOwnAccounts(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	manager.SetAgentID("agent-a")
	assert.Equal(t, "agent-a", manager.agent())
	ctx := context.Background()
	mine, _ := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "mine", PhoneNumber: "+1", APIID: 1, APIHash: "h"})
	theirs, _ := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "theirs", PhoneNumber: "+2", APIID: 1, APIHash: "h"})
	require.NoError(t, dao.BindAgent(ctx, mine.ID, "agent-a"))
	require.NoError(t, dao.BindAgent(ctx, theirs.ID, "agent-b"))
	require.NoError(t, dao.SetConnectionResult(ctx, theirs.ID, true, ""))

	require.NoError(t, manager.Start(ctx))
	got, _ := dao.GetAccount(ctx, theirs.ID)
	assert.Equal(t, model.TelegramClientConnected, got.ConnectionStatus, "another agent's account state is not reset")
}

func TestUnsubscribeNeedsAConnectedAccountAndKnownSubscription(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	assert.Error(t, manager.Unsubscribe(ctx, 404))

	sub := &model.TelegramClientSubscription{AccountID: 3, PeerID: 1, PeerType: "channel", Enabled: true}
	require.NoError(t, dao.CreateSubscription(ctx, sub))
	assert.ErrorIs(t, manager.Unsubscribe(ctx, sub.ID), ErrNotConnected)

	assert.Empty(t, manager.ConnectedIDs())
	manager.clients[3] = &runningClient{}
	assert.Equal(t, []int64{3}, manager.ConnectedIDs())
}

func TestSubscribingRejectsInvitesBeforeTouchingTelegram(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: map[int64]*runningClient{3: {}}}
	_, err := manager.Subscribe(context.Background(), 3, "https://t.me/+secretinvite", nil, true)
	assert.ErrorIs(t, err, ErrChannelRef)
}

func TestStartingTheManagerResetsStaleConnectionStates(t *testing.T) {
	dao := newTelegramClientTestDAO(t)
	manager := &Manager{dao: dao, clients: make(map[int64]*runningClient)}
	ctx := context.Background()
	account, err := dao.CreateAccount(ctx, &apis.TelegramClientAccountCreateReq{Name: "a", PhoneNumber: "+1", APIID: 1, APIHash: "h"})
	require.NoError(t, err)
	require.NoError(t, dao.SetConnectionResult(ctx, account.ID, true, ""))

	require.NoError(t, manager.Start(ctx))
	got, err := dao.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TelegramClientDisconnected, got.ConnectionStatus)
}
