package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/cylonchau/kubeforecast/pkg/notify/template"
	botTelegram "github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setupTelegramNotifierDB seeds just enough SysConfig rows for Notifier.Send's
// own lookups (enabled flag, bot token, mini app URL, and — the key trick
// that lets this test exercise the real HTTP send path without touching the
// network — an API base URL pointed at a local httptest.Server standing in
// for api.telegram.org).
func setupTelegramNotifierDB(t *testing.T, apiBaseURL string) *rdb.SysConfigDAO {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.SysConfig{
		{ConfigKey: botTelegram.ConfigBotEnabled, ConfigValue: "true", ConfigName: "enabled"},
		{ConfigKey: botTelegram.ConfigBotToken, ConfigValue: "test-token", ConfigName: "token"},
		{ConfigKey: botTelegram.ConfigBotMiniAppURL, ConfigValue: "https://miniapp.example", ConfigName: "miniapp"},
		{ConfigKey: botTelegram.ConfigBotAPIBaseURL, ConfigValue: apiBaseURL, ConfigName: "api base url"},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return rdb.NewSysConfigDAO(db)
}

// TestTelegramNotifierSendDeliversThroughChatDriver is an end-to-end proof
// that Notifier.Send doesn't build its own Telegram send request — it goes
// through pkg/telegram.TelegramChatDriver, the same Sender every other
// outbound Telegram message (workflow pipeline notifications,
// SendEmergencyAlert) uses. The request is redirected to a local
// httptest.Server via ConfigBotAPIBaseURL, so this exercises the real
// HTTP-building/button-conversion path without any network access.
func TestTelegramNotifierSendDeliversThroughChatDriver(t *testing.T) {
	type captured struct {
		ChatID      int64           `json:"chat_id"`
		Text        string          `json:"text"`
		ReplyMarkup json.RawMessage `json:"reply_markup"`
	}
	var got captured
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	configDAO := setupTelegramNotifierDB(t, server.URL)
	n := &Notifier{configDAO: configDAO}

	err := n.Send(context.Background(), "555", notify.Message{
		Content: "hello from broadcast",
		Buttons: []template.Button{
			{Label: "Open", ActionType: template.ActionTypeWebApp, ActionTarget: ""},
		},
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if gotPath != "/bottest-token/sendMessage" {
		t.Fatalf("expected the request to hit sendMessage with the configured token, got path: %s", gotPath)
	}
	if got.ChatID != 555 || got.Text != "hello from broadcast" {
		t.Fatalf("unexpected request body: %#v", got)
	}
	if len(got.ReplyMarkup) == 0 || string(got.ReplyMarkup) == "null" {
		t.Fatal("expected the webapp button (falling back to the configured mini app URL) to produce a reply markup")
	}
}

// TestTelegramNotifierSendWithoutABotFails: when no bot is enabled Send must not
// call the Bot API, and it must say so. Returning nil here made a forwarded
// message look delivered (status SENT) although nothing was sent.
func TestTelegramNotifierSendWithoutABotFails(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}); err != nil {
		t.Fatal(err)
	}
	// No ConfigBotEnabled row at all — GetBool's fallback (false) applies.
	n := &Notifier{configDAO: rdb.NewSysConfigDAO(db)}

	if err := n.Send(context.Background(), "555", notify.Message{Content: "hello"}); !errors.Is(err, ErrNoBot) {
		t.Fatalf("expected ErrNoBot when no bot is enabled, got: %v", err)
	}
	if called {
		t.Fatal("Send must not call the Bot API when notify.telegram_bot.enabled is not true")
	}
}

// TestTelegramNotifierSendsThroughTheFirstEnabledBot proves the notify
// provider follows the telegram_bot table (Bot management) and only falls
// back to the legacy SysConfig bot when no bot row is enabled.
func TestTelegramNotifierSendsThroughTheFirstEnabledBot(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.TelegramBot{}, &model.TelegramBinding{}, &model.User{}); err != nil {
		t.Fatal(err)
	}
	bots := rdb.NewTelegramBotDAO(db)
	n := &Notifier{configDAO: rdb.NewSysConfigDAO(db), bindingDAO: rdb.NewTelegramBindingDAO(db), bots: bots}

	// Nothing configured anywhere: no recipients, and sending is a no-op.
	recipients, err := n.Recipients(context.Background())
	if err != nil || len(recipients) != 0 {
		t.Fatalf("expected no recipients without a bot, got %v, %v", recipients, err)
	}
	if err := n.Send(context.Background(), "555", notify.Message{Content: "hi"}); !errors.Is(err, ErrNoBot) || gotPath != "" {
		t.Fatalf("expected ErrNoBot and no request, got err=%v path=%q", err, gotPath)
	}

	name, token := "primary", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
	if _, err := bots.Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token, APIBaseURL: &server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := n.Send(context.Background(), "555", notify.Message{Content: "hi"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if gotPath != "/bot"+token+"/sendMessage" {
		t.Fatalf("expected the bot from Bot management to send, got path %q", gotPath)
	}
	if err := n.Send(context.Background(), "not-a-chat", notify.Message{Content: "hi"}); err == nil {
		t.Fatal("expected an invalid recipient to be rejected")
	}

	// A bound user now counts as a recipient because a bot is enabled.
	user := model.User{Username: "ops", PasswordHash: "x", Status: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TelegramBinding{UserID: user.ID, TelegramUserID: 555, Status: true}).Error; err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	recipients, err = n.Recipients(context.Background())
	if err != nil || len(recipients) != 1 || recipients[0] != "555" {
		t.Fatalf("expected the bound user, got %v, %v", recipients, err)
	}
}

// TestTelegramNotifierAcceptsAPublicChannelUsernameButNotJunk covers the recipient
// forms a forward rule can name: a numeric chat id or an @username.
func TestTelegramNotifierAcceptsAPublicChannelUsernameButNotJunk(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.TelegramBot{}); err != nil {
		t.Fatal(err)
	}
	bots := rdb.NewTelegramBotDAO(db)
	name, token := "primary", "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
	if _, err := bots.Create(context.Background(), rdb.TelegramBotInput{Name: &name, Token: &token, APIBaseURL: &server.URL}); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{configDAO: rdb.NewSysConfigDAO(db), bots: bots}

	if err := n.Send(context.Background(), " @kubeforecast_news ", notify.Message{Content: "to a channel"}); err != nil {
		t.Fatalf("Send to @username failed: %v", err)
	}
	if body["chat_id"] != "@kubeforecast_news" {
		t.Fatalf("expected the @username as chat_id, got %#v", body["chat_id"])
	}
	if err := n.Send(context.Background(), "555", notify.Message{Content: "to a user"}); err != nil {
		t.Fatalf("Send to a chat id failed: %v", err)
	}
	if body["chat_id"] != float64(555) {
		t.Fatalf("expected the numeric chat_id, got %#v", body["chat_id"])
	}
	for _, bad := range []string{"", "@", "some user", "12ab"} {
		if err := n.Send(context.Background(), bad, notify.Message{Content: "x"}); !errors.Is(err, ErrBadRecipient) {
			t.Fatalf("recipient %q: expected ErrBadRecipient, got %v", bad, err)
		}
	}
}
