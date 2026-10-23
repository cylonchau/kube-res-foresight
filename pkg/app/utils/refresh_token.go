package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

func GenerateRefreshToken() (raw, hash string, err error) {
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(value)
	return raw, HashRefreshToken(raw), nil
}

func HashRefreshToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func GenerateSessionFamilyID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
