package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

const agentTestToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"

func agentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramBot{}, &model.TelegramAgentRequest{}, &model.TelegramForwardRule{}, &model.TelegramForwardChannel{}, &model.WorkflowDef{},
		&model.BotCommandDef{}, &model.TelegramClientAccount{}, &model.TelegramClientSubscription{}))
	return db
}

// directChannel makes a channel that hands messages straight to a job type and returns its id.
func directChannel(t *testing.T, db *gorm.DB, name string) int64 {
	t.Helper()
	ch, err := NewTelegramForwardChannelDAO(db).Create(context.Background(), ChannelInput{TelegramForwardChannel: model.TelegramForwardChannel{
		Name: name, Transport: model.ChannelTransportDirect, ConsumerKind: model.ChannelConsumerJob, ConsumerRef: "llm_rating",
		Enabled: true, ConsumerEnabled: true,
	}})
	require.NoError(t, err)
	return ch.ID
}

func str(s string) *string { return &s }
func flag(b bool) *bool    { return &b }

func TestBotsAreValidatedStoredEncryptedAndFiltered(t *testing.T) {
	dao := NewTelegramBotDAO(agentTestDB(t))
	ctx := context.Background()

	_, err := dao.Create(ctx, TelegramBotInput{Token: str(agentTestToken)})
	assert.ErrorAs(t, err, &InvalidInputError{}, "a name is required")
	_, err = dao.Create(ctx, TelegramBotInput{Name: str(" "), Token: str(agentTestToken)})
	assert.Error(t, err)
	_, err = dao.Create(ctx, TelegramBotInput{Name: str("a")})
	assert.ErrorAs(t, err, &InvalidInputError{})
	_, err = dao.Create(ctx, TelegramBotInput{Name: str("a"), Token: str("12:short")})
	assert.ErrorAs(t, err, &InvalidInputError{})
	_, err = dao.Create(ctx, TelegramBotInput{Name: str("a"), Token: str(agentTestToken), Mode: str("webhook")})
	assert.ErrorContains(t, err, "not supported")
	_, err = dao.Create(ctx, TelegramBotInput{Name: str("a"), Token: str(agentTestToken), Mode: str("smoke")})
	assert.ErrorContains(t, err, "unknown")

	first, err := dao.Create(ctx, TelegramBotInput{Name: str(" alpha "), Token: str(" " + agentTestToken + " "), Username: str("@alpha_bot"), APIBaseURL: str(" http://bot-api:8081 "), MiniAppURL: str("https://app"), Enabled: flag(true)})
	require.NoError(t, err)
	assert.Equal(t, "alpha", first.Name)
	assert.Equal(t, "alpha_bot", first.Username)
	assert.Equal(t, "http://bot-api:8081", first.APIBaseURL)
	assert.NotContains(t, first.TokenEncrypted, agentTestToken)
	token, err := dao.Token(first)
	require.NoError(t, err)
	assert.Equal(t, agentTestToken, token)
	second, err := dao.Create(ctx, TelegramBotInput{Name: str("beta"), Token: str(agentTestToken), Enabled: flag(false)})
	require.NoError(t, err)
	assert.False(t, second.Enabled, "a bot created disabled stays disabled")
	_, err = dao.Create(ctx, TelegramBotInput{Name: str("alpha"), Token: str(agentTestToken)})
	assert.Error(t, err, "names are unique")

	rows, total, err := dao.List(ctx, 1, 10, TelegramBotFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.True(t, rows[0].HasToken)
	_, total, _ = dao.List(ctx, 1, 10, TelegramBotFilter{Keyword: "alp"})
	assert.EqualValues(t, 1, total)
	_, total, _ = dao.List(ctx, 1, 10, TelegramBotFilter{Enabled: flag(false)})
	assert.EqualValues(t, 1, total)
	_, total, _ = dao.List(ctx, 1, 10, TelegramBotFilter{Status: model.TelegramBotError})
	assert.Zero(t, total)
	page, total, _ := dao.List(ctx, 2, 1, TelegramBotFilter{})
	assert.EqualValues(t, 2, total)
	assert.Len(t, page, 1)

	enabled, err := dao.ListEnabled(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 1)
	assert.Equal(t, first.ID, enabled[0].ID)
}

func TestBotUpdatesKeepTheTokenUnlessReplacedAndTrackRuntimeState(t *testing.T) {
	dao := NewTelegramBotDAO(agentTestDB(t))
	ctx := context.Background()
	bot, err := dao.Create(ctx, TelegramBotInput{Name: str("alpha"), Token: str(agentTestToken)})
	require.NoError(t, err)
	before := bot.TokenEncrypted

	got, err := dao.Update(ctx, bot.ID, TelegramBotInput{Name: str("renamed"), Token: str(""), Username: str("@x_bot"), Mode: str("polling"), APIBaseURL: str("u"), MiniAppURL: str("m"), Enabled: flag(false)})
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, before, got.TokenEncrypted)
	assert.False(t, got.Enabled)
	_, err = dao.Update(ctx, bot.ID, TelegramBotInput{Name: str("  ")})
	assert.ErrorAs(t, err, &InvalidInputError{})
	_, err = dao.Update(ctx, bot.ID, TelegramBotInput{Token: str("bad")})
	assert.ErrorAs(t, err, &InvalidInputError{})
	_, err = dao.Update(ctx, bot.ID, TelegramBotInput{Mode: str("webhook")})
	assert.Error(t, err)
	_, err = dao.Update(ctx, 404, TelegramBotInput{Name: str("x")})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = dao.Update(ctx, 404, TelegramBotInput{})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "nothing to change still checks the bot exists")

	require.NoError(t, dao.MarkCommandsSynced(ctx, bot.ID, "abc"))
	other := "654321:ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvu"
	got, err = dao.Update(ctx, bot.ID, TelegramBotInput{Token: &other})
	require.NoError(t, err)
	assert.NotEqual(t, before, got.TokenEncrypted)
	assert.Empty(t, got.CommandsHash, "a new token forces the commands to be pushed again")

	require.NoError(t, dao.MarkRunning(ctx, bot.ID, "real_bot", 77))
	got, _ = dao.Get(ctx, bot.ID)
	assert.True(t, got.Connected)
	assert.Equal(t, "real_bot", got.Username)
	assert.EqualValues(t, 77, got.BotUserID)
	require.NoError(t, dao.MarkError(ctx, bot.ID, "boom"))
	got, _ = dao.Get(ctx, bot.ID)
	assert.False(t, got.Connected)
	assert.Equal(t, "boom", got.LastError)
	require.NoError(t, dao.MarkRunning(ctx, bot.ID, "", 77))
	require.NoError(t, dao.MarkStopped(ctx, bot.ID))
	got, _ = dao.Get(ctx, bot.ID)
	assert.Equal(t, model.TelegramBotStopped, got.Status)
	assert.Equal(t, "real_bot", got.Username, "an empty getMe username keeps the stored one")

	// A heartbeat older than the freshness window is not "connected".
	old := time.Now().Add(-10 * time.Minute)
	require.NoError(t, dao.db.Model(&model.TelegramBot{}).Where("id = ?", bot.ID).Updates(map[string]any{"status": model.TelegramBotRunning, "seen_at": &old}).Error)
	got, _ = dao.Get(ctx, bot.ID)
	assert.False(t, got.Connected)
	require.NoError(t, dao.Touch(ctx, []int64{bot.ID}))
	require.NoError(t, dao.Touch(ctx, nil))
	got, _ = dao.Get(ctx, bot.ID)
	assert.True(t, got.Connected)

	require.NoError(t, dao.MarkCommandsSynced(ctx, bot.ID, "h"))
	require.NoError(t, dao.ClearCommandsHash(ctx, bot.ID))
	got, _ = dao.Get(ctx, bot.ID)
	assert.Empty(t, got.CommandsHash)
}

func TestBotCommandsAreScopedAndBotsDeletedWithTheirs(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramBotDAO(db)
	ctx := context.Background()
	a, _ := dao.Create(ctx, TelegramBotInput{Name: str("a"), Token: str(agentTestToken)})
	b, _ := dao.Create(ctx, TelegramBotInput{Name: str("b"), Token: str(agentTestToken)})
	for _, c := range []model.BotCommandDef{
		{BotID: 0, Command: "/help", Name: "shared", Enabled: true},
		{BotID: a.ID, Command: "/help", Name: "a-help", Enabled: true},
		{BotID: a.ID, Command: "/only_a", Name: "a", Enabled: true},
		{BotID: b.ID, Command: "/only_b", Name: "b", Enabled: true},
		{BotID: a.ID, Command: "/off", Name: "off", Enabled: true},
	} {
		require.NoError(t, db.Create(&c).Error)
	}
	require.NoError(t, db.Model(&model.BotCommandDef{}).Where("command = ?", "/off").Update("enabled", false).Error)

	cmds, err := dao.CommandsFor(ctx, a.ID)
	require.NoError(t, err)
	names := map[string]string{}
	for _, c := range cmds {
		names[c.Command] = c.Name
	}
	assert.Equal(t, map[string]string{"/help": "a-help", "/only_a": "a"}, names, "own commands win, other bots' and disabled ones are left out")
	counts := dao.CommandCounts(ctx, []model.TelegramBot{*a, *b})
	assert.EqualValues(t, 2, counts[a.ID])
	assert.EqualValues(t, 2, counts[b.ID])

	// The same command may exist once per bot, not twice for one bot.
	assert.NoError(t, db.Create(&model.BotCommandDef{BotID: b.ID, Command: "/help", Name: "b-help", Enabled: true}).Error)
	assert.Error(t, db.Create(&model.BotCommandDef{BotID: b.ID, Command: "/help", Name: "dup", Enabled: true}).Error)

	store := NewBotCommandDAO(db)
	forA, err := store.ListForBot(ctx, a.ID)
	require.NoError(t, err)
	assert.Len(t, forA, 4, "a's three (including the disabled one) plus the shared one")
	scoped := NewWorkflowStore(db)
	list, err := scoped.ListCommands(model.WithBot(ctx, b.ID))
	require.NoError(t, err)
	for _, c := range list {
		assert.Contains(t, []int64{0, b.ID}, c.BotID)
	}
	all, err := scoped.ListCommands(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 6)

	n, err := dao.Delete(ctx, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = dao.Delete(ctx, []int64{a.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	var left int64
	db.Model(&model.BotCommandDef{}).Where("bot_id = ?", a.ID).Count(&left)
	assert.Zero(t, left)
	n, err = dao.SetEnabled(ctx, nil, true)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestDefaultSenderIsTheFirstEnabledBot(t *testing.T) {
	dao := NewTelegramBotDAO(agentTestDB(t))
	ctx := context.Background()
	_, _, _, ok, err := dao.DefaultSender(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "no bot, no sender")

	_, _ = dao.Create(ctx, TelegramBotInput{Name: str("off"), Token: str(agentTestToken), Enabled: flag(false)})
	other := "654321:ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvu"
	_, _ = dao.Create(ctx, TelegramBotInput{Name: str("on"), Token: &other, APIBaseURL: str("http://x"), MiniAppURL: str("https://m")})
	token, base, mini, ok, err := dao.DefaultSender(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, other, token)
	assert.Equal(t, "http://x", base)
	assert.Equal(t, "https://m", mini)

	require.NoError(t, dao.db.Model(&model.TelegramBot{}).Where("name = ?", "on").Update("token_encrypted", "not-encrypted").Error)
	_, _, _, ok, err = dao.DefaultSender(ctx)
	assert.Error(t, err, "a token that cannot be decrypted is an error, not a silent no-op")
	assert.False(t, ok)
}

func TestAgentRequestsAreClaimedOnceAndAnsweredToTheWaiter(t *testing.T) {
	dao := NewTelegramAgentRequestDAO(agentTestDB(t))
	ctx := context.Background()

	none, err := dao.Claim(ctx)
	require.NoError(t, err)
	assert.Nil(t, none)

	first, err := dao.Enqueue(ctx, model.AgentReqVerifyCode, 4, map[string]string{"code": "1", "password": "pw"})
	require.NoError(t, err)
	second, err := dao.Enqueue(ctx, model.AgentReqSendCode, 5, nil)
	require.NoError(t, err)
	assert.NotContains(t, first.PayloadEncrypted, "pw", "payloads are encrypted")
	assert.Empty(t, second.PayloadEncrypted)

	claimed, err := dao.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, first.ID, claimed.ID, "oldest first")
	var payload map[string]string
	require.NoError(t, dao.Payload(claimed, &payload))
	assert.Equal(t, "pw", payload["password"])
	require.NoError(t, dao.Payload(&model.TelegramAgentRequest{}, &payload), "no payload is fine")
	assert.Error(t, dao.Payload(&model.TelegramAgentRequest{PayloadEncrypted: "garbage"}, &payload))

	again, _ := dao.Claim(ctx)
	assert.Equal(t, second.ID, again.ID, "a claimed request is not handed out twice")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = dao.Finish(ctx, first.ID, map[string]int{"n": 1}, nil)
		_ = dao.Finish(ctx, second.ID, nil, errors.New("nope"))
	}()
	done, err := dao.Wait(ctx, first.ID, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, model.AgentReqDone, done.Status)
	assert.JSONEq(t, `{"n":1}`, done.ResultJSON)
	assert.Empty(t, done.PayloadEncrypted, "the secret is gone once handled")
	failed, err := dao.Wait(ctx, second.ID, 5*time.Second)
	assert.EqualError(t, err, "nope")
	assert.Equal(t, model.AgentReqFailed, failed.Status)

	_, err = dao.Get(ctx, 404)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = dao.Wait(ctx, 404, time.Second)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAnUnansweredRequestTimesOutAndIsCancelled(t *testing.T) {
	dao := NewTelegramAgentRequestDAO(agentTestDB(t))
	ctx := context.Background()
	req, err := dao.Enqueue(ctx, model.AgentReqVerifyCode, 1, map[string]string{"code": "1"})
	require.NoError(t, err)

	_, err = dao.Wait(ctx, req.ID, 250*time.Millisecond)
	assert.ErrorIs(t, err, ErrAgentTimeout)
	row, _ := dao.Get(ctx, req.ID)
	assert.Equal(t, model.AgentReqFailed, row.Status)
	assert.Empty(t, row.PayloadEncrypted)
	claimed, _ := dao.Claim(ctx)
	assert.Nil(t, claimed, "a late agent does not run a request nobody is waiting for")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	pending, _ := dao.Enqueue(ctx, model.AgentReqSendCode, 1, nil)
	_, err = dao.Wait(cancelled, pending.ID, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSweepFailsStuckRequestsAndDropsOldOnes(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramAgentRequestDAO(db)
	ctx := context.Background()
	stuck, _ := dao.Enqueue(ctx, model.AgentReqLogout, 1, map[string]string{"x": "y"})
	claimed, _ := dao.Claim(ctx)
	require.Equal(t, stuck.ID, claimed.ID)
	finished, _ := dao.Enqueue(ctx, model.AgentReqSendCode, 1, nil)
	require.NoError(t, dao.Finish(ctx, finished.ID, nil, nil))
	fresh, _ := dao.Enqueue(ctx, model.AgentReqConnect, 1, nil)

	old := time.Now().Add(-5 * time.Minute)
	require.NoError(t, db.Model(&model.TelegramAgentRequest{}).Where("id = ?", stuck.ID).Update("claimed_at", &old).Error)
	require.NoError(t, db.Model(&model.TelegramAgentRequest{}).Where("id = ?", finished.ID).Update("updated_at", time.Now().Add(-3*time.Hour)).Error)

	require.NoError(t, dao.Sweep(ctx, time.Now(), time.Hour))

	row, _ := dao.Get(ctx, stuck.ID)
	assert.Equal(t, model.AgentReqFailed, row.Status)
	assert.Contains(t, row.Error, "stopped")
	assert.Empty(t, row.PayloadEncrypted)
	_, err := dao.Get(ctx, finished.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "old finished requests are removed")
	row, _ = dao.Get(ctx, fresh.ID)
	assert.Equal(t, model.AgentReqPending, row.Status, "pending work is kept")
}

func TestForwardRulesApplyPerSubscriptionOrToAll(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramForwardRuleDAO(db)
	channel := directChannel(t, db, "jobs")
	ctx := context.Background()
	mk := func(name string, sub int64, enabled bool) *model.TelegramForwardRule {
		row := &model.TelegramForwardRule{Name: name, SubscriptionID: sub, TargetKind: "channel", ChannelID: channel, Enabled: enabled}
		require.NoError(t, dao.Create(ctx, row))
		return row
	}
	all, one, other, off := mk("all", 0, true), mk("one", 3, true), mk("other", 4, true), mk("off", 3, false)
	assert.False(t, off.Enabled)

	rules, err := dao.ForSource(ctx, 1, 3, model.TelegramSourceChannel)
	require.NoError(t, err)
	names := []string{}
	for _, r := range rules {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"all", "one"}, names, "shared and own rules, enabled only")
	_ = other

	got, err := dao.Get(ctx, one.ID)
	require.NoError(t, err)
	assert.Equal(t, "one", got.Name)
	rows, total, err := dao.List(ctx, 1, 10, TelegramForwardRuleFilter{Keyword: "o", TargetKind: "channel", SubscriptionID: 3, Enabled: flag(true)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "one", rows[0].Name)

	updated, err := dao.Update(ctx, all.ID, model.TelegramForwardRule{Pattern: "BTC", Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, "all", updated.Name, "unspecified fields keep their stored value")
	assert.Equal(t, "channel", updated.TargetKind)
	assert.Equal(t, channel, updated.ChannelID)
	_, err = dao.Update(ctx, 404, model.TelegramForwardRule{})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = dao.Update(ctx, all.ID, model.TelegramForwardRule{TargetKind: "notify"})
	assert.ErrorAs(t, err, &InvalidInputError{}, "switching to notify needs a recipient")

	n, err := dao.SetEnabled(ctx, []int64{one.ID, off.ID}, false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	n, err = dao.SetEnabled(ctx, nil, true)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = dao.Delete(ctx, []int64{one.ID, off.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	n, err = dao.Delete(ctx, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestClientAccountStatesAndSubscriptionMembership(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	a := model.TelegramClientAccount{Name: "a", PhoneNumber: "+1", APIID: 1, APIHashEncrypted: "x", SessionEncrypted: "s", Authorized: true, Enabled: true}
	require.NoError(t, db.Create(&a).Error)

	got, err := dao.GetAccount(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, got.Connected, "not connected until the agent says so")
	require.NoError(t, dao.SetConnectionResult(ctx, a.ID, true, ""))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.True(t, got.Connected)
	assert.Equal(t, model.TelegramClientConnected, got.ConnectionStatus)

	old := time.Now().Add(-time.Hour)
	require.NoError(t, db.Model(&a).Update("seen_at", &old).Error)
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.False(t, got.Connected, "a stale heartbeat means the agent is gone")
	require.NoError(t, dao.TouchAccounts(ctx, []int64{a.ID}))
	require.NoError(t, dao.TouchAccounts(ctx, nil))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.True(t, got.Connected)

	require.NoError(t, dao.SetConnectionResult(ctx, a.ID, false, "session revoked"))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.Equal(t, model.TelegramClientError, got.ConnectionStatus)
	require.NoError(t, dao.SetConnectionResult(ctx, a.ID, false, ""))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.Equal(t, model.TelegramClientDisconnected, got.ConnectionStatus)
	require.NoError(t, dao.SetConnectionResult(ctx, a.ID, true, ""))
	require.NoError(t, dao.ResetConnectionStates(ctx))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.Equal(t, model.TelegramClientDisconnected, got.ConnectionStatus, "a restarted agent starts from a clean slate")
	require.NoError(t, dao.SetConnectionResult(ctx, a.ID, true, ""))
	require.NoError(t, dao.MarkUnauthorized(ctx, a.ID))
	got, _ = dao.GetAccount(ctx, a.ID)
	assert.Equal(t, model.TelegramClientDisconnected, got.ConnectionStatus)
	assert.False(t, got.Authorized)

	sub := &model.TelegramClientSubscription{AccountID: a.ID, PeerID: 9, PeerType: "channel", Enabled: true}
	require.NoError(t, dao.CreateSubscription(ctx, sub))
	assert.Equal(t, model.TelegramSubPending, sub.State, "a new subscription starts pending")
	found, err := dao.FindSubscription(ctx, a.ID, 9)
	require.NoError(t, err)
	assert.Equal(t, sub.ID, found.ID)
	require.NoError(t, dao.SetSubscriptionState(ctx, sub.ID, model.TelegramSubJoined, ""))
	got2, err := dao.GetSubscription(ctx, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TelegramSubJoined, got2.State)
	assert.True(t, got2.Enabled)
	require.NoError(t, dao.SetSubscriptionState(ctx, sub.ID, model.TelegramSubLeft, ""))
	got2, _ = dao.GetSubscription(ctx, sub.ID)
	assert.False(t, got2.Enabled, "leaving a channel also stops receiving from it")
	_, err = dao.GetSubscription(ctx, 404)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSubscriptionsCanBeFilteredByMembershipState(t *testing.T) {
	db := agentTestDB(t)
	dao := NewTelegramClientDAO(db)
	ctx := context.Background()
	for i, state := range []string{model.TelegramSubJoined, model.TelegramSubJoined, model.TelegramSubError} {
		require.NoError(t, dao.CreateSubscription(ctx, &model.TelegramClientSubscription{AccountID: 1, PeerID: int64(i + 1), PeerType: "channel", State: state, Enabled: true}))
	}
	count := func(state string) int64 {
		req := &apis.TelegramClientSubscriptionListReq{State: state}
		req.Page, req.PageSize = 1, 10
		_, n, err := dao.ListSubscriptions(ctx, req)
		require.NoError(t, err)
		return n
	}
	assert.EqualValues(t, 3, count(""))
	assert.EqualValues(t, 2, count(model.TelegramSubJoined))
	assert.EqualValues(t, 1, count(model.TelegramSubError))
}

func TestATelegramNotifyRecipientMustBeUsable(t *testing.T) {
	dao := NewTelegramForwardRuleDAO(agentTestDB(t))
	ctx := context.Background()
	mk := func(name, recipient string) error {
		return dao.Create(ctx, &model.TelegramForwardRule{Name: name, TargetKind: "notify", TargetRef: "telegram", TargetRecipient: recipient, Enabled: true})
	}
	assert.NoError(t, mk("user", "123456789"))
	assert.NoError(t, mk("group", "-1001234567890"))
	assert.NoError(t, mk("channel", "@kubeforecast_news"))
	for _, bad := range []string{"kubeforecast_news", "@abc", "some body", "12ab", "@1news"} {
		assert.ErrorAs(t, mk("bad "+bad, bad), &InvalidInputError{}, bad)
	}
	// Other notify channels are not judged by Telegram's rules.
	assert.NoError(t, dao.Create(ctx, &model.TelegramForwardRule{Name: "mail", TargetKind: "notify", TargetRef: "email", TargetRecipient: "ops@example.com", Enabled: true}))
}

func TestClaimForSkipsRequestsOfOtherAgentsAndKeepsOrder(t *testing.T) {
	dao := NewTelegramAgentRequestDAO(agentTestDB(t))
	ctx := context.Background()
	first, _ := dao.Enqueue(ctx, model.AgentReqConnect, 1, nil)
	second, _ := dao.Enqueue(ctx, model.AgentReqConnect, 2, nil)
	owners := map[int64]string{1: "b", 2: ""}
	owner := func(r *model.TelegramAgentRequest) (string, error) { return owners[r.TargetID], nil }

	got, err := dao.ClaimFor(ctx, "a", owner)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, second.ID, got.ID, "the older request belongs to agent b, so a takes the next one")
	again, _ := dao.ClaimFor(ctx, "a", owner)
	assert.Nil(t, again, "nothing else is for a")
	forB, err := dao.ClaimFor(ctx, "b", owner)
	require.NoError(t, err)
	require.NotNil(t, forB)
	assert.Equal(t, first.ID, forB.ID)
}
