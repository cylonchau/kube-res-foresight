package telegramagent

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegramclient"
	"time"
)

// sendBlockFor is how long an account is left alone after Telegram answers PEER_FLOOD. Telegram does not say how
// long the limit lasts; an hour keeps the account from making it worse while a message is not held up for long.
const sendBlockFor = time.Hour

var (
	errUnsupportedMode = errors.New("only polling connections are supported")
	errBotNotRunning   = errors.New("the bot is not running; enable it and check its token")
)

// requestTimeout bounds one queued request.
const requestTimeout = 60 * time.Second

type verifyPayload struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

type sendPayload struct {
	Recipient string `json:"recipient"`
	Text      string `json:"text"`
}

type subscribePayload struct {
	AccountID   int64  `json:"account_id"`
	ChannelLink string `json:"channel_link"`
	Join        bool   `json:"join"`
}

// drainRequests runs the queued requests one after another until none is left.
func (r *Runtime) drainRequests(ctx context.Context) {
	for ctx.Err() == nil {
		req, err := r.requests.ClaimFor(ctx, r.agentID, r.requestOwner)
		if err != nil {
			logger.Warn("Telegram Agent could not claim a request", logger.Err(err))
			return
		}
		if req == nil {
			return
		}
		result, runErr := r.execute(ctx, req)
		if err := r.requests.Finish(ctx, req.ID, result, runErr); err != nil {
			logger.Warn("Telegram Agent could not record a request result", logger.Int64("request_id", req.ID), logger.Err(err))
		}
	}
}

// requestOwner is the agent a request belongs to: the one that runs the account it is about. Requests about an
// account no agent holds yet, and bot requests (every agent runs the bots), belong to nobody in particular.
func (r *Runtime) requestOwner(req *model.TelegramAgentRequest) (string, error) {
	accountID := int64(0)
	switch req.Kind {
	case model.AgentReqSendCode, model.AgentReqVerifyCode, model.AgentReqConnect, model.AgentReqDisconnect,
		model.AgentReqLogout, model.AgentReqSubscribe, model.AgentReqSendMessage:
		accountID = req.TargetID
	case model.AgentReqUnsubscribe:
		sub, err := r.messageDAO.GetSubscription(context.Background(), req.TargetID)
		if err != nil {
			return "", nil // gone: any agent answers with the error
		}
		accountID = sub.AccountID
	default:
		return "", nil
	}
	account, err := r.messageDAO.GetAccount(context.Background(), accountID)
	if err != nil {
		return "", nil
	}
	return account.AgentID, nil
}

// execute performs one request with the Telegram adapters.
func (r *Runtime) execute(ctx context.Context, req *model.TelegramAgentRequest) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	switch req.Kind {
	case model.AgentReqSendCode:
		return nil, r.clients.SendCode(ctx, req.TargetID)
	case model.AgentReqVerifyCode:
		var payload verifyPayload
		if err := r.requests.Payload(req, &payload); err != nil {
			return nil, err
		}
		return nil, r.clients.VerifyCode(ctx, req.TargetID, payload.Code, payload.Password)
	case model.AgentReqConnect:
		return nil, r.clients.StartAccount(req.TargetID)
	case model.AgentReqDisconnect:
		r.clients.StopAccount(req.TargetID)
		return nil, r.accounts.SetConnectionResult(ctx, req.TargetID, false, "")
	case model.AgentReqLogout:
		return nil, r.clients.Logout(ctx, req.TargetID)
	case model.AgentReqSubscribe:
		var payload subscribePayload
		if err := r.requests.Payload(req, &payload); err != nil {
			return nil, err
		}
		return r.clients.Subscribe(ctx, payload.AccountID, payload.ChannelLink, nil, payload.Join)
	case model.AgentReqUnsubscribe:
		return nil, r.clients.Unsubscribe(ctx, req.TargetID)
	case model.AgentReqSendMessage:
		var payload sendPayload
		if err := r.requests.Payload(req, &payload); err != nil {
			return nil, err
		}
		return nil, r.sendAsAccount(ctx, req.TargetID, payload)
	case model.AgentReqSyncBotCommand:
		if err := r.bots.ClearCommandsHash(ctx, req.TargetID); err != nil {
			return nil, err
		}
		return nil, r.syncCommands(ctx, req.TargetID)
	}
	return nil, fmt.Errorf("unknown request kind %q", req.Kind)
}

// sendAsAccount sends one message from an account. An account Telegram has limited is not asked at all; when
// Telegram answers PEER_FLOOD the account is paused so the next messages fail at once, with the reason.
func (r *Runtime) sendAsAccount(ctx context.Context, accountID int64, p sendPayload) error {
	if account, err := r.accounts.GetAccount(ctx, accountID); err == nil && account.SendBlocked(time.Now()) {
		return fmt.Errorf("account #%d cannot send until %s: %s", accountID, account.SendBlockedUntil.Local().Format("2006-01-02 15:04"), account.SendBlockedReason)
	}
	err := r.clients.SendMessage(ctx, accountID, p.Recipient, p.Text)
	if errors.Is(err, telegramclient.ErrPeerFlood) {
		until := time.Now().Add(sendBlockFor)
		if blockErr := r.accounts.BlockSending(ctx, accountID, until, err.Error()); blockErr != nil {
			logger.Warn("Telegram Agent could not record a sending limit", logger.Int64("account_id", accountID), logger.Err(blockErr))
		}
		logger.Warn("Telegram limited an account's sending (PEER_FLOOD); it is paused", logger.Int64("account_id", accountID), logger.String("until", until.Format(time.RFC3339)))
	}
	return err
}
