package telegramclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"gorm.io/gorm"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotConnected     = errors.New("telegram client account is not connected")
	ErrPasswordRequired = errors.New("telegram two-step verification password is required")
)

type sessionStorage struct {
	dao       *rdb.TelegramClientDAO
	accountID int64
}

func (s *sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	return s.dao.LoadSession(ctx, s.accountID)
}
func (s *sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	return s.dao.StoreSession(ctx, s.accountID, data)
}

type runningClient struct {
	client *telegram.Client
	cancel context.CancelFunc
	done   chan struct{}
}

// Manager owns long-lived MTProto clients. Authentication requests use short
// clients backed by the same encrypted database session; after login the
// long-lived client is started and receives updates until shutdown/logout.
type Manager struct {
	dao *rdb.TelegramClientDAO

	mu      sync.RWMutex
	clients map[int64]*runningClient
	rootCtx context.Context
	sink    message.Sink
	agentID string

	// catching holds, per subscription being caught up, the newest id live updates delivered
	// meanwhile; the read position only moves once the catch-up has finished, so a stop in the
	// middle of it resumes from where the older messages were still missing.
	catching map[int64]int

	// policy reads the catch-up spacing; nil uses the default. random picks the jitter (a test replaces it).
	policy func(context.Context) CatchUpPolicy
	random func(n int64) int64
}

func NewManager(db *gorm.DB) *Manager {
	config := rdb.NewSysConfigDAO(db)
	return &Manager{dao: rdb.NewTelegramClientDAO(db), clients: make(map[int64]*runningClient), catching: map[int64]int{},
		policy: func(ctx context.Context) CatchUpPolicy { return LoadCatchUpPolicy(ctx, config) }}
}

func (m *Manager) DAO() *rdb.TelegramClientDAO { return m.dao }

// SetSink sets where received channel messages are published as standard
// events. Nothing downstream ever sees a gotd type.
func (m *Manager) SetSink(sink message.Sink) {
	m.mu.Lock()
	m.sink = sink
	m.mu.Unlock()
}

// SetAgentID names the TG Agent this manager runs for. Accounts bound to another
// agent are left alone, and the accounts it connects are bound to it.
func (m *Manager) SetAgentID(agentID string) {
	m.mu.Lock()
	m.agentID = agentID
	m.mu.Unlock()
}

func (m *Manager) agent() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agentID
}

// ConnectedIDs lists the accounts this process currently holds a session for.
func (m *Manager) ConnectedIDs() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]int64, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()
	// This process is the only one holding sessions, so whatever the database
	// says about connections from a previous run is stale.
	if err := m.dao.ResetConnectionStatesFor(ctx, m.agent()); err != nil {
		return err
	}
	accounts, err := m.dao.ListRunnableAccountsFor(ctx, m.agent())
	if err != nil {
		return err
	}
	for i := range accounts {
		accountID := accounts[i].ID
		go func() {
			if err := m.StartAccount(accountID); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("Failed to start Telegram client account", logger.Int64("account_id", accountID), logger.Err(err))
			}
		}()
	}
	return nil
}

func (m *Manager) IsConnected(accountID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[accountID]
	return ok
}

func (m *Manager) StartAccount(accountID int64) error {
	m.StopAccount(accountID)
	m.mu.RLock()
	rootCtx := m.rootCtx
	m.mu.RUnlock()
	if rootCtx == nil {
		return errors.New("telegram client manager is not started")
	}
	account, err := m.dao.GetAccount(rootCtx, accountID)
	if err != nil {
		return err
	}
	if !account.Enabled || !account.Authorized || !account.HasSession {
		return errors.New("telegram client account is not enabled and authorized")
	}
	apiHash, err := m.dao.DecryptAPIHash(account)
	if err != nil {
		return fmt.Errorf("decrypt api hash: %w", err)
	}

	dispatcher := tg.NewUpdateDispatcher()
	gaps := updates.New(updates.Config{Handler: dispatcher})
	client := telegram.NewClient(account.APIID, apiHash, telegram.Options{SessionStorage: &sessionStorage{dao: m.dao, accountID: accountID}, UpdateHandler: gaps})
	dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
		return m.handleMessage(ctx, accountID, entities, update.Message)
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
		return m.handleMessage(ctx, accountID, entities, update.Message)
	})

	ctx, cancel := context.WithCancel(rootCtx)
	running := &runningClient{client: client, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.clients[accountID] = running
	m.mu.Unlock()
	ready := make(chan error, 1)
	go func() {
		defer close(running.done)
		err := client.Run(ctx, func(ctx context.Context) error {
			status, err := client.Auth().Status(ctx)
			if err != nil {
				ready <- err
				return err
			}
			if !status.Authorized {
				err = errors.New("telegram session is no longer authorized")
				ready <- err
				return err
			}
			self, err := client.Self(ctx)
			if err != nil {
				ready <- err
				return err
			}
			_ = m.dao.SetConnectionResult(context.Background(), accountID, true, "")
			if agent := m.agent(); agent != "" {
				_ = m.dao.BindAgent(context.Background(), accountID, agent)
			}
			ready <- nil
			// What was sent while this agent was not listening is fetched from the read positions.
			go m.catchUp(ctx, accountID, client.API())
			return gaps.Run(ctx, client.API(), self.ID, updates.AuthOptions{})
		})
		m.mu.Lock()
		if current := m.clients[accountID]; current == running {
			delete(m.clients, accountID)
		}
		m.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = m.dao.SetConnectionResult(context.Background(), accountID, false, err.Error())
			logger.Warn("Telegram client account stopped", logger.Int64("account_id", accountID), logger.Err(err))
		} else {
			_ = m.dao.SetConnectionResult(context.Background(), accountID, false, "")
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			return err
		}
		return nil
	case <-time.After(20 * time.Second):
		cancel()
		return errors.New("telegram client connection timed out")
	case <-rootCtx.Done():
		cancel()
		return rootCtx.Err()
	}
}

func (m *Manager) StopAccount(accountID int64) {
	m.mu.Lock()
	running := m.clients[accountID]
	delete(m.clients, accountID)
	m.mu.Unlock()
	if running != nil {
		running.cancel()
		select {
		case <-running.done:
		case <-time.After(5 * time.Second):
			logger.Warn("Timed out waiting for Telegram client account to stop", logger.Int64("account_id", accountID))
		}
	}
}

func (m *Manager) withTransientClient(ctx context.Context, account *model.TelegramClientAccount, fn func(context.Context, *telegram.Client) error) error {
	apiHash, err := m.dao.DecryptAPIHash(account)
	if err != nil {
		return fmt.Errorf("decrypt api hash: %w", err)
	}
	client := telegram.NewClient(account.APIID, apiHash, telegram.Options{SessionStorage: &sessionStorage{dao: m.dao, accountID: account.ID}, NoUpdates: true})
	return client.Run(ctx, func(ctx context.Context) error { return fn(ctx, client) })
}

func (m *Manager) SendCode(ctx context.Context, accountID int64) error {
	account, err := m.dao.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	return m.withTransientClient(ctx, account, func(ctx context.Context, client *telegram.Client) error {
		sent, err := client.Auth().SendCode(ctx, account.PhoneNumber, auth.SendCodeOptions{})
		if err != nil {
			return fmt.Errorf("send Telegram login code: %w", err)
		}
		typed, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("unexpected Telegram sent-code response %T", sent)
		}
		return m.dao.SaveCodeHash(ctx, accountID, typed.PhoneCodeHash, time.Now().Add(10*time.Minute))
	})
}

func (m *Manager) VerifyCode(ctx context.Context, accountID int64, code, password string) error {
	account, err := m.dao.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account.PhoneCodeHash == "" || account.CodeExpiresAt == nil || time.Now().After(*account.CodeExpiresAt) {
		return errors.New("login code has expired; send a new code")
	}
	err = m.withTransientClient(ctx, account, func(ctx context.Context, client *telegram.Client) error {
		_, signInErr := client.Auth().SignIn(ctx, account.PhoneNumber, strings.TrimSpace(code), account.PhoneCodeHash)
		if errors.Is(signInErr, auth.ErrPasswordAuthNeeded) {
			if password == "" {
				return ErrPasswordRequired
			}
			if _, signInErr = client.Auth().Password(ctx, password); signInErr != nil {
				return fmt.Errorf("Telegram two-step verification failed: %w", signInErr)
			}
		} else if signInErr != nil {
			return fmt.Errorf("Telegram sign-in failed: %w", signInErr)
		}
		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("load Telegram account profile: %w", err)
		}
		return m.dao.MarkAuthorized(ctx, accountID, self.ID, self.Username, self.FirstName, self.LastName)
	})
	if err != nil {
		return err
	}
	return m.StartAccount(accountID)
}

func (m *Manager) Logout(ctx context.Context, accountID int64) error {
	m.StopAccount(accountID)
	account, err := m.dao.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account.HasSession {
		if err := m.withTransientClient(ctx, account, func(ctx context.Context, client *telegram.Client) error {
			_, err := client.API().AuthLogOut(ctx)
			return err
		}); err != nil {
			return err
		}
	}
	return m.dao.MarkUnauthorized(ctx, accountID)
}

// ErrChannelRef means the input is not a public channel or group reference.
var ErrChannelRef = errors.New("enter a public channel username or a t.me link (invite links are not supported)")

// normalizeUsername accepts @name, name, t.me/name and t.me/name/123 and
// returns the bare username.
func normalizeUsername(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"https://", "http://"} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.TrimPrefix(value, "t.me/")
	value = strings.TrimPrefix(value, "telegram.me/")
	value = strings.TrimPrefix(strings.Trim(value, "/"), "@")
	if i := strings.IndexAny(value, "/?#"); i >= 0 {
		value = value[:i]
	}
	return value
}

// ParseChannelRef validates a channel reference and returns its username.
func ParseChannelRef(input string) (string, error) {
	username := normalizeUsername(input)
	// Public usernames are 5-32 characters; that also rules out the one-letter
	// "c" of private message links (t.me/c/<id>/<post>).
	if len(username) < 5 || len(username) > 32 || strings.HasPrefix(username, "+") || strings.EqualFold(username, "joinchat") {
		return "", ErrChannelRef
	}
	for _, r := range username {
		if !(r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return "", ErrChannelRef
		}
	}
	return username, nil
}

func (m *Manager) Subscribe(ctx context.Context, accountID int64, username string, keywords []string, join bool) (*model.TelegramClientSubscription, error) {
	m.mu.RLock()
	running := m.clients[accountID]
	m.mu.RUnlock()
	if running == nil {
		return nil, ErrNotConnected
	}
	username, err := ParseChannelRef(username)
	if err != nil {
		return nil, err
	}
	resolved, err := running.client.API().ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return nil, fmt.Errorf("resolve Telegram username: %w", err)
	}
	row := &model.TelegramClientSubscription{AccountID: accountID, Username: username, Enabled: true, State: model.TelegramSubPending}
	for _, chat := range resolved.Chats {
		switch value := chat.(type) {
		case *tg.Channel:
			row.PeerID, row.AccessHash, row.Title = value.ID, value.AccessHash, value.Title
			if value.Megagroup {
				row.PeerType = "supergroup"
			} else {
				row.PeerType = "channel"
			}
			if join && value.Left {
				if _, err := running.client.API().ChannelsJoinChannel(ctx, value.AsInput()); err != nil {
					return nil, fmt.Errorf("join Telegram channel: %w", err)
				}
			}
			if join || !value.Left {
				row.State = model.TelegramSubJoined
			}
		case *tg.Chat:
			row.PeerID, row.Title, row.PeerType = value.ID, value.Title, "group"
		}
		if row.PeerID != 0 {
			break
		}
	}
	if row.PeerID == 0 {
		return nil, errors.New("resolved Telegram peer is not a group or channel")
	}
	cleanKeywords := make([]string, 0, len(keywords))
	for _, value := range keywords {
		if value = strings.TrimSpace(value); value != "" {
			cleanKeywords = append(cleanKeywords, value)
		}
	}
	raw, _ := json.Marshal(cleanKeywords)
	row.Keywords = string(raw)
	// The read position starts at the chat's newest message: a subscription forwards what is sent
	// from now on, not the chat's history.
	if peer := inputPeerOf(*row); peer != nil {
		row.LastMessageID = latestMessageID(ctx, running.client.API(), peer)
	}
	if err := m.dao.CreateSubscription(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// Unsubscribe leaves the channel with the subscription's account and marks the
// subscription left (and disabled). Chats that are not channels cannot be left
// with this call: the subscription is still switched off.
func (m *Manager) Unsubscribe(ctx context.Context, subscriptionID int64) error {
	sub, err := m.dao.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	m.mu.RLock()
	running := m.clients[sub.AccountID]
	m.mu.RUnlock()
	if running == nil {
		return ErrNotConnected
	}
	if sub.PeerType == "channel" || sub.PeerType == "supergroup" {
		channel := &tg.InputChannel{ChannelID: sub.PeerID, AccessHash: sub.AccessHash}
		if _, err := running.client.API().ChannelsLeaveChannel(ctx, channel); err != nil {
			_ = m.dao.SetSubscriptionState(ctx, subscriptionID, model.TelegramSubError, err.Error())
			return fmt.Errorf("leave Telegram channel: %w", err)
		}
	}
	return m.dao.SetSubscriptionState(ctx, subscriptionID, model.TelegramSubLeft, "")
}

func peerID(peer tg.PeerClass) int64 {
	switch value := peer.(type) {
	case *tg.PeerChannel:
		return value.ChannelID
	case *tg.PeerChat:
		return value.ChatID
	default:
		return 0
	}
}
func sender(entities tg.Entities, peer tg.PeerClass) (int64, string) {
	switch value := peer.(type) {
	case *tg.PeerUser:
		if user := entities.Users[value.UserID]; user != nil {
			name := strings.TrimSpace(user.FirstName + " " + user.LastName)
			if name == "" {
				name = user.Username
			}
			return value.UserID, name
		}
		return value.UserID, ""
	case *tg.PeerChannel:
		if channel := entities.Channels[value.ChannelID]; channel != nil {
			return value.ChannelID, channel.Title
		}
		return value.ChannelID, ""
	case *tg.PeerChat:
		if chat := entities.Chats[value.ChatID]; chat != nil {
			return value.ChatID, chat.Title
		}
		return value.ChatID, ""
	default:
		return 0, ""
	}
}

func matchesKeywords(raw, text string) bool {
	var keywords []string
	if json.Unmarshal([]byte(raw), &keywords) != nil || len(keywords) == 0 {
		return true
	}
	text = strings.ToLower(text)
	for _, keyword := range keywords {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(keyword))) {
			return true
		}
	}
	return false
}

// handleMessage caches one received message. Subscribed channels and groups,
// private chats and Telegram service messages are accepted; anything else
// (an unsubscribed group, our own outgoing message) is ignored.
func (m *Manager) handleMessage(ctx context.Context, accountID int64, entities tg.Entities, raw tg.MessageClass) error {
	return m.handle(ctx, accountID, entities, raw, false, time.Time{})
}

// handle takes one message in. fromCatchUp marks a message read from the history while catching
// up after a stop, as opposed to one delivered live.
// notBefore, when set, is the earliest time the message may be forwarded (the spacing of a backlog).
func (m *Manager) handle(ctx context.Context, accountID int64, entities tg.Entities, raw tg.MessageClass, fromCatchUp bool, notBefore time.Time) error {
	value, ok := raw.(*tg.Message)
	if !ok || value.Out {
		return nil
	}
	row := &model.TelegramClientMessage{
		AccountID: accountID, TelegramMessageID: value.ID, Text: value.Message, MediaType: "text", Status: model.TelegramMsgPending,
	}
	if value.Media != nil {
		row.MediaType = "media"
	}
	if !notBefore.IsZero() {
		row.NextAttemptAt = &notBefore
	}
	row.MessageAt = time.Unix(int64(value.Date), 0)
	if value.Date == 0 {
		row.MessageAt = time.Now()
	}
	row.SenderID, row.SenderName = sender(entities, value.FromID)

	// Only subscribed channels / groups are cached; private chats, service messages
	// and every other chat are dropped.
	if _, private := value.PeerID.(*tg.PeerUser); private {
		return nil
	}
	{
		id := peerID(value.PeerID)
		if id == 0 {
			return nil
		}
		subscription, err := m.dao.GetEnabledSubscription(ctx, accountID, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		_ = m.dao.ObserveSubscriptionLatest(ctx, subscription.ID, value.ID)
		if !matchesKeywords(subscription.Keywords, value.Message) {
			// Looked at and dropped: it counts as read.
			return m.advance(ctx, subscription.ID, value.ID, fromCatchUp)
		}
		row.PeerID, row.SubscriptionID, row.SourceType = id, subscription.ID, model.TelegramSourceChannel
		if subscription.PeerType != "channel" {
			row.SourceType = model.TelegramSourceGroup
		}
		row.Source = subscription.Username
		if row.Source == "" {
			row.Source = subscription.Title
		}
	}
	if err := m.dao.SaveMessage(ctx, row); err != nil {
		return err
	}
	// The position moves only after the message is safely cached; from there the forwarder
	// delivers it with retries, so a restart loses neither this message nor a later one.
	if row.SubscriptionID > 0 {
		if err := m.advance(ctx, row.SubscriptionID, value.ID, fromCatchUp); err != nil {
			return err
		}
	}
	m.mu.RLock()
	sink := m.sink
	m.mu.RUnlock()
	if sink != nil {
		sink.Handle(message.Event{Type: eventTypeOf(row.SourceType), Message: StandardMessage(*row)})
	}
	return nil
}

// telegramServiceUserID is the account Telegram sends service messages from
// (login codes, security notices).
const telegramServiceUserID int64 = 777000

func eventTypeOf(sourceType string) string {
	switch sourceType {
	case model.TelegramSourcePrivate:
		return message.EventPrivateMessage
	case model.TelegramSourceSystem:
		return message.EventSystemMessage
	}
	return message.EventChannelMessage
}

// StandardMessage converts a cached message into the platform-neutral message.
// Telegram-only fields stay in Metadata.
func StandardMessage(row model.TelegramClientMessage) message.Message {
	source := row.Source
	if source == "" {
		source = fmt.Sprint(row.PeerID)
	}
	sender := row.SenderName
	if sender == "" {
		sender = fmt.Sprint(row.SenderID)
	}
	return message.Message{
		Platform:   "telegram",
		Account:    fmt.Sprint(row.AccountID),
		Source:     source,
		SourceType: row.SourceType,
		MessageID:  fmt.Sprint(row.TelegramMessageID),
		Sender:     sender,
		Text:       row.Text,
		Timestamp:  row.MessageAt,
		Metadata: map[string]string{
			"subscription_id": fmt.Sprint(row.SubscriptionID),
			"peer_id":         fmt.Sprint(row.PeerID),
			"sender_id":       fmt.Sprint(row.SenderID),
			"media_type":      row.MediaType,
			"cache_id":        fmt.Sprint(row.ID),
		},
	}
}

// advance moves a subscription's read position to a message id. While the subscription is being
// caught up, live messages are only noted: the catch-up moves the position when it is done.
func (m *Manager) advance(ctx context.Context, subscriptionID int64, messageID int, fromCatchUp bool) error {
	if !fromCatchUp {
		m.mu.Lock()
		if seen, busy := m.catching[subscriptionID]; busy {
			if messageID > seen {
				m.catching[subscriptionID] = messageID
			}
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
	}
	return m.dao.AdvanceSubscriptionPosition(ctx, subscriptionID, messageID)
}

// inputPeerOf is how the account addresses a subscribed channel or group.
func inputPeerOf(sub model.TelegramClientSubscription) tg.InputPeerClass {
	switch sub.PeerType {
	case "channel", "supergroup":
		return &tg.InputPeerChannel{ChannelID: sub.PeerID, AccessHash: sub.AccessHash}
	case "group":
		return &tg.InputPeerChat{ChatID: sub.PeerID}
	}
	return nil
}

// historyPage is how many messages one history request asks for, and maxCatchUpPages bounds how far
// back one catch-up reads (anything older than that is reported as skipped).
const (
	historyPage     = 100
	maxCatchUpPages = 50
)

// latestMessageID is the id of the newest message of a chat; 0 when it has none or cannot be read.
func latestMessageID(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) int {
	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1})
	if err != nil {
		return 0
	}
	if mod, ok := res.AsModified(); ok {
		for _, raw := range mod.GetMessages() {
			if msg, ok := raw.(*tg.Message); ok {
				return msg.ID
			}
		}
	}
	return 0
}

// catchUp reads what every subscription of an account received after its read position, so a
// restart continues where the last run stopped instead of listening only from now on.
func (m *Manager) catchUp(ctx context.Context, accountID int64, api *tg.Client) {
	subs, err := m.dao.ListJoinedSubscriptions(ctx, accountID)
	if err != nil {
		logger.Warn("Telegram catch-up could not list subscriptions", logger.Int64("account_id", accountID), logger.Err(err))
		return
	}
	for _, sub := range subs {
		if ctx.Err() != nil {
			return
		}
		if err := m.catchUpOne(ctx, accountID, api, sub); err != nil {
			logger.Warn("Telegram catch-up stopped", logger.Int64("subscription_id", sub.ID), logger.Err(err))
		}
	}
}

func (m *Manager) catchUpOne(ctx context.Context, accountID int64, api *tg.Client, sub model.TelegramClientSubscription) error {
	peer := inputPeerOf(sub)
	if peer == nil {
		return nil
	}
	position := sub.LastMessageID
	if position == 0 {
		// A subscription from before positions existed: continue after what is already cached,
		// else start from now (the chat's history is not replayed).
		cached, err := m.dao.MaxCachedMessageID(ctx, accountID, sub.PeerID)
		if err != nil {
			return err
		}
		position = cached
		if position == 0 {
			position = latestMessageID(ctx, api, peer)
			_ = m.dao.ObserveSubscriptionLatest(ctx, sub.ID, position)
		}
		if position == 0 {
			return nil
		}
		if err := m.dao.AdvanceSubscriptionPosition(ctx, sub.ID, position); err != nil {
			return err
		}
		if cached == 0 {
			return nil
		}
	}

	m.mu.Lock()
	if m.catching == nil {
		m.catching = map[int64]int{}
	}
	m.catching[sub.ID] = 0
	m.mu.Unlock()
	finished := false
	defer func() {
		m.mu.Lock()
		seen := m.catching[sub.ID]
		delete(m.catching, sub.ID)
		m.mu.Unlock()
		if finished && seen > 0 {
			_ = m.dao.AdvanceSubscriptionPosition(context.Background(), sub.ID, seen)
		}
	}()

	if latest := latestMessageID(ctx, api, peer); latest > 0 {
		_ = m.dao.ObserveSubscriptionLatest(ctx, sub.ID, latest)
	}
	var found []tg.MessageClass
	entities := tg.Entities{Users: map[int64]*tg.User{}, Chats: map[int64]*tg.Chat{}, Channels: map[int64]*tg.Channel{}}
	offset, truncated := 0, false
	for page := 0; ; page++ {
		if page >= maxCatchUpPages {
			truncated = true
			break
		}
		res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: offset, Limit: historyPage, MinID: position})
		if err != nil {
			return fmt.Errorf("read Telegram history: %w", err)
		}
		mod, ok := res.AsModified()
		if !ok {
			break
		}
		for _, u := range mod.GetUsers() {
			if user, ok := u.(*tg.User); ok {
				entities.Users[user.ID] = user
			}
		}
		for _, c := range mod.GetChats() {
			switch chat := c.(type) {
			case *tg.Chat:
				entities.Chats[chat.ID] = chat
			case *tg.Channel:
				entities.Channels[chat.ID] = chat
			}
		}
		messages := mod.GetMessages()
		if len(messages) == 0 {
			break
		}
		found = append(found, messages...)
		lowest := messages[len(messages)-1].GetID()
		if len(messages) < historyPage || lowest <= position+1 {
			break
		}
		offset = lowest
	}
	if truncated {
		logger.Warn("Telegram catch-up read only the newest messages", logger.Int64("subscription_id", sub.ID), logger.Int("read", len(found)))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].GetID() < found[j].GetID() })
	backlog := found[:0:0]
	for _, raw := range found {
		if raw.GetID() > position {
			backlog = append(backlog, raw)
		}
	}
	if len(backlog) > 0 {
		_ = m.dao.ObserveSubscriptionLatest(ctx, sub.ID, backlog[len(backlog)-1].GetID())
	}
	// A backlog is not fired all at once: from the configured size on, the messages go out one by one,
	// each after the base delay plus a random jitter.
	policy := DefaultCatchUpPolicy
	if m.policy != nil {
		policy = m.policy(ctx)
	}
	random := m.random
	if random == nil {
		random = defaultRandom
	}
	offsets := policy.Offsets(len(backlog), random)
	started := time.Now()
	if len(backlog) >= 2 && offsets[len(offsets)-1] > 0 {
		logger.Info("Telegram backlog is forwarded with spacing", logger.Int64("subscription_id", sub.ID),
			logger.Int("messages", len(backlog)), logger.String("takes", offsets[len(offsets)-1].String()))
	}
	for i, raw := range backlog {
		var notBefore time.Time
		if offsets[i] > 0 {
			notBefore = started.Add(offsets[i])
		}
		// In order, oldest first: a stop in the middle resumes after the last message taken in.
		if err := m.handle(ctx, accountID, entities, raw, true, notBefore); err != nil {
			return err
		}
	}
	finished = true
	return nil
}
