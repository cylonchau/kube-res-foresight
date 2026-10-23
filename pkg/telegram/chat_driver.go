package telegram

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"net/url"
	"strings"
)

// isValidWebAppURL reports whether Telegram will accept a URL for an inline
// WebApp button. Telegram requires an absolute HTTPS URL with a host; when
// it disagrees it fails the whole sendMessage, not just the button.
func isValidWebAppURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

// isValidLinkURL reports whether Telegram will accept a URL for a plain link
// button. Unlike WebApp buttons plain http is allowed, but a bare host like
// "baidu.com" is not — and, as above, an invalid one fails the whole send.
func isValidLinkURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

// TelegramChatDriver implements workflow.ChatDriver for Telegram — the
// first (and, today, only) concrete chat platform the generic workflow
// engine can notify through. A Teams/Slack/Feishu driver would live in its
// own package and implement the same interface; pkg/workflow never depends
// on this package.
type TelegramChatDriver struct {
	botToken   string
	miniAppURL string
	apiBaseURL string
}

// NewChatDriver builds a Telegram ChatDriver. miniAppURL is the default
// Mini App URL a "webapp" button falls back to when a workflow.Button's
// Target is empty, or gets prefixed onto when Target starts with "#"
// (relative route, e.g. "#/tickets/action/42") — the exact convention
// pkg/workflow/engine.go's executeNotifyAction used to apply itself before
// this driver abstraction existed.
func NewChatDriver(botToken, miniAppURL, apiBaseURL string) *TelegramChatDriver {
	return &TelegramChatDriver{botToken: botToken, miniAppURL: miniAppURL, apiBaseURL: apiBaseURL}
}

func (d *TelegramChatDriver) SendMessage(ctx context.Context, recipientID int64, text string, buttons []workflow.Button) error {
	if recipientID == 0 {
		return nil
	}
	opts := make([]SendOption, 0, 2)
	if rows := d.buildButtonRows(buttons); len(rows) > 0 {
		opts = append(opts, WithButtons(rows))
	}
	if d.apiBaseURL != "" {
		opts = append(opts, WithBaseURL(d.apiBaseURL))
	}
	return SendMessage(ctx, d.botToken, recipientID, text, opts...)
}

func (d *TelegramChatDriver) buildButtonRows(buttons []workflow.Button) [][]Button {
	rows := make([][]Button, 0, len(buttons))
	for _, b := range buttons {
		btn := Button{Text: b.Label}
		switch b.ActionType {
		case "callback":
			btn.CallbackData = b.Target
		case "webapp":
			url := b.Target
			if url == "" || strings.HasPrefix(url, "#") {
				if d.miniAppURL == "" {
					// No Mini App URL is configured, so there is nothing to
					// resolve this button against. Dropping it matters more
					// than it looks: Telegram validates every WebApp button
					// and rejects the ENTIRE sendMessage if one is invalid,
					// so emitting a relative Mini App route here does not
					// produce a broken button — it makes the whole
					// notification silently never arrive.
					continue
				}
				url = d.miniAppURL + url
			}
			if !isValidWebAppURL(url) {
				// Same reasoning: an http:// or otherwise malformed Mini App
				// URL takes the message down with it. Telegram only accepts
				// HTTPS for WebApp buttons.
				logger.Warn("Skipping WebApp button with an invalid URL; Telegram only accepts https",
					logger.String("url", url), logger.String("label", b.Label))
				continue
			}
			btn.WebAppURL = url
		case "url":
			if b.Target == "" {
				continue
			}
			btn.URL = b.Target
		default:
			continue
		}
		rows = append(rows, []Button{btn})
	}
	return rows
}

// SendToChat sends to a public channel or group named "@username".
func (d *TelegramChatDriver) SendToChat(ctx context.Context, chat, text string, buttons []workflow.Button) error {
	opts := make([]SendOption, 0, 2)
	if rows := d.buildButtonRows(buttons); len(rows) > 0 {
		opts = append(opts, WithButtons(rows))
	}
	if d.apiBaseURL != "" {
		opts = append(opts, WithBaseURL(d.apiBaseURL))
	}
	return SendMessageToChat(ctx, d.botToken, chat, text, opts...)
}

// SenderProvider resolves the credentials outbound messages are sent with.
// ok is false when no bot is configured.
type SenderProvider func(ctx context.Context) (token, apiBaseURL, miniAppURL string, ok bool, err error)

// DynamicChatDriver sends through whichever bot the provider currently names,
// so adding, disabling or re-keying a bot takes effect without restarting the
// process that holds the driver.
type DynamicChatDriver struct{ provider SenderProvider }

func NewDynamicChatDriver(provider SenderProvider) *DynamicChatDriver {
	return &DynamicChatDriver{provider: provider}
}

func (d *DynamicChatDriver) SendMessage(ctx context.Context, recipientID int64, text string, buttons []workflow.Button) error {
	token, baseURL, miniApp, ok, err := d.provider(ctx)
	if err != nil {
		return err
	}
	if !ok {
		logger.Warn("No enabled Telegram bot; dropping outbound message")
		return nil
	}
	return NewChatDriver(token, miniApp, baseURL).SendMessage(ctx, recipientID, text, buttons)
}
