package utils

import (
	"os"
	"testing"
)

func TestCrypto(t *testing.T) {
	os.Setenv("KUBEFORECAST_SECRET_KEY", "12345678901234567890123456789012") // 32 bytes
	defer os.Unsetenv("KUBEFORECAST_SECRET_KEY")

	plaintext := "hello-world-secret-123"

	encrypted, err := EncryptValue(plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	if encrypted == "" {
		t.Fatal("encrypted value is empty")
	}

	if encrypted == plaintext {
		t.Fatal("encrypted value matches plaintext")
	}

	decrypted, err := DecryptValue(encrypted)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Fatalf("decrypted value mismatch: got %s, want %s", decrypted, plaintext)
	}
}

func TestCryptoInvalidKey(t *testing.T) {
	os.Setenv("KUBEFORECAST_SECRET_KEY", "too-short")
	defer os.Unsetenv("KUBEFORECAST_SECRET_KEY")

	_, err := EncryptValue("test")
	if err == nil {
		t.Fatal("should fail with short key")
	}
}

func TestCryptoUsesDefaultKeyWhenEnvMissing(t *testing.T) {
	os.Unsetenv("KUBEFORECAST_SECRET_KEY")

	plaintext := "default-key-secret"
	encrypted, err := EncryptValue(plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt with default key: %v", err)
	}
	decrypted, err := DecryptValue(encrypted)
	if err != nil {
		t.Fatalf("failed to decrypt with default key: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("decrypted value mismatch: got %s, want %s", decrypted, plaintext)
	}
}
