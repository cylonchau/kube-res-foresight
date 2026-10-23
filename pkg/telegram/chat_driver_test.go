package telegram

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"net/http"
	"testing"
)

// capturedSendRequest mirrors sendMessageRequest but keeps ReplyMarkup as
// raw JSON so a test can decode it into the concrete inlineKeyboardMarkup
// shape instead of a generic map, to assert on individual button fields.
type capturedSendRequest struct {
	ChatID      int64           `json:"chat_id"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode"`
	ReplyMarkup json.RawMessage `json:"reply_markup"`
}

func captureOneSend(t *testing.T) *capturedSendRequest {
	t.Helper()
	var captured capturedSendRequest
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{OK: true})
	})
	return &captured
}

func TestChatDriverSendMessage_ZeroRecipientIsNoOp(t *testing.T) {
	called := false
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{OK: true})
	})

	driver := NewChatDriver("test-token", "https://miniapp.example", "")
	if err := driver.SendMessage(context.Background(), 0, "hello", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("SendMessage must not call the Bot API for recipientID 0")
	}
}

func TestChatDriverSendMessage_NoButtons(t *testing.T) {
	captured := captureOneSend(t)
	driver := NewChatDriver("test-token", "https://miniapp.example", "")

	if err := driver.SendMessage(context.Background(), 12345, "hello", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.ChatID != 12345 || captured.Text != "hello" {
		t.Fatalf("unexpected request: %#v", captured)
	}
	if string(captured.ReplyMarkup) != "" && string(captured.ReplyMarkup) != "null" {
		t.Fatalf("expected no reply markup, got: %s", captured.ReplyMarkup)
	}
}

func TestChatDriverSendMessage_WebAppButtonFallsBackToMiniAppURL(t *testing.T) {
	captured := captureOneSend(t)
	driver := NewChatDriver("test-token", "https://miniapp.example", "")

	err := driver.SendMessage(context.Background(), 12345, "hello", []workflow.Button{
		{Label: "Open", ActionType: "webapp", Target: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var markup inlineKeyboardMarkup
	if err := json.Unmarshal(captured.ReplyMarkup, &markup); err != nil {
		t.Fatalf("failed to decode reply markup: %v", err)
	}
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("expected exactly one button, got: %#v", markup.InlineKeyboard)
	}
	btn := markup.InlineKeyboard[0][0]
	if btn.WebApp == nil || btn.WebApp.URL != "https://miniapp.example" {
		t.Fatalf("expected webapp button to fall back to the default mini app URL, got: %#v", btn)
	}
}

func TestChatDriverSendMessage_WebAppRelativeRouteIsPrefixed(t *testing.T) {
	captured := captureOneSend(t)
	driver := NewChatDriver("test-token", "https://miniapp.example", "")

	err := driver.SendMessage(context.Background(), 12345, "hello", []workflow.Button{
		{Label: "Open", ActionType: "webapp", Target: "#/workflow/instances/42"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var markup inlineKeyboardMarkup
	if err := json.Unmarshal(captured.ReplyMarkup, &markup); err != nil {
		t.Fatalf("failed to decode reply markup: %v", err)
	}
	btn := markup.InlineKeyboard[0][0]
	if btn.WebApp == nil || btn.WebApp.URL != "https://miniapp.example#/workflow/instances/42" {
		t.Fatalf("expected relative route to be prefixed with the mini app URL, got: %#v", btn)
	}
}

// The regression this guards: with notify.telegram_bot.miniapp_url unset,
// a workflow webapp button resolved to a bare relative route. Telegram rejects an invalid
// WebApp URL by failing the entire sendMessage, so the OPS role silently
// never received the notification at all.
func TestChatDriverSendMessage_WebAppRelativeRouteWithoutMiniAppURLKeepsMessage(t *testing.T) {
	captured := captureOneSend(t)
	driver := NewChatDriver("test-token", "", "")

	err := driver.SendMessage(context.Background(), 12345, "action approved", []workflow.Button{
		{Label: "查看实例", ActionType: "webapp", Target: "#/workflow/instances/1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Text != "action approved" {
		t.Fatalf("the message itself must still be sent, got: %#v", captured)
	}
	if string(captured.ReplyMarkup) != "" && string(captured.ReplyMarkup) != "null" {
		t.Fatalf("expected the unresolvable button to be dropped, got markup: %s", captured.ReplyMarkup)
	}
}

func TestChatDriverSendMessage_NonHTTPSWebAppURLIsDropped(t *testing.T) {
	captured := captureOneSend(t)
	// http:// is rejected by Telegram for WebApp buttons, and takes the
	// whole message with it — so it must be dropped here, not passed on.
	driver := NewChatDriver("test-token", "http://miniapp.example", "")

	err := driver.SendMessage(context.Background(), 12345, "hello", []workflow.Button{
		{Label: "Open", ActionType: "webapp", Target: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Text != "hello" {
		t.Fatalf("the message itself must still be sent, got: %#v", captured)
	}
	if string(captured.ReplyMarkup) != "" && string(captured.ReplyMarkup) != "null" {
		t.Fatalf("expected the invalid button to be dropped, got markup: %s", captured.ReplyMarkup)
	}
}

func TestChatDriverSendMessage_CallbackAndURLButtons(t *testing.T) {
	captured := captureOneSend(t)
	driver := NewChatDriver("test-token", "https://miniapp.example", "")

	err := driver.SendMessage(context.Background(), 12345, "hello", []workflow.Button{
		{Label: "Approve", ActionType: "callback", Target: "leader_approve:42"},
		{Label: "Docs", ActionType: "url", Target: "https://example.com"},
		{Label: "Unrecognized", ActionType: "not_a_type", Target: "x"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var markup inlineKeyboardMarkup
	if err := json.Unmarshal(captured.ReplyMarkup, &markup); err != nil {
		t.Fatalf("failed to decode reply markup: %v", err)
	}
	// One row per button (see buildButtonRows), and the unrecognized
	// ActionType must be dropped rather than passed through.
	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 button rows (unrecognized type dropped), got: %#v", markup.InlineKeyboard)
	}
	if markup.InlineKeyboard[0][0].Data != "leader_approve:42" {
		t.Fatalf("expected callback button with Data set, got: %#v", markup.InlineKeyboard[0][0])
	}
	if markup.InlineKeyboard[1][0].URL != "https://example.com" {
		t.Fatalf("expected url button with URL set, got: %#v", markup.InlineKeyboard[1][0])
	}
}
