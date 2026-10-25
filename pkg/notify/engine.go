package notify

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/notify/template"
	"strings"
	"sync"
	"time"
)

type DeliveryStatus string

const (
	StatusPending            DeliveryStatus = "PENDING"
	StatusSent               DeliveryStatus = "SENT"
	StatusAcked              DeliveryStatus = "ACKED"
	StatusFailed             DeliveryStatus = "FAILED"
	StatusDeduped            DeliveryStatus = "DEDUPED"
	StatusCooldownSuppressed DeliveryStatus = "COOLDOWN_SUPPRESSED"
	StatusEscalated          DeliveryStatus = "ESCALATED"
)

// DeliveryRequest defines a single alert or message to dispatch.
type DeliveryRequest struct {
	NotificationID  string
	DedupKey        string
	DedupWindow     time.Duration
	Cooldown        time.Duration
	Channel         string
	Recipient       string
	Title           string
	Content         string
	Severity        string // INFO, WARN, P1, P0
	EscalateChannel string // Secondary channel if unacked (e.g. "voice", "webhook")
	Buttons         []template.Button
}

// DeliveryResult contains the outcome of a dispatch attempt.
type DeliveryResult struct {
	NotificationID string
	Status         DeliveryStatus
	Reason         string
	DeliveredAt    time.Time
}

// Engine owns notification dispatch, deduplication, cooldowns, ACKs, and escalation.
type Engine struct {
	dao       *rdb.NotificationDAO
	mu        sync.RWMutex
	cooldowns map[string]time.Time // key: channel + ":" + recipient -> last sent time
	now       func() time.Time
}

func NewEngine(dao *rdb.NotificationDAO) *Engine {
	return &Engine{
		dao:       dao,
		cooldowns: make(map[string]time.Time),
		now:       time.Now,
	}
}

// Dispatch processes and delivers a notification according to dedup, cooldown, and channel rules.
func (e *Engine) Dispatch(ctx context.Context, req DeliveryRequest) (*DeliveryResult, error) {
	if req.NotificationID == "" {
		req.NotificationID = fmt.Sprintf("notif_%d", e.now().UnixNano())
	}
	if req.Severity == "" {
		req.Severity = "INFO"
	}
	if req.DedupWindow <= 0 {
		req.DedupWindow = 5 * time.Minute
	}

	if req.Channel == "" {
		return &DeliveryResult{
			NotificationID: req.NotificationID,
			Status:         StatusFailed,
			Reason:         "notification channel is required",
			DeliveredAt:    e.now().UTC(),
		}, errors.New("notification channel is required")
	}
	if req.Recipient == "" {
		return &DeliveryResult{
			NotificationID: req.NotificationID,
			Status:         StatusFailed,
			Reason:         "notification recipient is required",
			DeliveredAt:    e.now().UTC(),
		}, errors.New("notification recipient is required")
	}

	// 1. Deduplication check
	if req.DedupKey != "" && e.dao != nil {
		if e.dao.IsDeduped(ctx, req.DedupKey, req.DedupWindow) {
			logger.Debug("Notification suppressed by deduplication",
				logger.String("dedup_key", req.DedupKey),
				logger.String("channel", req.Channel),
			)
			return &DeliveryResult{
				NotificationID: req.NotificationID,
				Status:         StatusDeduped,
				Reason:         "duplicate alert within dedup window",
				DeliveredAt:    e.now().UTC(),
			}, nil
		}
	}

	// 2. Cooldown check
	cdKey := fmt.Sprintf("%s:%s", strings.ToLower(req.Channel), strings.ToLower(req.Recipient))
	if req.Cooldown > 0 {
		e.mu.RLock()
		lastSent, exists := e.cooldowns[cdKey]
		e.mu.RUnlock()
		if exists && e.now().Sub(lastSent) < req.Cooldown {
			logger.Debug("Notification suppressed by cooldown",
				logger.String("channel", req.Channel),
				logger.String("recipient", req.Recipient),
			)
			return &DeliveryResult{
				NotificationID: req.NotificationID,
				Status:         StatusCooldownSuppressed,
				Reason:         "recipient in cooldown window",
				DeliveredAt:    e.now().UTC(),
			}, nil
		}
	}

	// 3. Resolve Channel
	channelNotifier, ok := Get(req.Channel)
	if !ok {
		err := fmt.Errorf("notification channel %q is not registered", req.Channel)
		return &DeliveryResult{
			NotificationID: req.NotificationID,
			Status:         StatusFailed,
			Reason:         err.Error(),
			DeliveredAt:    e.now().UTC(),
		}, err
	}

	// 4. Send Message
	msg := Message{
		Title:   req.Title,
		Content: req.Content,
		Buttons: req.Buttons,
	}

	sendErr := channelNotifier.Send(ctx, req.Recipient, msg)

	// Record in memory cooldown
	if sendErr == nil && req.Cooldown > 0 {
		e.mu.Lock()
		e.cooldowns[cdKey] = e.now()
		e.mu.Unlock()
	}

	status := StatusSent
	errMsg := ""
	if sendErr != nil {
		status = StatusFailed
		errMsg = sendErr.Error()
	}

	// 5. Persist delivery audit log
	if e.dao != nil {
		record := &model.NotificationDelivery{
			NotificationID:  req.NotificationID,
			DedupKey:        req.DedupKey,
			Channel:         req.Channel,
			Recipient:       req.Recipient,
			Title:           req.Title,
			Content:         req.Content,
			Severity:        req.Severity,
			Status:          string(status),
			EscalateChannel: req.EscalateChannel,
			SentAt:          e.now().UTC(),
			LastError:       errMsg,
		}
		_ = e.dao.RecordDelivery(ctx, record)
	}

	if sendErr != nil {
		return &DeliveryResult{
			NotificationID: req.NotificationID,
			Status:         StatusFailed,
			Reason:         errMsg,
			DeliveredAt:    e.now().UTC(),
		}, sendErr
	}

	return &DeliveryResult{
		NotificationID: req.NotificationID,
		Status:         StatusSent,
		DeliveredAt:    e.now().UTC(),
	}, nil
}

// Acknowledge marks an alert as acknowledged by an operator.
func (e *Engine) Acknowledge(ctx context.Context, notificationID string) error {
	if e.dao == nil {
		return nil
	}
	return e.dao.AckNotification(ctx, notificationID)
}

// CheckAndEscalate scans unacknowledged critical alerts (P0/P1) and triggers secondary channels.
func (e *Engine) CheckAndEscalate(ctx context.Context, unackedOlderThan time.Duration) (int, error) {
	if e.dao == nil {
		return 0, nil
	}
	unackedP0, err := e.dao.GetUnackedDeliveries(ctx, "P0", unackedOlderThan)
	if err != nil {
		return 0, fmt.Errorf("query unacked P0 notifications: %w", err)
	}
	unackedP1, err := e.dao.GetUnackedDeliveries(ctx, "P1", unackedOlderThan)
	if err != nil {
		return 0, fmt.Errorf("query unacked P1 notifications: %w", err)
	}

	candidates := append(unackedP0, unackedP1...)
	escalatedCount := 0

	for _, item := range candidates {
		if item.EscalateChannel == "" {
			continue
		}
		secNotifier, ok := Get(item.EscalateChannel)
		if !ok {
			logger.Warn("Escalation channel not found", logger.String("channel", item.EscalateChannel))
			continue
		}

		logger.Warn("Escalating unacknowledged critical alert!",
			logger.String("notification_id", item.NotificationID),
			logger.String("severity", item.Severity),
			logger.String("escalate_channel", item.EscalateChannel),
		)

		escalatedTitle := fmt.Sprintf("[ESCALATED %s] %s", item.Severity, item.Title)
		escErr := secNotifier.Send(ctx, item.Recipient, Message{
			Title:   escalatedTitle,
			Content: fmt.Sprintf("Alert not ACKed after %v. Original message: %s", unackedOlderThan, item.Content),
		})

		if escErr == nil {
			_ = e.dao.UpdateStatus(ctx, item.NotificationID, string(StatusEscalated), "")
			escalatedCount++
		} else {
			_ = e.dao.UpdateStatus(ctx, item.NotificationID, string(StatusFailed), escErr.Error())
		}
	}

	return escalatedCount, nil
}
