package telegram

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	botTelegram "github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// botServer stands in for api.telegram.org and counts the messages the bot sends.
func botServer(t *testing.T) (*httptest.Server, *atomic.Int32, *string) {
	t.Helper()
	var sent atomic.Int32
	var text string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		text = body.Text
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server, &sent, &text
}

// accountAndBot is a notifier with a connected user account and an enabled bot (pointed at the fake Telegram).
func accountAndBot(t *testing.T, apiURL string) (*Notifier, *rdb.TelegramAgentRequestDAO, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:botfallback"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramClientAccount{}, &model.TelegramAgentRequest{}, &model.SysConfig{}, &model.SystemMarker{}))
	require.NoError(t, db.Create(&model.TelegramClientAccount{PhoneNumber: "+100", APIID: 1, Enabled: true, Authorized: true, ConnectionStatus: "connected"}).Error)
	for k, v := range map[string]string{
		botTelegram.ConfigBotEnabled: "true", botTelegram.ConfigBotToken: "t", botTelegram.ConfigBotAPIBaseURL: apiURL,
	} {
		require.NoError(t, db.Create(&model.SysConfig{ConfigKey: k, ConfigValue: v, ConfigName: k}).Error)
	}
	requests := rdb.NewTelegramAgentRequestDAO(db)
	n := &Notifier{configDAO: rdb.NewSysConfigDAO(db)}
	WithClientAccount(n, rdb.NewTelegramClientDAO(db), requests)
	return n, requests, db
}

func TestPeerFloodFromTheAgentSendsThatMessageByTheBot(t *testing.T) {
	server, sent, text := botServer(t)
	n, requests, _ := accountAndBot(t, server.URL)
	go func() {
		for {
			req, _ := requests.Claim(context.Background())
			if req != nil {
				_ = requests.Finish(context.Background(), req.ID, nil, errString("Telegram limited this account (PEER_FLOOD: too many messages)"))
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "wall alert"}))
	assert.EqualValues(t, 1, sent.Load(), "the bot sent it")
	assert.Equal(t, "wall alert", *text)
}

type errString string

func (e errString) Error() string { return string(e) }

func TestWhileTheAccountIsLimitedEveryMessageGoesByTheBotEvenWhenOneWasChosen(t *testing.T) {
	server, sent, _ := botServer(t)
	n, _, db := accountAndBot(t, server.URL)
	require.NoError(t, db.Model(&model.TelegramClientAccount{}).Where("phone_number = ?", "+100").
		Updates(map[string]any{"send_blocked_until": time.Now().Add(time.Hour), "send_blocked_reason": "PEER_FLOOD"}).Error)
	var acc model.TelegramClientAccount
	require.NoError(t, db.Where("phone_number = ?", "+100").First(&acc).Error)

	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "automatic"}))
	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "chosen account", SendAccountID: acc.ID}))
	assert.EqualValues(t, 2, sent.Load(), "both went by the bot")
	var queued int64
	require.NoError(t, db.Model(&model.TelegramAgentRequest{}).Count(&queued).Error)
	assert.Zero(t, queued, "the limited account was not asked at all")
}

func TestBotOnlyModeNeverUsesAUserAccount(t *testing.T) {
	server, sent, _ := botServer(t)
	n, _, db := accountAndBot(t, server.URL)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: ConfigSender, ConfigValue: SenderBot, ConfigName: "sender"}).Error)

	require.NoError(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}))
	assert.EqualValues(t, 1, sent.Load())
	var queued int64
	require.NoError(t, db.Model(&model.TelegramAgentRequest{}).Count(&queued).Error)
	assert.Zero(t, queued)
}

func TestAnOrdinaryAccountFailureIsNotHiddenByTheBot(t *testing.T) {
	server, sent, _ := botServer(t)
	n, requests, _ := accountAndBot(t, server.URL)
	go func() {
		for {
			req, _ := requests.Claim(context.Background())
			if req != nil {
				_ = requests.Finish(context.Background(), req.ID, nil, errString("USER_PRIVACY_RESTRICTED"))
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	assert.ErrorContains(t, n.Send(context.Background(), "123456789", notify.Message{Content: "hi"}), "USER_PRIVACY_RESTRICTED")
	assert.Zero(t, sent.Load(), "only a limit by Telegram (or silence) hands the message to the bot")
}

func TestIsLimited(t *testing.T) {
	assert.True(t, isLimited(errString("send through Telegram account #1: Telegram limited this account (PEER_FLOOD: x)")))
	assert.True(t, isLimited(errString("account #1 cannot send until 2026-10-09 14:00: PEER_FLOOD")))
	assert.False(t, isLimited(errString("USER_PRIVACY_RESTRICTED")))
	assert.False(t, isLimited(nil))
}
