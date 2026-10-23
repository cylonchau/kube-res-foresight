package telegram

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func setupBootstrapDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestBootstrap_WiresEngineWithoutStartingDisabledBot is the core guarantee
// of Bootstrap: even with the interactive listener switched off (no
// SysConfig rows at all here, so enabled=false), the outbound Sender and
// this package's CommandHandlers must still be installed on the engine —
// pipeline notifications and the admin API depend on them regardless of
// whether the long-poller runs.
func TestBootstrap_WiresEngineWithoutStartingDisabledBot(t *testing.T) {
	engine := workflow.NewEngine(nil, nil, nil, nil)

	done := make(chan struct{})
	go func() {
		Bootstrap(context.Background(), setupBootstrapDB(t), engine)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Bootstrap did not return promptly; the disabled bot must not attempt a network call")
	}

	names := engine.RegisteredCommandHandlerNames()
	found := false
	for _, n := range names {
		if n == "confirm_bind" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Bootstrap to register this package's command handlers, got: %v", names)
	}
}

func TestBootstrap_NilDBIsNoOp(t *testing.T) {
	engine := workflow.NewEngine(nil, nil, nil, nil)
	Bootstrap(context.Background(), nil, engine)

	if len(engine.RegisteredCommandHandlerNames()) != 0 {
		t.Fatal("expected Bootstrap to no-op entirely with a nil DB")
	}
}

func TestBootstrap_NilEngineIsNoOp(t *testing.T) {
	// Must not panic dereferencing the nil engine.
	Bootstrap(context.Background(), setupBootstrapDB(t), nil)
}
