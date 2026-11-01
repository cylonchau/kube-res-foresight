// Package assistant is the chat that reads the platform's state for a user. A language model (any service that speaks the OpenAI chat
// completions protocol) is given a small set of read-only tools; it asks for the data it needs, the platform answers from what it has
// stored, and the model puts it into words. The assistant never changes anything: there is no tool that writes, and the tools it has
// return stored facts only.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	cryptoutils "github.com/cylonchau/kube-resource-fcst/pkg/utils"
)

// The connection to the language model is four ordinary system parameters (off by default). The key is a secret: it is stored encrypted,
// shown as a mask, and kept when the mask is sent back. The limits of the assistant (timeout, reads per question, reply length) are
// system parameters too, in package timing.
const (
	KeyPrefix   = "assistant."
	KeyEnabled  = "assistant.enabled"
	KeyEndpoint = "assistant.endpoint"
	KeyModel    = "assistant.model"
	KeyAPIKey   = "assistant.api_key"

	// SecretMask stands for a stored key in the settings list; sending it back keeps the stored one.
	SecretMask = "********"
)

// DefaultEndpoint is the OpenAI API; any service with the same protocol (a gateway, vLLM, Ollama, DeepSeek, Qwen's compatible mode) is
// used by changing the endpoint.
const DefaultEndpoint = "https://api.openai.com/v1"

// DefaultConfigs are the parameters the platform starts with.
func DefaultConfigs() []model.SysConfig {
	const remark = "AI assistant: the chat reads the platform's state through read-only tools; enabling needs the endpoint, the model and the key"
	return []model.SysConfig{
		{ConfigKey: KeyEnabled, ConfigValue: "false", ConfigName: "Assistant Enabled", Remark: remark},
		{ConfigKey: KeyEndpoint, ConfigValue: DefaultEndpoint, ConfigName: "Assistant Language Model Endpoint", Remark: "Base URL of a service that speaks the OpenAI chat completions protocol, for example https://api.openai.com/v1. The platform adds /chat/completions."},
		{ConfigKey: KeyModel, ConfigValue: "", ConfigName: "Assistant Language Model", Remark: "The model name the endpoint knows, for example gpt-4o-mini. It must support tool (function) calling."},
		{ConfigKey: KeyAPIKey, ConfigValue: "", ConfigName: "Assistant API Key", Remark: "The key of the endpoint. Stored encrypted and never shown."},
	}
}

// Secret reports whether the parameter holds a credential.
func Secret(key string) bool { return key == KeyAPIKey }

// Settings is the connection to the language model.
type Settings struct {
	Enabled  bool
	Endpoint string
	Model    string
	APIKey   string
}

// ConfigReader is the part of the system parameters the assistant reads.
type ConfigReader interface {
	GetValueOrDefault(ctx context.Context, key, fallback string) (string, error)
}

// Load reads the settings; a key that cannot be decrypted (the secret key of the platform changed) counts as no key.
func Load(ctx context.Context, r ConfigReader) (Settings, error) {
	get := func(key, fallback string) (string, error) { return r.GetValueOrDefault(ctx, key, fallback) }
	var s Settings
	enabled, err := get(KeyEnabled, "false")
	if err != nil {
		return s, err
	}
	s.Enabled = strings.EqualFold(strings.TrimSpace(enabled), "true")
	if s.Endpoint, err = get(KeyEndpoint, DefaultEndpoint); err != nil {
		return s, err
	}
	if s.Model, err = get(KeyModel, ""); err != nil {
		return s, err
	}
	stored, err := get(KeyAPIKey, "")
	if err != nil {
		return s, err
	}
	if stored != "" {
		if key, err := cryptoutils.DecryptValue(stored); err == nil {
			s.APIKey = key
		}
	}
	s.Endpoint, s.Model = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Model)
	return s, nil
}

// Problem says what is missing for the assistant to work, or "" when it can be used.
func (s Settings) Problem() string {
	switch {
	case !s.Enabled:
		return "the assistant is switched off (system parameter assistant.enabled)"
	case s.missing() != "":
		return s.missing()
	}
	return ""
}

// missing names the connection parameter that is not set.
func (s Settings) missing() string {
	switch {
	case s.Endpoint == "":
		return "the endpoint of the language model is not set (assistant.endpoint)"
	case s.Model == "":
		return "the language model is not set (assistant.model)"
	case s.APIKey == "":
		return "the key of the language model is not set (assistant.api_key)"
	}
	return ""
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

// UpdateKey saves one assistant parameter after checking it. The key is stored encrypted, and a mask sent back keeps it; the endpoint is
// checked; switching the assistant on needs the whole connection to be set, so it cannot be left on and dead. Parameters that are not
// connection settings (the limits) are ordinary numbers and go through the generic update.
func UpdateKey(ctx context.Context, dao *rdb.SysConfigDAO, key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case KeyAPIKey:
		if value == SecretMask {
			return nil
		}
		if value != "" {
			enc, err := cryptoutils.EncryptValue(value)
			if err != nil {
				return fmt.Errorf("could not store the key: %w", err)
			}
			value = enc
		}
		if err := dao.UpdateValue(ctx, key, value); err != nil {
			return err
		}
		// a key that was cleared leaves the assistant with nothing to connect with: it is switched off rather than left failing
		if value == "" {
			return dao.UpdateValue(ctx, KeyEnabled, "false")
		}
		return nil
	case KeyEndpoint:
		endpoint, err := NormalizeEndpoint(value)
		if err != nil {
			return err
		}
		value = endpoint
	case KeyModel:
		if len(value) > 100 {
			return errors.New("the model name is too long")
		}
	case KeyEnabled:
		switch strings.ToLower(value) {
		case "true":
			s, err := Load(ctx, dao)
			if err != nil {
				return err
			}
			s.Enabled = true
			if problem := s.missing(); problem != "" {
				return errors.New(problem + ": set it first, then switch the assistant on")
			}
			value = "true"
		case "false":
			value = "false"
		default:
			return errors.New("assistant.enabled is true or false")
		}
	}
	return dao.UpdateValue(ctx, key, value)
}
