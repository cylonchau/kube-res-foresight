package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SysConfig keys holding the bot's configuration. This is the single source
// of truth used by the long-poll receiver (server process, pkg/telegram.BotService),
// outbound sends from both the server and the separately-deployed job engine
// process (via SendMessage above), and the admin bot-management API
// (pkg/app/server/api/handle_telegram.go) that lets these be edited without a
// config.yaml redeploy.
//
// Deliberately prefixed "telegram.bot.", not "notify.": ConfigBotEnabled
// gates the entire bot, including inbound account-binding (/start) and
// workflow-approval callbacks — not just outbound notifications — so it
// doesn't belong under the notification-settings category. See
// pkg/migration.migrateTelegramBotConfigPrefix for the one-time rename off
// the original "notify.telegram_bot.*" keys.
const (
	ConfigBotEnabled    = "telegram.bot.enabled"
	ConfigBotToken      = "telegram.bot.token"
	ConfigBotUsername   = "telegram.bot.username"
	ConfigBotMiniAppURL = "telegram.bot.miniapp_url"
	// ConfigBotAPIBaseURL optionally points outbound sends at a self-hosted
	// telegram-bot-api server (https://github.com/tdlib/telegram-bot-api)
	// instead of api.telegram.org — e.g. "http://10.0.0.5:8081". Empty means
	// the default public API.
	ConfigBotAPIBaseURL = "telegram.bot.api_base_url"

	// Legacy keys compatibility (notify.telegram.*)
	ConfigLegacyBotEnabled  = "notify.telegram.enabled"
	ConfigLegacyBotToken    = "notify.telegram.bot_token"
	ConfigLegacyBotUsername = "notify.telegram.bot_username"
	ConfigLegacyMiniAppURL  = "notify.telegram.miniapp_url"
	ConfigLegacyAPIBaseURL  = "notify.telegram.api_base_url"

	// Intermediate keys used by releases between the original notify.telegram.*
	// scheme and the current telegram.bot.* scheme. Keep read compatibility so
	// an older database cannot make the server silently lose its configured bot.
	ConfigIntermediateBotEnabled  = "notify.telegram_bot.enabled"
	ConfigIntermediateBotToken    = "notify.telegram_bot.bot_token"
	ConfigIntermediateBotUsername = "notify.telegram_bot.bot_username"
	ConfigIntermediateMiniAppURL  = "notify.telegram_bot.miniapp_url"
	ConfigIntermediateAPIBaseURL  = "notify.telegram_bot.api_base_url"
)

const defaultAPIBaseURL = "https://api.telegram.org"

var senderHTTPClient = &http.Client{Timeout: 10 * time.Second}

type sendMessageRequest struct {
	// ChatID is a numeric chat id, or "@username" for a public channel or group.
	ChatID      any         `json:"chat_id"`
	Text        string      `json:"text"`
	ParseMode   string      `json:"parse_mode,omitempty"`
	ReplyMarkup interface{} `json:"reply_markup,omitempty"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text   string      `json:"text"`
	WebApp *webAppInfo `json:"web_app,omitempty"`
	Data   string      `json:"callback_data,omitempty"`
	URL    string      `json:"url,omitempty"`
}

type webAppInfo struct {
	URL string `json:"url"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// sendOptions collects both request-body fields (replyMarkup) and transport
// options (baseURL) that SendOption values can set — kept separate from
// sendMessageRequest so a base-URL override never leaks into the JSON body.
type sendOptions struct {
	replyMarkup any
	baseURL     string
}

// SendOption customizes an outbound message built by SendMessage.
type SendOption func(*sendOptions)

// WithInlineButton attaches a single WebApp inline button below the message.
func WithInlineButton(text, webAppURL string) SendOption {
	return func(o *sendOptions) {
		if webAppURL == "" {
			return
		}
		o.replyMarkup = inlineKeyboardMarkup{
			InlineKeyboard: [][]inlineKeyboardButton{{{Text: text, WebApp: &webAppInfo{URL: webAppURL}}}},
		}
	}
}

// Button is one inline keyboard button for WithButtons. Exactly one of
// WebAppURL (opens the Mini App), CallbackData (routed to the long-poll
// receiver's callback handler, see pkg/telegram/bot.go), or URL (opens an
// ordinary external link, not routed through the bot at all) should be
// set — a button with none of the three is dropped.
type Button struct {
	Text         string
	WebAppURL    string
	CallbackData string
	URL          string
}

// WithButtons attaches a full inline keyboard (rows of buttons, top to
// bottom) to the message. Unlike WithInlineButton it supports multiple
// buttons/rows and callback-data buttons, not just a single WebApp button —
// used by notify.Broadcast sends that carry a NotifyTemplate's configured
// buttons (see pkg/job/tasks/notify.go).
func WithButtons(rows [][]Button) SendOption {
	return func(o *sendOptions) {
		if len(rows) == 0 {
			return
		}
		keyboard := make([][]inlineKeyboardButton, 0, len(rows))
		for _, row := range rows {
			btnRow := make([]inlineKeyboardButton, 0, len(row))
			for _, b := range row {
				btn := inlineKeyboardButton{Text: b.Text}
				switch {
				case b.WebAppURL != "":
					btn.WebApp = &webAppInfo{URL: b.WebAppURL}
				case b.CallbackData != "":
					btn.Data = b.CallbackData
				case b.URL != "":
					btn.URL = b.URL
				default:
					continue
				}
				btnRow = append(btnRow, btn)
			}
			if len(btnRow) > 0 {
				keyboard = append(keyboard, btnRow)
			}
		}
		if len(keyboard) > 0 {
			o.replyMarkup = inlineKeyboardMarkup{InlineKeyboard: keyboard}
		}
	}
}

// WithBaseURL points this call at a self-hosted telegram-bot-api server
// (e.g. "http://10.0.0.5:8081") instead of api.telegram.org. Empty is a no-op.
func WithBaseURL(baseURL string) SendOption {
	return func(o *sendOptions) {
		if baseURL != "" {
			o.baseURL = strings.TrimSuffix(baseURL, "/")
		}
	}
}

// SendMessage sends a Telegram message via a stateless HTTP call to the Bot
// API — it needs only the bot token, not a running long-poll BotService
// instance, so it can be called from any process (the job engine included,
// which is deployed separately from the server that runs the long-poller).
func SendMessage(ctx context.Context, botToken string, chatID int64, text string, opts ...SendOption) error {
	return sendMessage(ctx, botToken, chatID, text, opts...)
}

// SendMessageToChat is SendMessage for a chat named by "@username" (a public
// channel or group the bot can post to) instead of a numeric id.
func SendMessageToChat(ctx context.Context, botToken, chat, text string, opts ...SendOption) error {
	return sendMessage(ctx, botToken, chat, text, opts...)
}

func sendMessage(ctx context.Context, botToken string, chatID any, text string, opts ...SendOption) error {
	if botToken == "" {
		return fmt.Errorf("telegram bot token is empty")
	}
	o := &sendOptions{}
	for _, opt := range opts {
		opt(o)
	}
	req := sendMessageRequest{ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: o.replyMarkup}
	return call(ctx, o.baseURL, botToken, "sendMessage", req, nil)
}

// GetBotUsername calls the Bot API's getMe endpoint to verify a token is
// valid and return the bot's username, without requiring any bound chat.
func GetBotUsername(ctx context.Context, botToken string, opts ...SendOption) (string, error) {
	if botToken == "" {
		return "", fmt.Errorf("telegram bot token is empty")
	}
	o := &sendOptions{}
	for _, opt := range opts {
		opt(o)
	}
	var result struct {
		Username string `json:"username"`
	}
	if err := call(ctx, o.baseURL, botToken, "getMe", nil, &result); err != nil {
		return "", err
	}
	return result.Username, nil
}

func call(ctx context.Context, baseURL, botToken, method string, body any, into any) error {
	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	url := fmt.Sprintf("%s/bot%s/%s", baseURL, botToken, method)
	httpMethod := http.MethodGet
	if body != nil {
		httpMethod = http.MethodPost
	}
	httpReq, err := http.NewRequestWithContext(ctx, httpMethod, url, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := senderHTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode telegram %s response: %w", method, err)
	}
	if !result.OK {
		return fmt.Errorf("telegram %s failed: %s", method, result.Description)
	}
	if into != nil && len(result.Result) > 0 {
		if err := json.Unmarshal(result.Result, into); err != nil {
			return fmt.Errorf("decode telegram %s result: %w", method, err)
		}
	}
	return nil
}
