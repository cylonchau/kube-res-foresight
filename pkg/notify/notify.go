// Package notify is the generic notification-channel abstraction. Concrete
// channels (webhook and voice today, potentially email later)
// implement Notifier and register themselves here; callers that want to
// notify people never talk to a specific channel — they call Broadcast,
// which asks every registered channel for its current recipients and
// enqueues one delivery job per recipient. Adding a new channel means
// writing one Notifier implementation and registering it; it does not
// require touching any code that calls Broadcast.
package notify

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/template"
)

// Message is a channel-agnostic notification payload. Buttons is a flat
// list of interactive buttons (see pkg/notify/template); a channel renders each on its own row (or ignores
// them entirely if it can't render buttons at all).
type Message struct {
	Title   string
	Content string
	Buttons []template.Button
}

// Notifier is implemented by one notification channel.
type Notifier interface {
	// Channel returns the channel's stable identifier (e.g. "webhook"),
	// used to route notify jobs back to the right Notifier and as the
	// job payload's channel field.
	Channel() string

	// Recipients returns who currently wants to receive broadcasts on this
	// channel (e.g. every configured webhook endpoint). An empty
	// slice (not an error) means the channel is configured but has nobody
	// to notify right now.
	Recipients(ctx context.Context) ([]string, error)

	// Send delivers msg to a single recipient returned by Recipients.
	Send(ctx context.Context, recipient string, msg Message) error
}

var registry = map[string]Notifier{}

// Register adds a channel implementation. Call once at startup (see
// pkg/job/tasks.RegisterAll), before any Broadcast/Get call.
func Register(n Notifier) {
	registry[n.Channel()] = n
}

// Get looks up a registered channel by its Channel() identifier.
func Get(channel string) (Notifier, bool) {
	n, ok := registry[channel]
	return n, ok
}

// EnqueueFunc persists one notify job for a single (channel, recipient)
// delivery. It's a plain function type rather than a concrete DAO
// dependency, so this package stays free of the database layer and channel adapters can live anywhere.
type EnqueueFunc func(ctx context.Context, channel, recipient string, msg Message) error

// Broadcast asks every registered channel for its current recipients and
// enqueues one job per (channel, recipient) via enqueue, instead of sending
// synchronously in a loop. This means delivery goes through the job
// engine's normal retry/backoff and per-job failure isolation — one bad
// recipient (blocked bot, invalid address) can't affect any other, and a
// transient failure gets retried instead of silently dropped.
func Broadcast(ctx context.Context, enqueue EnqueueFunc, msg Message) error {
	if len(registry) == 0 {
		return nil
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for _, n := range registry {
		recipients, err := n.Recipients(ctx)
		if err != nil {
			note(fmt.Errorf("list recipients for channel %s: %w", n.Channel(), err))
			continue
		}
		for _, recipient := range recipients {
			note(enqueue(ctx, n.Channel(), recipient, msg))
		}
	}
	return firstErr
}
