package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// Helper to sign test initData
func signInitData(params map[string]string, botToken string) string {
	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var checkParts []string
	for _, k := range keys {
		checkParts = append(checkParts, fmt.Sprintf("%s=%s", k, params[k]))
	}
	dataCheckString := strings.Join(checkParts, "\n")

	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	secretKey := secretMac.Sum(nil)

	hashMac := hmac.New(sha256.New, secretKey)
	hashMac.Write([]byte(dataCheckString))
	calculatedHash := hex.EncodeToString(hashMac.Sum(nil))

	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	values.Set("hash", calculatedHash)
	return values.Encode()
}

func TestValidateInitData_Success(t *testing.T) {
	botToken := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ"
	authDate := time.Now().Unix()
	userJSON := `{"id":987654321,"first_name":"Alice","last_name":"Smith","username":"alice_smith","language_code":"zh-hans"}`

	params := map[string]string{
		"query_id":  "AAHdF6IQAAAAAN0XohDhrOrc",
		"user":      userJSON,
		"auth_date": fmt.Sprintf("%d", authDate),
	}

	initDataStr := signInitData(params, botToken)

	res, err := ValidateInitData(initDataStr, botToken, 24*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "AAHdF6IQAAAAAN0XohDhrOrc", res.QueryID)
	require.NotNil(t, res.User)
	assert.Equal(t, int64(987654321), res.User.ID)
	assert.Equal(t, "Alice", res.User.FirstName)
	assert.Equal(t, "Smith", res.User.LastName)
	assert.Equal(t, "alice_smith", res.User.Username)
	assert.Equal(t, "zh-hans", res.User.LanguageCode)
}

func TestValidateInitData_InvalidSignature(t *testing.T) {
	botToken := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ"
	wrongToken := "987654321:XYZwvutsrqponmlkjihgfedcba"
	authDate := time.Now().Unix()
	userJSON := `{"id":987654321,"first_name":"Alice"}`

	params := map[string]string{
		"user":      userJSON,
		"auth_date": fmt.Sprintf("%d", authDate),
	}

	initDataStr := signInitData(params, botToken)

	// Validate with wrong token
	_, err := ValidateInitData(initDataStr, wrongToken, 24*time.Hour)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}

func TestValidateInitData_Expired(t *testing.T) {
	botToken := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ"
	authDate := time.Now().Add(-48 * time.Hour).Unix()
	userJSON := `{"id":987654321,"first_name":"Alice"}`

	params := map[string]string{
		"user":      userJSON,
		"auth_date": fmt.Sprintf("%d", authDate),
	}

	initDataStr := signInitData(params, botToken)

	_, err := ValidateInitData(initDataStr, botToken, 24*time.Hour)
	assert.ErrorIs(t, err, ErrAuthDateExpired)
}

func TestValidateInitData_MissingParameters(t *testing.T) {
	botToken := "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ"

	_, err := ValidateInitData("", botToken, 24*time.Hour)
	assert.ErrorIs(t, err, ErrEmptyInitData)

	_, err = ValidateInitData("foo=bar", "", 24*time.Hour)
	assert.ErrorIs(t, err, ErrEmptyBotToken)

	_, err = ValidateInitData("foo=bar", botToken, 24*time.Hour)
	assert.ErrorIs(t, err, ErrMissingHash)
}
