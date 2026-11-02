package utils

import (
	"os"
	"testing"
)

func TestForesightEnvironmentKeepsBothPreviousNames(t *testing.T) {
	const current = "KUBE_RES_FORESIGHT_RENAME_TEST"
	t.Setenv(current, "")
	if err := os.Unsetenv(current); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBEFORECAST_RENAME_TEST", "oldest")
	t.Setenv("KUBE_RESOURCE_FCST_RENAME_TEST", "previous")
	if got := ProjectEnv(current); got != "previous" {
		t.Fatalf("previous prefix: %q", got)
	}
	t.Setenv(current, "current")
	if got := ProjectEnv(current); got != "current" {
		t.Fatalf("current prefix: %q", got)
	}
	t.Setenv(current, "")
	if got := ProjectEnv(current); got != "" {
		t.Fatalf("explicit empty must win: %q", got)
	}
}

func TestForesightReadsTheExistingCredentialEncryptionKey(t *testing.T) {
	const current = "KUBE_RES_FORESIGHT_SECRET_KEY"
	const old = "KUBE_RESOURCE_FCST_SECRET_KEY"
	const key = "12345678901234567890123456789012"
	t.Setenv(current, "")
	if err := os.Unsetenv(current); err != nil {
		t.Fatal(err)
	}
	t.Setenv(old, key)
	ciphertext, err := EncryptValue("existing-stored-credential")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(current, key)
	plain, err := DecryptValue(ciphertext)
	if err != nil || plain != "existing-stored-credential" {
		t.Fatalf("existing key unreadable: %v", err)
	}
}
