package moduleruntime

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWaitStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, "test") }()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("module did not stop after cancellation")
	}
}
