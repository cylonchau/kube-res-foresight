package app

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPServerStartsAndShutsDownWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("app_name: kubeforecast-test\nserver:\n  port: 0\n"), 0600))
	_, err := config.Load(path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewHTTPServer(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}
