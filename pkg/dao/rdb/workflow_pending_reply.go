package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"gorm.io/gorm"
	"time"
)

// pendingReplyTTL is how long a ForceReply prompt stays valid — long enough
// for someone to actually type an answer, short enough that a stale prompt
// from days ago can't be accidentally consumed as today's reply.
const pendingReplyTTL = 15 * time.Minute

type WorkflowPendingReplyDAO struct {
	db *gorm.DB
}

func NewWorkflowPendingReplyDAO(db *gorm.DB) *WorkflowPendingReplyDAO {
	return &WorkflowPendingReplyDAO{db: db}
}

// Begin records that telegramUserID was just prompted for instanceID/event.
// One pending reply per user: any previous prompt is replaced, matching the
// "last prompt wins" UX (tapping a second decision button before replying
// to the first cancels the first prompt).
func (dao *WorkflowPendingReplyDAO) Begin(ctx context.Context, telegramUserID, instanceID int64, event string) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("telegram_user_id = ?", telegramUserID).Delete(&model.WorkflowPendingReply{}).Error; err != nil {
			return err
		}
		return tx.Create(&model.WorkflowPendingReply{
			TelegramUserID: telegramUserID,
			InstanceID:     instanceID,
			Event:          event,
			ExpiresAt:      time.Now().Add(pendingReplyTTL),
		}).Error
	})
}

// Consume looks up and deletes telegramUserID's pending reply, if any and
// not expired. ok is false (with no error) when there's nothing pending or
// it already expired — both are normal "this message is not a captured
// reply" outcomes for the caller, not failures.
func (dao *WorkflowPendingReplyDAO) Consume(ctx context.Context, telegramUserID int64) (instanceID int64, event string, ok bool, err error) {
	err = dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Opportunistic cleanup of this user's own stale rows — avoids an
		// unbounded table for users who get prompted a lot and never reply.
		if delErr := tx.Where("telegram_user_id = ? AND expires_at <= ?", telegramUserID, time.Now()).
			Delete(&model.WorkflowPendingReply{}).Error; delErr != nil {
			return delErr
		}

		var pending model.WorkflowPendingReply
		findErr := tx.Where("telegram_user_id = ?", telegramUserID).First(&pending).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if findErr != nil {
			return findErr
		}

		instanceID = pending.InstanceID
		event = pending.Event
		ok = true
		return tx.Delete(&pending).Error
	})
	return instanceID, event, ok, err
}
