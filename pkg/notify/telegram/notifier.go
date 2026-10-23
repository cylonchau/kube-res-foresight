// Package telegram implements notify.Notifier for the Telegram channel: it
// broadcasts to every backend user with an active TelegramBinding, using the
// bot token configured in SysConfig (see pkg/telegram.ConfigBot*).
//
// This lives under pkg/notify rather than inside pkg/telegram itself: it
// depends on pkg/dao/rdb and pkg/workflow, and pkg/telegram/bot.go already
// imports pkg/notify — putting the implementation in pkg/telegram would
// import notify right back into it, an import cycle. Naming the package
// "telegram" (same as its sibling pkg/telegram) is intentional, matching
// every other notify/vendor channel subpackage's convention of being named
// after the thing it implements; callers needing both import pkg/telegram
// under an alias (see the botTelegram alias below).
package telegram

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	botTelegram "github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"strconv"
	"strings"
	"time"
)

// Notifier is the Telegram implementation of notify.Notifier.
type Notifier struct {
	configDAO  *rdb.SysConfigDAO
	bindingDAO *rdb.TelegramBindingDAO
	bots       *rdb.TelegramBotDAO
	accounts   *rdb.TelegramClientDAO
	requests   *rdb.TelegramAgentRequestDAO
}

// sendTimeout bounds one send through the TG Agent.
const sendTimeout = 30 * time.Second

// WithClientAccount makes the notifier send through a logged-in Telegram user
// account (executed by the TG Agent) whenever one is connected; a bot is only the
// fallback. A normal account needs no bot to message a user.
func WithClientAccount(n notify.Notifier, accounts *rdb.TelegramClientDAO, requests *rdb.TelegramAgentRequestDAO) notify.Notifier {
	if t, ok := n.(*Notifier); ok {
		t.accounts, t.requests = accounts, requests
	}
	return n
}

// sendAsAccount sends through the first connected user account. sent is false when
// no account is connected, so the caller can fall back to a bot.
func (n *Notifier) sendAsAccount(ctx context.Context, accountID int64, recipient, text string) (sent bool, err error) {
	if n.accounts == nil || n.requests == nil {
		return false, nil
	}
	account, err := n.accounts.FirstConnectedAccount(ctx, accountID)
	if err != nil {
		return false, err
	}
	if account == nil && accountID != 0 {
		return true, fmt.Errorf("the chosen Telegram account #%d is not connected", accountID)
	}
	if account == nil {
		return false, nil
	}
	if account.SendBlocked(time.Now()) {
		return true, fmt.Errorf("%w: the chosen Telegram account #%d cannot send until %s: %s", ErrAccountLimited, account.ID, account.SendBlockedUntil.Local().Format("2006-01-02 15:04"), account.SendBlockedReason)
	}
	req, err := n.requests.Enqueue(ctx, model.AgentReqSendMessage, account.ID, map[string]string{"recipient": recipient, "text": text})
	if err != nil {
		return true, err
	}
	if _, err = n.requests.Wait(ctx, req.ID, sendTimeout); err != nil {
		return true, fmt.Errorf("send through Telegram account #%d: %w", account.ID, err)
	}
	return true, nil
}

func New(configDAO *rdb.SysConfigDAO, bindingDAO *rdb.TelegramBindingDAO) notify.Notifier {
	return &Notifier{configDAO: configDAO, bindingDAO: bindingDAO}
}

// NewWithBots sends through the first enabled bot of the telegram_bot table,
// using the legacy SysConfig bot only when no bot row is enabled.
func NewWithBots(configDAO *rdb.SysConfigDAO, bindingDAO *rdb.TelegramBindingDAO, bots *rdb.TelegramBotDAO) notify.Notifier {
	return &Notifier{configDAO: configDAO, bindingDAO: bindingDAO, bots: bots}
}

// ErrAccountLimited means Telegram limited the sending of the user account (PEER_FLOOD): it is paused for a while.
var ErrAccountLimited = errors.New("the Telegram account is limited by Telegram")

// Settings of how Telegram messages are sent (editable on the system settings page).
const (
	// ConfigSender is auto (a connected user account sends, the bot takes over when the account is limited or
	// missing) or bot (only the bot sends; user accounts are never used for notifications and forwards).
	ConfigSender = "notify.telegram.sender"
	SenderAuto   = "auto"
	SenderBot    = "bot"
)

// SettingDefaults are the settings with their starting values and what they mean, for the migration to write.
func SettingDefaults() []struct{ Key, Value, Name, Remark string } {
	return []struct{ Key, Value, Name, Remark string }{
		{ConfigSender, SenderAuto, "Telegram Message Sender", "auto: a connected user account sends and the bot takes over when Telegram limits the account (PEER_FLOOD) or no account is connected; bot: only the bot sends (the person must have started the bot, which a bound user has)"},
	}
}

// useAccount says whether messages may go through a user account at all (not when the setting says bot only).
func (n *Notifier) useAccount(ctx context.Context) bool {
	if n.configDAO == nil {
		return true
	}
	v, err := n.configDAO.GetValueOrDefault(ctx, ConfigSender, SenderAuto)
	return err != nil || strings.ToLower(strings.TrimSpace(v)) != SenderBot
}

// isLimited says whether a failure is Telegram limiting the account: refused here because it is paused, or answered
// PEER_FLOOD by the agent (the agent's error arrives as text).
func isLimited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAccountLimited) {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "PEER_FLOOD") || strings.Contains(text, "cannot send until")
}

func (n *Notifier) Channel() string { return "telegram" }

func (n *Notifier) Recipients(ctx context.Context) ([]string, error) {
	enabled := false
	if n.bots != nil {
		if _, _, _, ok, err := n.bots.DefaultSender(ctx); err != nil {
			return nil, err
		} else {
			enabled = ok
		}
	}
	if !enabled {
		var err error
		if enabled, _, err = n.enabledAndToken(ctx); err != nil || !enabled {
			return nil, err
		}
	}

	bindings, err := n.bindingDAO.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	recipients := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		recipients = append(recipients, strconv.FormatInt(binding.TelegramUserID, 10))
	}
	return recipients, nil
}

// Send delegates to the same workflow.ChatDriver (Sender) every other
// outbound Telegram message goes through — see pkg/telegram.TelegramChatDriver
// — instead of building its own send request, so a broadcast notification
// and a workflow-pipeline notification can't drift on how a Telegram
// message actually gets built and delivered.
// ErrNoBot means no Telegram bot is enabled, so there is nothing to send with. It is
// an error on purpose: a delivery that sent nothing must not be recorded as sent.
var ErrNoBot = errors.New("no Telegram account is connected and no bot is enabled: connect a user account under Telegram > Accounts, or enable a bot")

// ErrBadRecipient means the recipient is neither a numeric chat id nor an @username.
var ErrBadRecipient = errors.New("a Telegram recipient must be a numeric chat id (a user who has started the bot) or an @username of a channel or group the bot can post to")

// parseRecipient accepts a numeric chat id or an @username.
func parseRecipient(recipient string) (chatID int64, username string, err error) {
	recipient = strings.TrimSpace(recipient)
	if strings.HasPrefix(recipient, "@") && len(recipient) > 1 {
		return 0, recipient, nil
	}
	chatID, err = strconv.ParseInt(recipient, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w (got %q)", ErrBadRecipient, recipient)
	}
	return chatID, "", nil
}

// credentials returns the bot to send with: the first enabled bot of the telegram_bot
// table, or the legacy SysConfig bot when no row is enabled.
func (n *Notifier) credentials(ctx context.Context) (token, baseURL, miniApp string, err error) {
	if n.bots != nil {
		t, b, m, ok, err := n.bots.DefaultSender(ctx)
		if err != nil {
			return "", "", "", err
		}
		if ok {
			return t, b, m, nil
		}
	}
	enabled, token, err := n.enabledAndToken(ctx)
	if err != nil {
		return "", "", "", err
	}
	if !enabled {
		return "", "", "", ErrNoBot
	}
	baseURL, _ = n.configDAO.GetValueOrDefault(ctx, botTelegram.ConfigBotAPIBaseURL, "")
	miniApp, _ = n.configDAO.GetValueOrDefault(ctx, botTelegram.ConfigBotMiniAppURL, "")
	return token, baseURL, miniApp, nil
}

// Send delegates to the same workflow.ChatDriver (Sender) every other
// outbound Telegram message goes through, so a broadcast notification and a
// workflow-pipeline notification can't drift on how a message is built and
// delivered. It fails (rather than silently doing nothing) when no bot is
// enabled or the recipient is not usable.
func (n *Notifier) Send(ctx context.Context, recipient string, msg notify.Message) error {
	chatID, username, err := parseRecipient(recipient)
	if err != nil {
		return err
	}
	var accountErr error
	if n.useAccount(ctx) {
		var sent bool
		sent, accountErr = n.sendAsAccount(ctx, msg.SendAccountID, recipient, msg.Content)
		if sent && accountErr == nil {
			return nil
		}
		// A user account that Telegram limited (PEER_FLOOD) or that did not answer does not stop the message: the
		// bot sends it, and while the account stays limited every later message goes by the bot as well. Any other
		// failure of the account is reported as it is.
		if sent && !errors.Is(accountErr, rdb.ErrAgentTimeout) && !isLimited(accountErr) {
			return accountErr
		}
	}
	token, baseURL, miniApp, err := n.credentials(ctx)
	if err != nil {
		if accountErr != nil && errors.Is(err, ErrNoBot) {
			return accountErr // the account failed and there is no bot to take over: say why the account failed
		}
		return err
	}
	driver := botTelegram.NewChatDriver(token, miniApp, baseURL)
	buttons := workflow.ButtonsFromTemplate(msg.Buttons)
	if username != "" {
		return driver.SendToChat(ctx, username, msg.Content, buttons)
	}
	return driver.SendMessage(ctx, chatID, msg.Content, buttons)
}

func (n *Notifier) enabledAndToken(ctx context.Context) (bool, string, error) {
	enabled, err := n.configDAO.GetBool(ctx, botTelegram.ConfigBotEnabled, false)
	if err != nil || !enabled {
		enabled, err = n.configDAO.GetBool(ctx, botTelegram.ConfigLegacyBotEnabled, false)
	}
	if err != nil || !enabled {
		return false, "", err
	}
	token, err := n.configDAO.GetValue(ctx, botTelegram.ConfigBotToken)
	if err != nil || token == "" {
		token, err = n.configDAO.GetValue(ctx, botTelegram.ConfigLegacyBotToken)
	}
	if err != nil {
		return false, "", err
	}
	if token == "" {
		return false, "", fmt.Errorf("telegram notification is enabled but bot token is empty")
	}
	return true, token, nil
}
