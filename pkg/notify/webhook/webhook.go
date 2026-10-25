// Package webhook implements a generic HTTP Webhook notification channel.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/notify"
	"net/http"
	"time"
)

const ChannelWebhook = "webhook"

type WebhookPayload struct {
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Recipient string    `json:"recipient"`
	Timestamp time.Time `json:"timestamp"`
}

type Notifier struct {
	client  *http.Client
	timeout time.Duration
}

func NewNotifier(timeout time.Duration) *Notifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Notifier{
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

func (n *Notifier) Channel() string {
	return ChannelWebhook
}

func (n *Notifier) Recipients(_ context.Context) ([]string, error) {
	return []string{}, nil
}

func (n *Notifier) Send(ctx context.Context, recipientWebhookURL string, msg notify.Message) error {
	if recipientWebhookURL == "" {
		return errors.New("webhook url recipient is required")
	}

	payload := WebhookPayload{
		Title:     msg.Title,
		Content:   msg.Content,
		Recipient: recipientWebhookURL,
		Timestamp: time.Now().UTC(),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, recipientWebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("execute webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook responded with status: %d", resp.StatusCode)
	}
	return nil
}
