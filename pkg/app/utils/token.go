package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// HashToken returns the hex-encoded SHA-256 digest of a bearer/enrollment token,
// suitable for storing instead of the raw secret.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomHex returns a hex-encoded string of size random bytes.
func RandomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("secure random source failed: %v", err))
	}
	return hex.EncodeToString(value)
}
