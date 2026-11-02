// Package voice implements a voice call notification channel for urgent/escalated alerts.
package voice

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"strings"
	"sync"
	"time"
)

const ChannelVoice = "voice"

type CallRecord struct {
	Recipient string
	Message   string
	CalledAt  time.Time
}

type Notifier struct {
	mu          sync.Mutex
	callHistory []CallRecord
	dialer      func(ctx context.Context, recipient, message string) error
}

func NewNotifier(dialer func(ctx context.Context, recipient, message string) error) *Notifier {
	return &Notifier{
		dialer: dialer,
	}
}

func (n *Notifier) Channel() string {
	return ChannelVoice
}

func (n *Notifier) Recipients(_ context.Context) ([]string, error) {
	return []string{}, nil
}

func (n *Notifier) Send(ctx context.Context, recipientPhoneNumber string, msg notify.Message) error {
	phone := strings.TrimSpace(recipientPhoneNumber)
	if phone == "" {
		return errors.New("recipient phone number is required")
	}

	synthesizedText := fmt.Sprintf("Emergency alert: %s. Details: %s", msg.Title, msg.Content)

	logger.Warn("Dispatching urgent voice alert call",
		logger.String("channel", ChannelVoice),
		logger.String("recipient", phone),
		logger.String("alert_title", msg.Title),
	)

	n.mu.Lock()
	n.callHistory = append(n.callHistory, CallRecord{
		Recipient: phone,
		Message:   synthesizedText,
		CalledAt:  time.Now().UTC(),
	})
	n.mu.Unlock()

	if n.dialer != nil {
		return n.dialer(ctx, phone, synthesizedText)
	}
	return nil
}

func (n *Notifier) History() []CallRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	copied := make([]CallRecord, len(n.callHistory))
	copy(copied, n.callHistory)
	return copied
}
