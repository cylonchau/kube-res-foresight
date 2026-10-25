package notify

import (
	"context"
	"errors"
	"testing"
)

type fakeNotifier struct {
	channel    string
	recipients []string
	recipErr   error
	sent       []string
	sendErr    error
}

func (f *fakeNotifier) Channel() string { return f.channel }

func (f *fakeNotifier) Recipients(ctx context.Context) ([]string, error) {
	return f.recipients, f.recipErr
}

func (f *fakeNotifier) Send(ctx context.Context, recipient string, msg Message) error {
	f.sent = append(f.sent, recipient)
	return f.sendErr
}

func withCleanRegistry(t *testing.T) {
	t.Helper()
	saved := registry
	registry = map[string]Notifier{}
	t.Cleanup(func() { registry = saved })
}

func TestBroadcastEnqueuesOnePerRecipientPerChannel(t *testing.T) {
	withCleanRegistry(t)
	a := &fakeNotifier{channel: "chan-a", recipients: []string{"u1", "u2"}}
	b := &fakeNotifier{channel: "chan-b", recipients: []string{"u3"}}
	Register(a)
	Register(b)

	var got []struct{ channel, recipient string }
	enqueue := func(ctx context.Context, channel, recipient string, msg Message) error {
		got = append(got, struct{ channel, recipient string }{channel, recipient})
		return nil
	}

	if err := Broadcast(context.Background(), enqueue, Message{Content: "hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 enqueue calls, got %d: %#v", len(got), got)
	}
}

func TestBroadcastContinuesPastOneChannelFailure(t *testing.T) {
	withCleanRegistry(t)
	Register(&fakeNotifier{channel: "bad", recipErr: errors.New("boom")})
	Register(&fakeNotifier{channel: "good", recipients: []string{"u1"}})

	var enqueued int
	enqueue := func(ctx context.Context, channel, recipient string, msg Message) error {
		enqueued++
		return nil
	}

	err := Broadcast(context.Background(), enqueue, Message{Content: "hi"})
	if err == nil {
		t.Fatal("expected error from failing channel to propagate")
	}
	if enqueued != 1 {
		t.Fatalf("expected the healthy channel to still be enqueued, got %d calls", enqueued)
	}
}

func TestBroadcastNoRegisteredChannelsIsNoop(t *testing.T) {
	withCleanRegistry(t)
	called := false
	enqueue := func(ctx context.Context, channel, recipient string, msg Message) error {
		called = true
		return nil
	}
	if err := Broadcast(context.Background(), enqueue, Message{Content: "hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("enqueue should not be called when no channel is registered")
	}
}

func TestRegisterGet(t *testing.T) {
	withCleanRegistry(t)
	n := &fakeNotifier{channel: "webhook"}
	Register(n)

	got, ok := Get("webhook")
	if !ok || got != n {
		t.Fatalf("expected to get back the registered notifier, got %#v ok=%v", got, ok)
	}
	if _, ok := Get("missing"); ok {
		t.Fatal("expected Get to report not-found for an unregistered channel")
	}
}
