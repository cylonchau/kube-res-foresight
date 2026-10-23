// pkg/utils/crypto.go
package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
)

// Development fallback; product naming does not change the existing key bytes.
const defaultEncryptionKey = "4PT95Xag+qb+DKjpQiZv/j91tNSf0n10adt2QmjldPA="

// getEncryptionKey returns a 32-byte key from KUBEFORECAST_SECRET_KEY for AES-256
func getEncryptionKey() ([]byte, error) {
	key := strings.TrimSpace(os.Getenv("KUBEFORECAST_SECRET_KEY"))
	if key == "" {
		key = defaultEncryptionKey
	}
	if len(key) < 32 {
		return nil, errors.New("KUBEFORECAST_SECRET_KEY must be at least 32 characters")
	}
	return []byte(key[:32]), nil
}

// EncryptValue encrypts a string using AES-256-GCM
// It returns a base64 encoded ciphertext including the nonce
func EncryptValue(plaintext string) (string, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	// Pack nonce + ciphertext together
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptValue decrypts a base64 encoded AES-256-GCM ciphertext
func DecryptValue(encoded string) (string, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("decryption failed")
	}
	return string(plaintext), nil
}

func MaskSecretValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	if len(runes) <= 8 {
		return string(runes[:1]) + strings.Repeat("*", len(runes)-2) + string(runes[len(runes)-1:])
	}
	return string(runes[:4]) + strings.Repeat("*", 6) + string(runes[len(runes)-4:])
}
