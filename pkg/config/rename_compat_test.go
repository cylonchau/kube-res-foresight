package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestForesightDefaultKeepsThePreviousDatabase(t *testing.T) {
	dir := t.TempDir()
	previous := filepath.Join(dir, "kube-resource-fcst.db")
	current := filepath.Join(dir, "kube-res-foresight.db")
	if err := os.WriteFile(previous, []byte("existing database"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("app_name: kube-resource-fcst\ndatabase_driver: sqlite\nsqlite:\n  file: "+filepath.ToSlash(current)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppName != "kube-res-foresight" || cfg.SQLite.File != previous {
		t.Fatalf("name=%q file=%q", cfg.AppName, cfg.SQLite.File)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatal("must not create or move a database during configuration loading")
	}
	if err := os.WriteFile(current, []byte("new database"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || filepath.Clean(cfg.SQLite.File) != filepath.Clean(current) {
		t.Fatalf("explicit existing new database must win: %v", err)
	}
}

func TestForesightConfigReadsThePreviousEnvironmentPrefix(t *testing.T) {
	const current = "KUBE_RES_FORESIGHT_SERVER_JWT_SECRET"
	t.Setenv(current, "")
	if err := os.Unsetenv(current); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_RESOURCE_FCST_SERVER_JWT_SECRET", "existing-signing-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("app_name: kube-res-foresight\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Server.JWTSecret != "existing-signing-key" {
		t.Fatalf("old JWT key not read: %v", err)
	}
}
