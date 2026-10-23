package telegramagent

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/telegramclient"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"sync"
	"testing"
	"time"
)

const botToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	return db
}

type fakeBot struct {
	mu       sync.Mutex
	username string
	id       int64
	started  int
	synced   [][]telegram.CommandSpec
	syncErr  error
	stopped  bool
	ctx      context.Context
}

func (b *fakeBot) Identity() (string, int64) { return b.username, b.id }
func (b *fakeBot) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started++
	b.ctx = ctx
}
func (b *fakeBot) SyncCommands(specs []telegram.CommandSpec) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.syncErr != nil {
		return b.syncErr
	}
	b.synced = append(b.synced, specs)
	return nil
}
func (b *fakeBot) syncCount() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.synced) }

type fakeClients struct {
	mu        sync.Mutex
	connected map[int64]bool
	calls     []string
	startErr  error
	verifyErr error
	sendErr   error
	sink      message.Sink
	agent     string
}

func newFakeClients() *fakeClients { return &fakeClients{connected: map[int64]bool{}} }

func (f *fakeClients) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}
func (f *fakeClients) Start(context.Context) error {
	f.record("start")
	return nil
}
func (f *fakeClients) StartAccount(id int64) error {
	f.record(fmt.Sprintf("connect:%d", id))
	if f.startErr != nil {
		return f.startErr
	}
	f.mu.Lock()
	f.connected[id] = true
	f.mu.Unlock()
	return nil
}
func (f *fakeClients) StopAccount(id int64) {
	f.record(fmt.Sprintf("stop:%d", id))
	f.mu.Lock()
	delete(f.connected, id)
	f.mu.Unlock()
}
func (f *fakeClients) SendCode(_ context.Context, id int64) error {
	f.record(fmt.Sprintf("code:%d", id))
	return nil
}
func (f *fakeClients) VerifyCode(_ context.Context, id int64, code, password string) error {
	f.record(fmt.Sprintf("verify:%d:%s:%s", id, code, password))
	return f.verifyErr
}
func (f *fakeClients) Logout(_ context.Context, id int64) error {
	f.record(fmt.Sprintf("logout:%d", id))
	return nil
}
func (f *fakeClients) Subscribe(_ context.Context, account int64, username string, keywords []string, join bool) (*model.TelegramClientSubscription, error) {
	f.record(fmt.Sprintf("subscribe:%d:%s:%v:%v", account, username, keywords, join))
	return &model.TelegramClientSubscription{ID: 11, AccountID: account, Username: username}, nil
}
func (f *fakeClients) Unsubscribe(_ context.Context, id int64) error {
	f.record(fmt.Sprintf("leave:%d", id))
	return nil
}
func (f *fakeClients) SendMessage(_ context.Context, id int64, recipient, text string) error {
	f.record(fmt.Sprintf("send:%d:%s:%s", id, recipient, text))
	return f.sendErr
}
func (f *fakeClients) ConnectedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(f.connected))
	for id := range f.connected {
		ids = append(ids, id)
	}
	return ids
}
func (f *fakeClients) SetSink(s message.Sink) { f.sink = s }
func (f *fakeClients) SetAgentID(id string)   { f.agent = id }
func (f *fakeClients) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

type fixture struct {
	t       *testing.T
	db      *gorm.DB
	rt      *Runtime
	bots    *rdb.TelegramBotDAO
	clients *fakeClients
	made    map[int64]*fakeBot
	failFor map[string]error
	pub     *fakePublisher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, db: testDB(t), clients: newFakeClients(), made: map[int64]*fakeBot{}, failFor: map[string]error{}}
	f.pub = &fakePublisher{}
	f.rt = New(f.db, Options{
		AgentID:   "agent-a",
		Publisher: f.pub,
		Clients:   f.clients,
		Engine:    workflow.NewEngine(rdb.NewWorkflowStore(f.db), rdb.NewNotifyTemplateDAO(f.db), rdb.NewTelegramBindingDAO(f.db), nil),
		BotFactory: func(row model.TelegramBot, token string) (BotRunner, error) {
			if err := f.failFor[row.Name]; err != nil {
				return nil, err
			}
			require.Equal(t, botToken, token, "the agent decrypts the stored token")
			bot := &fakeBot{username: row.Name + "_bot", id: 1000 + row.ID}
			f.made[row.ID] = bot
			return bot, nil
		},
		ReconcileEvery: 20 * time.Millisecond,
		RequestEvery:   10 * time.Millisecond,
	})
	f.bots = rdb.NewTelegramBotDAO(f.db)
	return f
}

func (f *fixture) addBot(name string, enabled bool) *model.TelegramBot {
	f.t.Helper()
	token := botToken
	row, err := f.bots.Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token, Enabled: &enabled})
	require.NoError(f.t, err)
	return row
}

func (f *fixture) bot(id int64) model.TelegramBot {
	f.t.Helper()
	row, err := f.bots.Get(context.Background(), id)
	require.NoError(f.t, err)
	return *row
}

func TestReconcileRunsOnlyEnabledBotsAndReportsThem(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.addBot("alpha", true), f.addBot("beta", false)

	f.rt.Reconcile(ctx)

	require.Contains(t, f.made, a.ID)
	assert.NotContains(t, f.made, b.ID, "a disabled bot is never started")
	assert.Equal(t, 1, f.made[a.ID].started)
	got := f.bot(a.ID)
	assert.Equal(t, model.TelegramBotRunning, got.Status)
	assert.Equal(t, "alpha_bot", got.Username, "the username comes from getMe")
	assert.True(t, got.Connected)
	assert.Equal(t, model.TelegramBotStopped, f.bot(b.ID).Status)

	// A second pass neither restarts the bot nor loses its status.
	f.rt.Reconcile(ctx)
	assert.Equal(t, 1, f.made[a.ID].started)
}

func TestReconcileStopsBotsThatAreDisabledOrDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	f.rt.Reconcile(ctx)
	botCtx := f.made[a.ID].ctx
	require.NoError(t, botCtx.Err())

	_, err := f.bots.SetEnabled(ctx, []int64{a.ID}, false)
	require.NoError(t, err)
	f.rt.Reconcile(ctx)
	assert.Error(t, botCtx.Err(), "polling stops")
	assert.Equal(t, model.TelegramBotStopped, f.bot(a.ID).Status)
	assert.NotContains(t, f.rt.running, a.ID)

	// A bot disabled while the agent was down is also reported stopped.
	require.NoError(t, f.db.Model(&model.TelegramBot{}).Where("id = ?", a.ID).Update("status", model.TelegramBotRunning).Error)
	f.rt.Reconcile(ctx)
	assert.Equal(t, model.TelegramBotStopped, f.bot(a.ID).Status)
}

func TestChangingTheTokenRestartsTheBot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	f.rt.Reconcile(ctx)
	first := f.made[a.ID]

	other := "654321:ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvu"
	_, err := f.bots.Update(ctx, a.ID, rdb.TelegramBotInput{Token: &other})
	require.NoError(t, err)
	f.rt.factory = func(row model.TelegramBot, token string) (BotRunner, error) {
		assert.Equal(t, other, token)
		f.made[row.ID] = &fakeBot{username: "renamed_bot", id: 1}
		return f.made[row.ID], nil
	}
	f.rt.Reconcile(ctx)
	assert.Error(t, first.ctx.Err(), "the old connection is closed")
	assert.Equal(t, "renamed_bot", f.bot(a.ID).Username)
}

func TestABotThatFailsToStartIsReportedAndRetriedLater(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	f.failFor["alpha"] = errors.New("401 unauthorized")

	f.rt.Reconcile(ctx)
	got := f.bot(a.ID)
	assert.Equal(t, model.TelegramBotError, got.Status)
	assert.Contains(t, got.LastError, "401")
	assert.False(t, got.Connected)

	// Not retried on the very next tick...
	delete(f.failFor, "alpha")
	f.rt.Reconcile(ctx)
	assert.Empty(t, f.made)
	// ...but once the delay has passed it starts and the error clears.
	f.rt.retryAt[a.ID] = time.Now().Add(-time.Second)
	f.rt.Reconcile(ctx)
	require.Contains(t, f.made, a.ID)
	got = f.bot(a.ID)
	assert.Equal(t, model.TelegramBotRunning, got.Status)
	assert.Empty(t, got.LastError)
}

func TestWebhookModeIsRefusedUntilItIsSupported(t *testing.T) {
	f := newFixture(t)
	a := f.addBot("alpha", true)
	require.NoError(t, f.db.Model(&model.TelegramBot{}).Where("id = ?", a.ID).Update("mode", model.TelegramModeWebhook).Error)
	f.rt.Reconcile(context.Background())
	assert.Equal(t, model.TelegramBotError, f.bot(a.ID).Status)
	assert.Empty(t, f.made)
}

func TestCommandsAreSyncedPerBotAndOnlyWhenTheyChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.addBot("alpha", true), f.addBot("beta", true)
	cmds := []model.BotCommandDef{
		{BotID: 0, Command: "/help", Name: "help", Description: "Shared help", Enabled: true},
		{BotID: a.ID, Command: "/status", Name: "status", Description: "Alpha status", Enabled: true},
		{BotID: b.ID, Command: "/help", Name: "help", Description: "Beta help", Enabled: true},
		{BotID: a.ID, Command: "/off", Name: "off", Description: "disabled", Enabled: true},
		{BotID: a.ID, Command: "plain", Name: "pattern only", Description: "no slash", Enabled: true},
	}
	for i := range cmds {
		require.NoError(t, f.db.Create(&cmds[i]).Error)
	}
	require.NoError(t, f.db.Model(&model.BotCommandDef{}).Where("command = ?", "/off").Update("enabled", false).Error)

	f.rt.Reconcile(ctx)
	alpha, beta := f.made[a.ID], f.made[b.ID]
	require.Equal(t, 1, alpha.syncCount())
	assert.Equal(t, []telegram.CommandSpec{{Command: "help", Description: "Shared help"}, {Command: "status", Description: "Alpha status"}}, alpha.synced[0])
	assert.Equal(t, []telegram.CommandSpec{{Command: "help", Description: "Beta help"}}, beta.synced[0], "a bot's own command wins over the shared one")
	assert.NotNil(t, f.bot(a.ID).CommandsSyncedAt)

	f.rt.Reconcile(ctx)
	assert.Equal(t, 1, alpha.syncCount(), "unchanged commands are not pushed again")

	require.NoError(t, f.db.Model(&model.BotCommandDef{}).Where("command = ? AND bot_id = ?", "/status", a.ID).Update("description", "New text").Error)
	f.rt.Reconcile(ctx)
	assert.Equal(t, 2, alpha.syncCount(), "an edited command is pushed")
	assert.Equal(t, 1, beta.syncCount())
}

func TestCommandSyncFailureIsRetriedNextTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	require.NoError(t, f.db.Create(&model.BotCommandDef{Command: "/help", Name: "h", Description: "d", Enabled: true}).Error)
	f.rt.Reconcile(ctx)
	bot := f.made[a.ID]
	require.Equal(t, 1, bot.syncCount())

	require.NoError(t, f.db.Create(&model.BotCommandDef{Command: "/more", Name: "m", Description: "d", Enabled: true}).Error)
	bot.syncErr = errors.New("telegram is down")
	f.rt.Reconcile(ctx)
	assert.Equal(t, 1, bot.syncCount())
	bot.syncErr = nil
	f.rt.Reconcile(ctx)
	assert.Equal(t, 2, bot.syncCount())

	assert.ErrorIs(t, f.rt.syncCommands(ctx, 999), errBotNotRunning)
}

func TestAgentRequestsAreExecutedAndTheirSecretsWiped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.addBot("alpha", true)
	f.rt.Reconcile(ctx)
	require.Equal(t, 1, f.made[a.ID].syncCount())

	q := f.rt.requests
	enqueue := func(kind string, target int64, payload any) int64 {
		row, err := q.Enqueue(ctx, kind, target, payload)
		require.NoError(t, err)
		return row.ID
	}
	ids := []int64{
		enqueue(model.AgentReqSendCode, 3, nil),
		enqueue(model.AgentReqVerifyCode, 3, verifyPayload{Code: "12345", Password: "hunter2"}),
		enqueue(model.AgentReqConnect, 3, nil),
		enqueue(model.AgentReqDisconnect, 3, nil),
		enqueue(model.AgentReqLogout, 3, nil),
		enqueue(model.AgentReqSubscribe, 3, subscribePayload{AccountID: 3, ChannelLink: "https://t.me/newsfeed", Join: true}),
		enqueue(model.AgentReqUnsubscribe, 11, nil),
		enqueue(model.AgentReqSyncBotCommand, a.ID, nil),
		enqueue(model.AgentReqSendMessage, 3, sendPayload{Recipient: "123456", Text: "hello"}),
	}
	f.rt.drainRequests(ctx)

	for _, call := range []string{"code:3", "verify:3:12345:hunter2", "connect:3", "stop:3", "logout:3", "subscribe:3:https://t.me/newsfeed:[]:true", "leave:11", "send:3:123456:hello"} {
		assert.True(t, f.clients.called(call), call)
	}
	assert.Equal(t, 2, f.made[a.ID].syncCount(), "a sync request pushes the commands even though nothing changed")
	for _, id := range ids {
		row, err := q.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, model.AgentReqDone, row.Status, row.Kind+": "+row.Error)
		assert.Empty(t, row.PayloadEncrypted, "no code or password stays stored")
	}
	sub, _ := q.Get(ctx, ids[5])
	assert.Contains(t, sub.ResultJSON, `"username":"https://t.me/newsfeed"`, "the created subscription is returned")
}

func TestAgentRequestFailuresAreReturnedToTheCaller(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.clients.verifyErr = errors.New("telegram two-step verification password is required")
	q := f.rt.requests
	bad, _ := q.Enqueue(ctx, model.AgentReqVerifyCode, 1, verifyPayload{Code: "1"})
	unknown, _ := q.Enqueue(ctx, "account.teleport", 1, nil)
	noBot, _ := q.Enqueue(ctx, model.AgentReqSyncBotCommand, 404, nil)
	f.rt.drainRequests(ctx)

	row, _ := q.Get(ctx, bad.ID)
	assert.Equal(t, model.AgentReqFailed, row.Status)
	assert.Contains(t, row.Error, "password is required")
	row, _ = q.Get(ctx, unknown.ID)
	assert.Contains(t, row.Error, "unknown request kind")
	row, _ = q.Get(ctx, noBot.ID)
	assert.Contains(t, row.Error, "not running")

	_, err := q.Wait(ctx, bad.ID, time.Second)
	assert.ErrorContains(t, err, "password is required")
}

func TestAccountsAreReconnectedAndStoppedWhenTheyStopBeingRunnable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Now()
	good := model.TelegramClientAccount{Name: "a", PhoneNumber: "+1", APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true}
	off := model.TelegramClientAccount{Name: "b", PhoneNumber: "+2", APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true}
	require.NoError(t, f.db.Create(&good).Error)
	require.NoError(t, f.db.Create(&off).Error)
	require.NoError(t, f.db.Model(&model.TelegramClientAccount{}).Where("id = ?", off.ID).Update("enabled", false).Error)

	f.rt.Reconcile(ctx)
	assert.True(t, f.clients.called(fmt.Sprintf("connect:%d", good.ID)))
	assert.False(t, f.clients.called(fmt.Sprintf("connect:%d", off.ID)))

	// A session the agent holds for an account that was disabled is closed.
	f.clients.connected[off.ID] = true
	f.rt.Reconcile(ctx)
	assert.True(t, f.clients.called(fmt.Sprintf("stop:%d", off.ID)))

	// A failed connection is recorded on the account and retried after a delay.
	require.NoError(t, f.db.Model(&model.TelegramClientAccount{}).Where("id = ?", good.ID).Update("connection_status", model.TelegramClientConnected).Error)
	delete(f.clients.connected, good.ID)
	f.clients.startErr = errors.New("session revoked")
	f.rt.acctNext[good.ID] = now.Add(-time.Second)
	f.rt.Reconcile(ctx)
	var row model.TelegramClientAccount
	require.NoError(t, f.db.First(&row, good.ID).Error)
	assert.Equal(t, model.TelegramClientError, row.ConnectionStatus)
	assert.Contains(t, row.LastError, "session revoked")
	calls := len(f.clients.calls)
	f.rt.Reconcile(ctx)
	assert.Equal(t, calls, len(f.clients.calls), "no reconnect storm")
}

func TestRunDrivesTheLoopsAndStopsCleanly(t *testing.T) {
	f := newFixture(t)
	a := f.addBot("alpha", true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.rt.Run(ctx) }()

	require.Eventually(t, func() bool { return f.bot(a.ID).Status == model.TelegramBotRunning }, 3*time.Second, 10*time.Millisecond)
	req, err := f.rt.requests.Enqueue(ctx, model.AgentReqSendCode, 5, nil)
	require.NoError(t, err)
	done2, err := f.rt.requests.Wait(ctx, req.ID, 3*time.Second)
	require.NoError(t, err)
	assert.Equal(t, model.AgentReqDone, done2.Status)
	assert.True(t, f.clients.called("start"))
	assert.NotNil(t, f.clients.sink, "the dispatcher receives client messages")

	f.clients.mu.Lock()
	f.clients.connected[8] = true
	f.clients.mu.Unlock()
	cancel()
	require.NoError(t, <-done)
	assert.Equal(t, model.TelegramBotStopped, f.bot(a.ID).Status)
	assert.True(t, f.clients.called("stop:8"), "sessions are closed on shutdown")
}

func TestRunRequiresADatabase(t *testing.T) {
	model.DB = nil
	assert.ErrorContains(t, Run(context.Background()), "not initialized")
}

func TestFingerprintChangesWithEverythingTheConnectionDependsOn(t *testing.T) {
	base := model.TelegramBot{TokenEncrypted: "a", APIBaseURL: "u", Mode: "polling"}
	seen := map[string]bool{fingerprint(base): true}
	for _, changed := range []model.TelegramBot{
		{TokenEncrypted: "b", APIBaseURL: "u", Mode: "polling"},
		{TokenEncrypted: "a", APIBaseURL: "v", Mode: "polling"},
		{TokenEncrypted: "a", APIBaseURL: "u", Mode: "webhook"},
	} {
		assert.False(t, seen[fingerprint(changed)])
		seen[fingerprint(changed)] = true
	}
	assert.Equal(t, fingerprint(base), fingerprint(model.TelegramBot{TokenEncrypted: "a", APIBaseURL: "u", Mode: "polling", Name: "ignored"}))
}

func TestAnAccountTelegramLimitedIsPausedAndNotAskedAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	account := model.TelegramClientAccount{Name: "a", PhoneNumber: "+1", APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true}
	require.NoError(t, f.db.Create(&account).Error)
	q := f.rt.requests
	send := func() *model.TelegramAgentRequest {
		row, err := q.Enqueue(ctx, model.AgentReqSendMessage, account.ID, sendPayload{Recipient: "123456", Text: "hello"})
		require.NoError(t, err)
		f.rt.drainRequests(ctx)
		done, err := q.Get(ctx, row.ID)
		require.NoError(t, err)
		return done
	}

	f.clients.sendErr = fmt.Errorf("%w: rpc error code 400: PEER_FLOOD", telegramclient.ErrPeerFlood)
	first := send()
	assert.Equal(t, model.AgentReqFailed, first.Status)
	assert.Contains(t, first.Error, "PEER_FLOOD")
	var row model.TelegramClientAccount
	require.NoError(t, f.db.First(&row, account.ID).Error)
	require.NotNil(t, row.SendBlockedUntil, "the limit is kept so the page can show it")
	assert.True(t, row.SendBlocked(time.Now()))
	assert.WithinDuration(t, time.Now().Add(sendBlockFor), *row.SendBlockedUntil, time.Minute)
	assert.Contains(t, row.SendBlockedReason, "PEER_FLOOD")

	sent := len(f.clients.calls)
	second := send()
	assert.Equal(t, model.AgentReqFailed, second.Status)
	assert.Contains(t, second.Error, "cannot send until", "the next message fails at once and says why")
	assert.Len(t, f.clients.calls, sent, "Telegram is not asked again while the account is paused")

	// when the pause is over the account sends again
	past := time.Now().Add(-time.Minute)
	require.NoError(t, f.db.Model(&model.TelegramClientAccount{}).Where("id = ?", account.ID).Update("send_blocked_until", past).Error)
	f.clients.sendErr = nil
	assert.Equal(t, model.AgentReqDone, send().Status)

	// other errors do not pause the account
	f.clients.sendErr = errors.New("the network dropped")
	assert.Equal(t, model.AgentReqFailed, send().Status)
	require.NoError(t, f.db.First(&row, account.ID).Error)
	assert.False(t, row.SendBlocked(time.Now()))
}
