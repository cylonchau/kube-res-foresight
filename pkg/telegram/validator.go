package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrEmptyInitData    = errors.New("telegram initData is empty")
	ErrEmptyBotToken    = errors.New("telegram bot token is empty")
	ErrMissingHash      = errors.New("telegram initData missing hash parameter")
	ErrInvalidSignature = errors.New("invalid telegram initData signature")
	ErrAuthDateExpired  = errors.New("telegram initData has expired")
	ErrMissingAuthDate  = errors.New("telegram initData missing auth_date parameter")
	ErrMissingUser      = errors.New("telegram initData missing user parameter")
	ErrInvalidUserData  = errors.New("failed to parse telegram user data")
)

// TelegramUser is an alias for model.TelegramUser — the type moved to
// pkg/model so pkg/dao/rdb could stop importing this package (see that
// type's doc comment). An alias, not a new type, so every existing
// telegram.TelegramUser reference keeps working and stays assignment-
// compatible with model.TelegramUser.
type TelegramUser = model.TelegramUser

// InitData represents the parsed and validated Telegram WebApp initData
type InitData struct {
	QueryID      string
	User         *TelegramUser
	Receiver     *TelegramUser
	Chat         map[string]interface{}
	ChatType     string
	ChatInstance string
	StartParam   string
	CanSendAfter int
	AuthDate     time.Time
	Hash         string
	RawData      map[string]string
}

// ValidateInitData validates the integrity and freshness of Telegram Mini App initData.
// If maxAge <= 0, a default maxAge of 24 hours is applied.
func ValidateInitData(rawInitData string, botToken string, maxAge time.Duration) (*InitData, error) {
	rawInitData = strings.TrimSpace(rawInitData)
	if rawInitData == "" {
		return nil, ErrEmptyInitData
	}
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return nil, ErrEmptyBotToken
	}
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}

	values, err := url.ParseQuery(rawInitData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse initData query string: %w", err)
	}

	hash := values.Get("hash")
	if hash == "" {
		return nil, ErrMissingHash
	}

	// 1. Build sorted data_check_string
	var keys []string
	rawMap := make(map[string]string)
	for k, v := range values {
		if k == "hash" {
			continue
		}
		if len(v) > 0 {
			rawMap[k] = v[0]
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var checkParts []string
	for _, k := range keys {
		checkParts = append(checkParts, fmt.Sprintf("%s=%s", k, rawMap[k]))
	}
	dataCheckString := strings.Join(checkParts, "\n")

	// 2. Secret Key = HMAC_SHA256(key="WebAppData", data=botToken)
	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	secretKey := secretMac.Sum(nil)

	// 3. Calculated Hash = hex(HMAC_SHA256(key=secretKey, data=dataCheckString))
	hashMac := hmac.New(sha256.New, secretKey)
	hashMac.Write([]byte(dataCheckString))
	calculatedHash := hex.EncodeToString(hashMac.Sum(nil))

	// 4. Constant time comparison
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(calculatedHash)), []byte(strings.ToLower(hash))) != 1 {
		return nil, ErrInvalidSignature
	}

	// 5. Validate auth_date freshness
	authDateStr := rawMap["auth_date"]
	if authDateStr == "" {
		return nil, ErrMissingAuthDate
	}
	authDateUnix, err := strconv.ParseInt(authDateStr, 10, 64)
	if err != nil {
		return nil, ErrMissingAuthDate
	}
	authDate := time.Unix(authDateUnix, 0)
	if time.Since(authDate) > maxAge {
		return nil, ErrAuthDateExpired
	}

	// 6. Parse User
	result := &InitData{
		QueryID:      rawMap["query_id"],
		ChatType:     rawMap["chat_type"],
		ChatInstance: rawMap["chat_instance"],
		StartParam:   rawMap["start_param"],
		AuthDate:     authDate,
		Hash:         hash,
		RawData:      rawMap,
	}

	if userStr, ok := rawMap["user"]; ok && userStr != "" {
		var user TelegramUser
		if err := json.Unmarshal([]byte(userStr), &user); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidUserData, err)
		}
		result.User = &user
	}

	if result.User == nil {
		return nil, ErrMissingUser
	}

	return result, nil
}
