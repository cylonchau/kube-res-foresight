package voice

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestVoiceNotifierSend(t *testing.T) {
	var dialedRecipient, dialedMessage string
	notifier := NewNotifier(func(_ context.Context, recipient, message string) error {
		dialedRecipient = recipient
		dialedMessage = message
		return nil
	})

	assert.Equal(t, "voice", notifier.Channel())

	recipients, err := notifier.Recipients(context.Background())
	require.NoError(t, err)
	assert.Empty(t, recipients)

	err = notifier.Send(context.Background(), "+1234567890", notify.Message{
		Title:   "Kill Switch Triggered",
		Content: "All positions halted",
	})
	require.NoError(t, err)
	assert.Equal(t, "+1234567890", dialedRecipient)
	assert.Contains(t, dialedMessage, "Emergency alert: Kill Switch Triggered")

	history := notifier.History()
	require.Len(t, history, 1)
	assert.Equal(t, "+1234567890", history[0].Recipient)
}

func TestVoiceNotifierRejectsEmptyRecipient(t *testing.T) {
	notifier := NewNotifier(nil)
	err := notifier.Send(context.Background(), "", notify.Message{Title: "Alert"})
	assert.ErrorContains(t, err, "phone number is required")
}

func TestVoiceNotifierDialerError(t *testing.T) {
	notifier := NewNotifier(func(_ context.Context, _, _ string) error {
		return errors.New("line busy")
	})
	err := notifier.Send(context.Background(), "+1234", notify.Message{Title: "Alert"})
	assert.ErrorContains(t, err, "line busy")
}
