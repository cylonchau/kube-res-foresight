// Package assistant is the chat that reads the platform's state for a user. A language model (any service that speaks the OpenAI chat
// completions protocol) is given a small set of read-only tools; it asks for the data it needs, the platform answers from what it has
// stored, and the model puts it into words. The assistant never changes anything: there is no tool that writes, and the tools it has
// return stored facts only. Skills (/report, /export, ... and the ones an administrator writes) do the common jobs in one command.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
)

// The assistant has one system parameter: the switch (off by default). Everything about a language model (provider, endpoint, model, key,
// limits, advanced settings) is a connection, kept in the menu AI configuration > LLM management and not in the system parameters.
const (
	KeyEnabled = "assistant.enabled"
	// KeyPrefix is the prefix of the parameters of the assistant; the ones other than the switch were connection settings that moved to the
	// connections (see the upgrade).
	KeyPrefix = "assistant."

	// SecretMask stands for a stored key, or the value of a stored header, in a form; sending it back keeps the stored one.
	SecretMask = "********"
)

// DefaultEndpoint is the OpenAI API; any service with the same protocol (a gateway, vLLM, Ollama, DeepSeek, Qwen's compatible mode) is
// used by changing the endpoint.
const DefaultEndpoint = "https://api.openai.com/v1"

// The limits a connection may set, and what it starts with.
const (
	DefaultTimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds = 90, 5, 600
	DefaultMaxToolRounds, MinMaxToolRounds, MaxMaxToolRounds    = 6, 1, 12
	MinMaxTokens, MaxMaxTokens                                  = 128, 65536
	MinContextLength, MaxContextLength                          = 1024, 2000000
	MaxTemperature                                              = 2.0
	maxHeaders, maxHeaderValue, maxCustomParams                 = 20, 1000, 4096
	maxEndpointRunes, maxNameRunes, maxModelRunes               = 500, 100, 100
	maxDescriptionRunes                                         = 255
	maxQuestionTime                                             = 10 * time.Minute
)

// DefaultConfigs are the parameters the platform starts with: the switch.
func DefaultConfigs() []model.SysConfig {
	return []model.SysConfig{{ConfigKey: KeyEnabled, ConfigValue: "false", ConfigName: "AI Assistant Enabled",
		Remark: "Switches the AI assistant (the chat window at the top right of the header) on or off for everyone. The language models it uses are set in the menu AI configuration > LLM management."}}
}

// Header is one custom header of the requests to a language model.
type Header struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Connection is the language model the assistant talks to, as the assistant uses it (the key and the headers decrypted).
type Connection struct {
	ID             uint
	Name           string
	Provider       string
	Endpoint       string
	Model          string
	APIKey         string
	Timeout        time.Duration
	SkipTLSVerify  bool
	ProxyURL       string
	Headers        []Header
	CustomParams   map[string]any
	Temperature    *float64
	MaxTokens      int
	ContextLength  int
	MaxToolRounds  int
	keyUnreadable  bool
	paramsUnusable bool
}

// FromRow reads a stored connection. A key or headers that cannot be decrypted (the secret key of the platform changed) count as missing,
// so the connection says it needs to be set again instead of failing with a strange error from the service.
func FromRow(row *model.AssistantConnection) Connection {
	c := Connection{ID: row.ID, Name: row.Name, Provider: row.Provider, Endpoint: strings.TrimSpace(row.Endpoint), Model: strings.TrimSpace(row.Model),
		Timeout:       time.Duration(clampInt(row.TimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds, DefaultTimeoutSeconds)) * time.Second,
		SkipTLSVerify: row.SkipTLSVerify, ProxyURL: strings.TrimSpace(row.ProxyURL), Temperature: row.Temperature,
		MaxTokens: row.MaxTokens, ContextLength: row.ContextLength,
		MaxToolRounds: clampInt(row.MaxToolRounds, MinMaxToolRounds, MaxMaxToolRounds, DefaultMaxToolRounds)}
	if c.Provider == "" {
		c.Provider = model.ProviderOpenAI
	}
	if row.APIKey != "" {
		if key, err := cryptoutils.DecryptValue(row.APIKey); err == nil {
			c.APIKey = key
		} else {
			c.keyUnreadable = true
		}
	}
	if row.Headers != "" {
		headers, err := decryptHeaders(row.Headers)
		if err != nil {
			c.keyUnreadable = true
		}
		c.Headers = headers
	}
	if strings.TrimSpace(row.CustomParams) != "" {
		if err := json.Unmarshal([]byte(row.CustomParams), &c.CustomParams); err != nil {
			c.paramsUnusable = true
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
	case c.Provider != model.ProviderOpenAI:
		return "the provider of the language model is not supported"
	case c.Endpoint == "":
		return "the endpoint of the language model is not set"
	case c.Model == "":
		return "the language model is not set"
	case c.keyUnreadable:
		return "the key or the headers of the language model can no longer be read: save them again"
	case c.APIKey == "":
		return "the key of the language model is not set"
	case c.paramsUnusable:
		return "the custom parameters of the language model are not valid JSON"
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
		return "", fmt.Errorf("the API URL must be an http or https URL, for example %s", DefaultEndpoint)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the API URL must not carry user info, a query or a fragment")
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

func encryptHeaders(headers []Header) (string, error) {
	if len(headers) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return cryptoutils.EncryptValue(string(raw))
}

func decryptHeaders(stored string) ([]Header, error) {
	plain, err := cryptoutils.DecryptValue(stored)
	if err != nil {
		return nil, err
	}
	var headers []Header
	return headers, json.Unmarshal([]byte(plain), &headers)
}

// StoredHeaderNames lists the names of the custom headers of a stored connection, the values hidden: the form shows them with the mask.
func StoredHeaderNames(row *model.AssistantConnection) []Header {
	out := []Header{}
	if row.Headers == "" {
		return out
	}
	headers, err := decryptHeaders(row.Headers)
	if err != nil {
		return out
	}
	for _, h := range headers {
		out = append(out, Header{Key: h.Key, Value: SecretMask})
	}
	return out
}

var (
	headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&*+.^_|~-]{1,64}$")
	// headers the transport owns: sending them would break the request, not configure it
	reservedHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "content-type": true}
	// fields of the request the assistant writes itself
	reservedParams = map[string]bool{"model": true, "messages": true, "tools": true, "tool_choice": true, "stream": true, "n": true, "functions": true, "function_call": true}
)

// ConnectionInput is what a form sends for a connection.
type ConnectionInput struct {
	Name          string
	Description   string
	Provider      string
	Endpoint      string
	Model         string
	APIKey        string // empty or the mask keeps the stored key
	Enabled       bool
	IsDefault     bool
	TimeoutSecond int
	SkipTLSVerify bool
	ProxyURL      string
	Headers       []Header // a value that is the mask keeps the stored value of the header of that name
	CustomParams  string
	Temperature   *float64
	MaxTokens     int
	ContextLength int
	MaxToolRounds int
}

// Apply checks the input and writes it into the row (a new row, or the stored one being edited). The key and the header values are
// encrypted; a new connection needs a key.
func Apply(row *model.AssistantConnection, in ConnectionInput) error {
	name, modelName, description := strings.TrimSpace(in.Name), strings.TrimSpace(in.Model), strings.TrimSpace(in.Description)
	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		provider = model.ProviderOpenAI
	}
	switch {
	case name == "" || len([]rune(name)) > maxNameRunes:
		return fmt.Errorf("the name is required and at most %d characters", maxNameRunes)
	case provider != model.ProviderOpenAI:
		return fmt.Errorf("the provider %q is not supported: the OpenAI-compatible protocol is", provider)
	case modelName == "" || len([]rune(modelName)) > maxModelRunes:
		return fmt.Errorf("the model is required and at most %d characters", maxModelRunes)
	case len([]rune(description)) > maxDescriptionRunes:
		return fmt.Errorf("the description is at most %d characters", maxDescriptionRunes)
	case in.IsDefault && !in.Enabled:
		return errors.New("the default connection must be enabled")
	}
	endpoint, err := NormalizeEndpoint(in.Endpoint)
	if err != nil {
		return err
	}
	if len([]rune(endpoint)) > maxEndpointRunes {
		return errors.New("the API URL is too long")
	}
	proxy := strings.TrimSpace(in.ProxyURL)
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return errors.New("the proxy must be an http, https or socks5 URL, for example http://proxy:8080")
		}
	}
	timeout := in.TimeoutSecond
	if timeout == 0 {
		timeout = DefaultTimeoutSeconds
	}
	rounds := in.MaxToolRounds
	if rounds == 0 {
		rounds = DefaultMaxToolRounds
	}
	for _, l := range []struct {
		name      string
		v, lo, hi int
	}{{"timeout_seconds", timeout, MinTimeoutSeconds, MaxTimeoutSeconds}, {"max_tool_rounds", rounds, MinMaxToolRounds, MaxMaxToolRounds}} {
		if l.v < l.lo || l.v > l.hi {
			return fmt.Errorf("%s must be between %d and %d", l.name, l.lo, l.hi)
		}
	}
	if in.MaxTokens != 0 && (in.MaxTokens < MinMaxTokens || in.MaxTokens > MaxMaxTokens) {
		return fmt.Errorf("max_tokens is empty or between %d and %d", MinMaxTokens, MaxMaxTokens)
	}
	if in.ContextLength != 0 && (in.ContextLength < MinContextLength || in.ContextLength > MaxContextLength) {
		return fmt.Errorf("context_length is empty or between %d and %d", MinContextLength, MaxContextLength)
	}
	if t := in.Temperature; t != nil && (*t < 0 || *t > MaxTemperature) {
		return fmt.Errorf("temperature is empty or between 0 and %g", MaxTemperature)
	}
	params := strings.TrimSpace(in.CustomParams)
	if params != "" {
		if len(params) > maxCustomParams {
			return fmt.Errorf("the custom parameters are at most %d characters", maxCustomParams)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(params), &obj); err != nil || obj == nil {
			return errors.New("the custom parameters must be a JSON object, for example {\"top_p\": 0.9}")
		}
		for key := range obj {
			if reservedParams[strings.ToLower(key)] {
				return fmt.Errorf("the custom parameter %q is set by the assistant itself", key)
			}
		}
	}

	// the key: kept when the form leaves it empty or sends the mask back
	key := strings.TrimSpace(in.APIKey)
	if key != "" && key != SecretMask {
		enc, err := cryptoutils.EncryptValue(key)
		if err != nil {
			return fmt.Errorf("could not store the key: %w", err)
		}
		row.APIKey = enc
	}
	if row.APIKey == "" {
		return errors.New("the API key is required")
	}

	// the headers: a value that is the mask is the stored value of the header of that name
	stored := map[string]string{}
	if row.Headers != "" {
		if old, err := decryptHeaders(row.Headers); err == nil {
			for _, h := range old {
				stored[strings.ToLower(h.Key)] = h.Value
			}
		}
	}
	if len(in.Headers) > maxHeaders {
		return fmt.Errorf("at most %d custom headers", maxHeaders)
	}
	headers := make([]Header, 0, len(in.Headers))
	seen := map[string]bool{}
	for _, h := range in.Headers {
		k, v := strings.TrimSpace(h.Key), h.Value
		if k == "" && v == "" {
			continue
		}
		if !headerName.MatchString(k) || reservedHeaders[strings.ToLower(k)] {
			return fmt.Errorf("the header name %q is not valid", k)
		}
		if seen[strings.ToLower(k)] {
			return fmt.Errorf("the header %q is given twice", k)
		}
		seen[strings.ToLower(k)] = true
		if v == SecretMask {
			old, ok := stored[strings.ToLower(k)]
			if !ok {
				return fmt.Errorf("the header %q has no stored value: enter it", k)
			}
			v = old
		}
		if len(v) > maxHeaderValue || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("the value of the header %q is not valid", k)
		}
		headers = append(headers, Header{Key: k, Value: v})
	}
	encHeaders, err := encryptHeaders(headers)
	if err != nil {
		return fmt.Errorf("could not store the headers: %w", err)
	}

	row.Name, row.Description, row.Provider, row.Endpoint, row.Model = name, description, provider, endpoint, modelName
	row.Enabled, row.IsDefault = in.Enabled, in.IsDefault
	row.TimeoutSeconds, row.SkipTLSVerify, row.ProxyURL, row.Headers, row.CustomParams = timeout, in.SkipTLSVerify, proxy, encHeaders, params
	row.Temperature, row.MaxTokens, row.ContextLength, row.MaxToolRounds = in.Temperature, in.MaxTokens, in.ContextLength, rounds
	return nil
}
