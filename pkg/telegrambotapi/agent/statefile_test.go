package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateFileSaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kubeforecast-agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	path := filepath.Join(tmpDir, "sub", "session.json")

	// 1. Initial load should fail (file doesn't exist)
	s, err := loadSession(path)
	if err == nil || s != nil {
		t.Fatalf("Expected error loading non-existent session, got %v, %v", s, err)
	}

	// 2. Save session
	testSession := &session{
		NodeID: 42,
		Token:  "test-bearer-token-12345",
	}
	if err := saveSession(path, testSession); err != nil {
		t.Fatalf("saveSession failed: %v", err)
	}

	// 3. Load session and verify
	loaded, err := loadSession(path)
	if err != nil {
		t.Fatalf("loadSession failed: %v", err)
	}
	if loaded.NodeID != 42 || loaded.Token != "test-bearer-token-12345" {
		t.Fatalf("Session mismatch: got %+v, want %+v", loaded, testSession)
	}

	// 4. Overwrite and verify atomic update
	testSession.NodeID = 100
	testSession.Token = "new-token-999"
	if err := saveSession(path, testSession); err != nil {
		t.Fatalf("saveSession overwrite failed: %v", err)
	}

	loaded2, err := loadSession(path)
	if err != nil {
		t.Fatalf("loadSession reload failed: %v", err)
	}
	if loaded2.NodeID != 100 || loaded2.Token != "new-token-999" {
		t.Fatalf("Reloaded session mismatch: got %+v", loaded2)
	}
}

func TestSaveSessionEmptyPathIsNoop(t *testing.T) {
	if err := saveSession("", &session{NodeID: 1, Token: "abc"}); err != nil {
		t.Fatalf("Expected noop for empty path, got error: %v", err)
	}
}
