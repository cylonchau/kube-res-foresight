package utils

import (
	"os"
	"testing"
)

func TestProjectEnvPrefersNewNameAndPreservesLegacyValue(t *testing.T) {
	const current = "KUBE_RESOURCE_FCST_COMPAT_TEST"
	const legacy = "KUBEFORECAST_COMPAT_TEST"
	t.Setenv(current, "")
	if err := os.Unsetenv(current); err != nil {
		t.Fatal(err)
	}
	t.Setenv(legacy, "existing-value")
	if got := ProjectEnv(current); got != "existing-value" {
		t.Fatalf("legacy fallback: %q", got)
	}
	t.Setenv(current, "new-value")
	if got := ProjectEnv(current); got != "new-value" {
		t.Fatalf("new name must win: %q", got)
	}
	t.Setenv(current, "")
	if got := ProjectEnv(current); got != "" {
		t.Fatalf("explicit empty value must not use old value: %q", got)
	}
}

func TestEncryptionKeySurvivesEnvironmentRename(t *testing.T) {
	const key = "12345678901234567890123456789012"
	t.Setenv("KUBE_RESOURCE_FCST_SECRET_KEY", "")
	if err := os.Unsetenv("KUBE_RESOURCE_FCST_SECRET_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBEFORECAST_SECRET_KEY", key)
	ciphertext, err := EncryptValue("stored-datasource-credential")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_RESOURCE_FCST_SECRET_KEY", key)
	plaintext, err := DecryptValue(ciphertext)
	if err != nil || plaintext != "stored-datasource-credential" {
		t.Fatalf("credential unreadable after rename: %v", err)
	}
	t.Setenv("KUBE_RESOURCE_FCST_SECRET_KEY", "different-value-12345678901234567890")
	if _, err := DecryptValue(ciphertext); err == nil {
		t.Fatal("new variable must take precedence over the legacy key")
	}
}
