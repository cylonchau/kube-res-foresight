package rdb

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"gorm.io/gorm"
	"time"
)

// ErrAgentTimeout means no Telegram Agent finished the request in time.
var ErrAgentTimeout = errors.New("the Telegram Agent did not answer; make sure it is running")

// agentRequestStale is how long a claimed request may run before it is failed.
const agentRequestStale = 2 * time.Minute

// agentPollInterval is how often Wait re-reads a request.
const agentPollInterval = 200 * time.Millisecond

type TelegramAgentRequestDAO struct{ db *gorm.DB }

func NewTelegramAgentRequestDAO(db *gorm.DB) *TelegramAgentRequestDAO {
	return &TelegramAgentRequestDAO{db: db}
}

// Enqueue queues a request for the Telegram Agent. payload is encrypted.
func (d *TelegramAgentRequestDAO) Enqueue(ctx context.Context, kind string, targetID int64, payload any) (*model.TelegramAgentRequest, error) {
	row := &model.TelegramAgentRequest{Kind: kind, TargetID: targetID, Status: model.AgentReqPending}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		if row.PayloadEncrypted, err = cryptoutils.EncryptValue(string(raw)); err != nil {
			return nil, err
		}
	}
	if err := d.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// Payload decodes the encrypted payload of a claimed request into out.
func (d *TelegramAgentRequestDAO) Payload(row *model.TelegramAgentRequest, out any) error {
	if row.PayloadEncrypted == "" {
		return nil
	}
	plain, err := cryptoutils.DecryptValue(row.PayloadEncrypted)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(plain), out)
}

// Claim atomically takes the oldest pending request, or returns nil.
func (d *TelegramAgentRequestDAO) Claim(ctx context.Context) (*model.TelegramAgentRequest, error) {
	return d.ClaimFor(ctx, "", nil)
}

// requestScan bounds how many pending requests one claim looks through for one it may take.
const requestScan = 50

// ClaimFor takes the oldest pending request the agent may run. owner says which agent a request belongs to
// ("" = any agent may take it): an account's requests belong to the agent that runs the account, so with
// several agents a request is never taken by one that does not hold the account.
func (d *TelegramAgentRequestDAO) ClaimFor(ctx context.Context, agentID string, owner func(*model.TelegramAgentRequest) (string, error)) (*model.TelegramAgentRequest, error) {
	var pending []model.TelegramAgentRequest
	if err := d.db.WithContext(ctx).Where("status = ?", model.AgentReqPending).Order("id ASC").Limit(requestScan).Find(&pending).Error; err != nil {
		return nil, err
	}
	for i := range pending {
		row := pending[i]
		if owner != nil {
			who, err := owner(&row)
			if err != nil {
				return nil, err
			}
			if who != "" && who != agentID {
				continue
			}
		}
		now := time.Now()
		res := d.db.WithContext(ctx).Model(&model.TelegramAgentRequest{}).
			Where("id = ? AND status = ?", row.ID, model.AgentReqPending).
			Updates(map[string]any{"status": model.AgentReqRunning, "claimed_at": &now})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 { // another agent may have taken it first
			row.Status, row.ClaimedAt = model.AgentReqRunning, &now
			return &row, nil
		}
	}
	return nil, nil
}

// Finish records the outcome and wipes the payload so codes and passwords do
// not stay in the database.
func (d *TelegramAgentRequestDAO) Finish(ctx context.Context, id int64, result any, runErr error) error {
	now := time.Now()
	updates := map[string]any{"status": model.AgentReqDone, "finished_at": &now, "payload_encrypted": ""}
	if runErr != nil {
		updates["status"] = model.AgentReqFailed
		updates["error"] = runErr.Error()
	}
	if result != nil {
		if raw, err := json.Marshal(result); err == nil {
			updates["result_json"] = string(raw)
		}
	}
	return d.db.WithContext(ctx).Model(&model.TelegramAgentRequest{}).Where("id = ?", id).Updates(updates).Error
}

// Get reads one request.
func (d *TelegramAgentRequestDAO) Get(ctx context.Context, id int64) (*model.TelegramAgentRequest, error) {
	var row model.TelegramAgentRequest
	if err := d.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Wait blocks until the request is finished or timeout elapses. A failed
// request is returned as an error carrying the Agent's message.
func (d *TelegramAgentRequestDAO) Wait(ctx context.Context, id int64, timeout time.Duration) (*model.TelegramAgentRequest, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(agentPollInterval)
	defer tick.Stop()
	for {
		row, err := d.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		switch row.Status {
		case model.AgentReqDone:
			return row, nil
		case model.AgentReqFailed:
			return row, errors.New(row.Error)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			// Nobody answered: drop an unclaimed request so it cannot run later.
			d.db.WithContext(context.Background()).Model(&model.TelegramAgentRequest{}).
				Where("id = ? AND status = ?", id, model.AgentReqPending).
				Updates(map[string]any{"status": model.AgentReqFailed, "error": ErrAgentTimeout.Error(), "payload_encrypted": ""})
			return nil, ErrAgentTimeout
		case <-tick.C:
		}
	}
}

// Sweep fails requests stuck in running (the agent died) and deletes finished
// ones older than keep.
func (d *TelegramAgentRequestDAO) Sweep(ctx context.Context, now time.Time, keep time.Duration) error {
	if err := d.db.WithContext(ctx).Model(&model.TelegramAgentRequest{}).
		Where("status = ? AND claimed_at < ?", model.AgentReqRunning, now.Add(-agentRequestStale)).
		Updates(map[string]any{"status": model.AgentReqFailed, "error": "the Telegram Agent stopped while handling this request", "payload_encrypted": ""}).Error; err != nil {
		return err
	}
	return d.db.WithContext(ctx).Where("status IN ? AND updated_at < ?", []string{model.AgentReqDone, model.AgentReqFailed}, now.Add(-keep)).
		Delete(&model.TelegramAgentRequest{}).Error
}
