package webhook

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookNotifierSend(t *testing.T) {
	var received WebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewNotifier(2 * time.Second)
	assert.Equal(t, "webhook", notifier.Channel())

	recipients, err := notifier.Recipients(context.Background())
	require.NoError(t, err)
	assert.Empty(t, recipients)

	err = notifier.Send(context.Background(), server.URL, notify.Message{
		Title:   "System Alert",
		Content: "Exchange degraded",
	})
	require.NoError(t, err)
	assert.Equal(t, "System Alert", received.Title)
	assert.Equal(t, "Exchange degraded", received.Content)
	assert.Equal(t, server.URL, received.Recipient)
}

func TestWebhookNotifierErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	notifier := NewNotifier(2 * time.Second)
	err := notifier.Send(context.Background(), server.URL, notify.Message{
		Title: "Alert",
	})
	assert.ErrorContains(t, err, "status: 500")
}
