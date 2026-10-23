package all

import (
	"bytes"
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandRejectsUnsupportedDatabaseBeforeStartingModules(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("app_name: phase1-test\n"), 0o600))
	command := NewCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--config", configPath, "--sql-driver", "oracle"})
	require.EqualError(t, command.Execute(), `unsupported sql driver "oracle"`)
}

func TestRunServicesStopsSiblingsAfterFailure(t *testing.T) {
	expected := errors.New("failed")
	stopped := make(chan struct{})
	err := runServices(context.Background(), []service{
		{name: "failing", run: func(context.Context) error { return expected }},
		{name: "waiting", run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		}},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, expected)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("sibling module was not cancelled")
	}
}

func TestRunServicesStopsOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runServices(ctx, []service{{name: "waiting", run: func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}}})
	require.NoError(t, err)
}
