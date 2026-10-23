package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withMockAPI temporarily points senderHTTPClient's requests at a local test
// server standing in for api.telegram.org, by intercepting via a custom
// RoundTripper that rewrites the host — this keeps SendMessage/GetBotUsername
// exercising their real request-building and response-parsing logic without
// hitting the network.
func withMockAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	original := senderHTTPClient
	senderHTTPClient = &http.Client{
		Transport: rewriteHostTransport{target: srv.URL},
	}
	t.Cleanup(func() { senderHTTPClient = original })
	return srv
}

type rewriteHostTransport struct{ target string }

func (r rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := r.target + req.URL.Path
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestSendMessage_Success(t *testing.T) {
	var captured sendMessageRequest
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/bottest-token/sendMessage") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{OK: true})
	})

	err := SendMessage(context.Background(), "test-token", 12345, "hello", WithInlineButton("Open", "https://example.com"))
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if captured.ChatID != float64(12345) || captured.Text != "hello" || captured.ParseMode != "HTML" {
		t.Fatalf("unexpected request body: %#v", captured)
	}
	if captured.ReplyMarkup == nil {
		t.Fatal("expected inline button reply markup to be set")
	}
}

func TestSendMessage_TelegramErrorSurfaced(t *testing.T) {
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{OK: false, Description: "chat not found"})
	})

	err := SendMessage(context.Background(), "test-token", 999, "hello")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected telegram error to be surfaced, got: %v", err)
	}
}

func TestSendMessage_EmptyTokenRejectedWithoutNetworkCall(t *testing.T) {
	err := SendMessage(context.Background(), "", 1, "hello")
	if err == nil {
		t.Fatal("expected error for empty bot token")
	}
}

func TestGetBotUsername_Success(t *testing.T) {
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/bottest-token/getMe") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 1, "username": "kubeforecast_bot", "first_name": "KubeForecast"},
		})
	})

	username, err := GetBotUsername(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("GetBotUsername failed: %v", err)
	}
	if username != "kubeforecast_bot" {
		t.Fatalf("expected username kubeforecast_bot, got %q", username)
	}
}

func TestGetBotUsername_InvalidTokenSurfacesError(t *testing.T) {
	withMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiResponse{OK: false, Description: "Unauthorized"})
	})

	_, err := GetBotUsername(context.Background(), "bad-token")
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("expected unauthorized error, got: %v", err)
	}
}

func TestSendMessageToChatUsesAnUsernameAsTheChatID(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	if err := SendMessageToChat(context.Background(), "test-token", "@some_channel", "hello", WithBaseURL(server.URL)); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "@some_channel" || got["text"] != "hello" {
		t.Fatalf("unexpected body %#v", got)
	}
	if err := SendMessageToChat(context.Background(), "", "@x", "hi"); err == nil {
		t.Fatal("an empty token must be rejected")
	}
	d := NewChatDriver("test-token", "", server.URL)
	if err := d.SendToChat(context.Background(), "@other", "hey", nil); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "@other" {
		t.Fatalf("driver did not pass the username: %#v", got)
	}
}
