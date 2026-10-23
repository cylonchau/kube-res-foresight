package telegramclient

import (
	"context"
	"errors"
	"fmt"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"strconv"
	"strings"
)

// ErrUserNotReachable means the account has no chat with, and no contact entry
// for, the numeric user id: Telegram only lets an account message a user it has
// already met (a dialog, a contact) or one that is addressed by @username.
var ErrUserNotReachable = errors.New("this account cannot reach that user id: it has no chat or contact with them (use their @username, or message them once from this account)")

// ErrPeerFlood means Telegram refused the message with PEER_FLOOD: the account has sent too many messages to people
// it has no chat with. It is a limit on the account, not on the recipient, and asking again only prolongs it.
var ErrPeerFlood = errors.New("Telegram limited this account (PEER_FLOOD: too many messages to people it has no chat with)")

var errFound = errors.New("found")

// sendError turns Telegram's PEER_FLOOD into ErrPeerFlood and leaves other errors as they are.
func sendError(err error) error {
	if err != nil && tgerr.Is(err, "PEER_FLOOD") {
		return fmt.Errorf("%w: %v", ErrPeerFlood, err)
	}
	return err
}

// SendMessage sends text from a connected user account. recipient is a numeric
// user / chat id or a public "@username".
func (m *Manager) SendMessage(ctx context.Context, accountID int64, recipient, text string) error {
	m.mu.RLock()
	running := m.clients[accountID]
	m.mu.RUnlock()
	if running == nil {
		return ErrNotConnected
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the message is empty")
	}
	api := running.client.API()
	sender := message.NewSender(api)
	recipient = strings.TrimSpace(recipient)
	if strings.HasPrefix(recipient, "@") {
		_, err := sender.Resolve(strings.TrimPrefix(recipient, "@")).Text(ctx, text)
		return sendError(err)
	}
	id, err := strconv.ParseInt(recipient, 10, 64)
	if err != nil {
		return fmt.Errorf("recipient %q is neither a user id nor an @username", recipient)
	}
	peer, err := findUserPeer(ctx, api, id)
	if err != nil {
		return err
	}
	_, err = sender.To(peer).Text(ctx, text)
	return sendError(err)
}

// findUserPeer looks the user up among the account's dialogs, then contacts.
func findUserPeer(ctx context.Context, api *tg.Client, userID int64) (tg.InputPeerClass, error) {
	var peer tg.InputPeerClass
	err := query.GetDialogs(api).BatchSize(100).ForEach(ctx, func(_ context.Context, e dialogs.Elem) error {
		if p, ok := e.Peer.(*tg.InputPeerUser); ok && p.UserID == userID {
			peer = p
			return errFound
		}
		return nil
	})
	if peer != nil {
		return peer, nil
	}
	if err != nil && !errors.Is(err, errFound) {
		return nil, fmt.Errorf("list Telegram dialogs: %w", err)
	}
	res, err := api.ContactsGetContacts(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("list Telegram contacts: %w", err)
	}
	if contacts, ok := res.(*tg.ContactsContacts); ok {
		for _, u := range contacts.Users {
			if user, ok := u.(*tg.User); ok && user.ID == userID {
				return user.AsInputPeer(), nil
			}
		}
	}
	return nil, ErrUserNotReachable
}
