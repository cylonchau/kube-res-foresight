// Package assistant is the chat that reads the platform's state for a user. A language model (any service that speaks the OpenAI chat
// completions protocol) is given a small set of read-only tools; it asks for the data it needs, the platform answers from what it has
// stored, and the model puts it into words. The assistant never changes anything: there is no tool that writes, and the tools it has
// return stored facts only. A few skills (/report, /export, ...) do the common jobs in one command.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
)

// The assistant has one system parameter: the switch (off by default). Everything about the language model (address, model, key, limits)
// is a connection, kept in the menu "AI assistant" and not in the system parameters.
const (
	KeyEnabled = "assistant.enabled"
	// KeyPrefix is the prefix of the parameters of the assistant; the ones other than the switch were connection settings that moved to the
	// connections (see the upgrade).
	KeyPrefix = "assistant."

	// SecretMask stands for a stored key in a form; sending it back keeps the stored one.
	SecretMask = "********"
)

// DefaultEndpoint is the OpenAI API; any service with the same protocol (a gateway, vLLM, Ollama, DeepSeek, Qwen's compatible mode) is
// used by changing the endpoint.
const DefaultEndpoint = "https://api.openai.com/v1"

// The limits of one question, and what a connection may set them to.
const (
	DefaultTimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds       = 60, 5, 300
	DefaultMaxToolRounds, MinMaxToolRounds, MaxMaxToolRounds          = 6, 1, 12
	DefaultMaxReplyTokens, MinMaxReplyTokens, MaxMaxReplyTokens       = 1500, 128, 8192
	maxEndpointRunes, maxNameRunes, maxModelRunes, maxRemarkRunes int = 500, 100, 100, 255
)

// DefaultConfigs are the parameters the platform starts with: the switch.
func DefaultConfigs() []model.SysConfig {
	return []model.SysConfig{{ConfigKey: KeyEnabled, ConfigValue: "false", ConfigName: "AI Assistant Enabled",
		Remark: "Switches the AI assistant (the chat window at the top right of the header) on or off for everyone. The language model it uses is set in the menu AI assistant."}}
}

// Connection is the language model the assistant talks to, as the assistant uses it (the key decrypted).
type Connection struct {
	Name           string
	Endpoint       string
	Model          string
	APIKey         string
	Timeout        time.Duration
	MaxToolRounds  int
	MaxReplyTokens int
}

// FromRow reads a stored connection; a key that cannot be decrypted (the secret key of the platform changed) counts as no key.
func FromRow(row *model.AssistantConnection) Connection {
	c := Connection{Name: row.Name, Endpoint: strings.TrimSpace(row.Endpoint), Model: strings.TrimSpace(row.Model),
		Timeout:        time.Duration(clampInt(row.TimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds, DefaultTimeoutSeconds)) * time.Second,
		MaxToolRounds:  clampInt(row.MaxToolRounds, MinMaxToolRounds, MaxMaxToolRounds, DefaultMaxToolRounds),
		MaxReplyTokens: clampInt(row.MaxReplyTokens, MinMaxReplyTokens, MaxMaxReplyTokens, DefaultMaxReplyTokens)}
	if row.APIKey != "" {
		if key, err := cryptoutils.DecryptValue(row.APIKey); err == nil {
			c.APIKey = key
		}
	}
	return c
}

func clampInt(n, lo, hi, fallback int) int {
	switch {
	case n == 0:
		return fallback
	case n < lo:
		return lo
	case n > hi:
		return hi
	}
	return n
}

// Problem says what is missing for the connection to be used, or "".
func (c Connection) Problem() string {
	switch {
	case c.Endpoint == "":
		return "the endpoint of the language model is not set"
	case c.Model == "":
		return "the language model is not set"
	case c.APIKey == "":
		return "the key of the language model is not set or can no longer be read"
	}
	return ""
}

// ConfigReader is the part of the system parameters the assistant reads.
type ConfigReader interface {
	GetBool(ctx context.Context, key string, fallback bool) (bool, error)
}

// Enabled reads the switch.
func Enabled(ctx context.Context, r ConfigReader) (bool, error) {
	return r.GetBool(ctx, KeyEnabled, false)
}

// NormalizeEndpoint checks an endpoint and returns it without a trailing slash: an http or https URL with a host and nothing after it that
// would turn into a different request (no user info, query or fragment).
func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("the endpoint must be an http or https URL, for example %s", DefaultEndpoint)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the endpoint must not carry user info, a query or a fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// UpdateEnabled saves the switch.
func UpdateEnabled(ctx context.Context, dao *rdb.SysConfigDAO, value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return dao.UpdateValue(ctx, KeyEnabled, "true")
	case "false":
		return dao.UpdateValue(ctx, KeyEnabled, "false")
	}
	return errors.New("assistant.enabled is true or false")
}

// ConnectionInput is what a form sends for a connection.
type ConnectionInput struct {
	Name           string
	Endpoint       string
	Model          string
	APIKey         string // empty or the mask keeps the stored key
	Enabled        bool
	TimeoutSeconds int
	MaxToolRounds  int
	MaxReplyTokens int
	Remark         string
}

// Apply checks the input and writes it into the row (a new row, or the stored one being edited). The key is encrypted; a new connection
// needs one.
func Apply(row *model.AssistantConnection, in ConnectionInput) error {
	name, model, remark := strings.TrimSpace(in.Name), strings.TrimSpace(in.Model), strings.TrimSpace(in.Remark)
	switch {
	case name == "" || len([]rune(name)) > maxNameRunes:
		return fmt.Errorf("the name is required and at most %d characters", maxNameRunes)
	case model == "" || len([]rune(model)) > maxModelRunes:
		return fmt.Errorf("the model is required and at most %d characters", maxModelRunes)
	case len([]rune(remark)) > maxRemarkRunes:
		return fmt.Errorf("the remark is at most %d characters", maxRemarkRunes)
	}
	endpoint, err := NormalizeEndpoint(in.Endpoint)
	if err != nil {
		return err
	}
	if len([]rune(endpoint)) > maxEndpointRunes {
		return errors.New("the endpoint is too long")
	}
	for _, l := range []struct {
		name      string
		v, lo, hi int
	}{{"timeout_seconds", in.TimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds}, {"max_tool_rounds", in.MaxToolRounds, MinMaxToolRounds, MaxMaxToolRounds},
		{"max_reply_tokens", in.MaxReplyTokens, MinMaxReplyTokens, MaxMaxReplyTokens}} {
		if l.v < l.lo || l.v > l.hi {
			return fmt.Errorf("%s must be between %d and %d", l.name, l.lo, l.hi)
		}
	}
	key := strings.TrimSpace(in.APIKey)
	if key != "" && key != SecretMask {
		enc, err := cryptoutils.EncryptValue(key)
		if err != nil {
			return fmt.Errorf("could not store the key: %w", err)
		}
		row.APIKey = enc
	}
	if row.APIKey == "" {
		return errors.New("the key of the language model is required")
	}
	row.Name, row.Endpoint, row.Model, row.Enabled, row.Remark = name, endpoint, model, in.Enabled, remark
	row.TimeoutSeconds, row.MaxToolRounds, row.MaxReplyTokens = in.TimeoutSeconds, in.MaxToolRounds, in.MaxReplyTokens
	return nil
}
