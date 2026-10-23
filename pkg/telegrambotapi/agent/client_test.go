package agent

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRegisterAndHeartbeat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/bot-api-nodes/register":
			resp := map[string]interface{}{
				"code": 10000,
				"msg":  "ok",
				"data": apis.TelegramBotAPINodeSession{
					NodeID: 10,
					Token:  "issued-token-123",
					Status: "online",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/bot-api-nodes/10/heartbeat":
			if r.Header.Get("Authorization") != "Bearer issued-token-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			resp := map[string]interface{}{
				"code": 10000,
				"msg":  "ok",
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/bot-api-nodes/10/config/snapshot":
			if r.Header.Get("Authorization") != "Bearer issued-token-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			resp := map[string]interface{}{
				"code": 10000,
				"msg":  "ok",
				"data": apis.TelegramBotAPINodeConfigSnapshot{
					Version:        "v1",
					Port:           8081,
					APIID:          "12345",
					APIHash:        "abcdef",
					LocalMode:      true,
					DesiredRunning: true,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "")
	ctx := context.Background()

	// 1. Test Register
	session, err := client.Register(ctx, apis.TelegramBotAPINodeRegisterReq{
		Name:  "bot-node-1",
		Token: "initial-token",
		TelegramBotAPINodeSystemInfo: apis.TelegramBotAPINodeSystemInfo{
			Hostname: "bot-node-1",
		},
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if session.NodeID != 10 || session.Token != "issued-token-123" {
		t.Fatalf("Unexpected session: %+v", session)
	}

	client.SetToken(session.Token)

	// 2. Test Heartbeat
	err = client.Heartbeat(ctx, 10, apis.TelegramBotAPINodeHeartbeatReq{
		ObservedRunning: true,
	})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	// 3. Test FetchConfigSnapshot
	snapshot, err := client.FetchConfigSnapshot(ctx, 10)
	if err != nil {
		t.Fatalf("FetchConfigSnapshot failed: %v", err)
	}
	if snapshot.Port != 8081 || snapshot.APIID != "12345" || !snapshot.DesiredRunning {
		t.Fatalf("Unexpected snapshot: %+v", snapshot)
	}
}
