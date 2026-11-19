package rdb

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
)

// AsyncTaskDAO stores the async tasks of the users and what they reported. Reads and changes that a user asks for are bound to the owner.
type AsyncTaskDAO struct {
	db *gorm.DB
}

func NewAsyncTaskDAO(db *gorm.DB) *AsyncTaskDAO { return &AsyncTaskDAO{db: db} }

// Open returns the owner's queued or running task with the dedup key, nil when there is none.
func (dao *AsyncTaskDAO) Open(ctx context.Context, owner uint, kind, dedup string) (*model.AsyncTask, error) {
	if dedup == "" {
		return nil, nil
	}
	var t model.AsyncTask
	err := dao.db.WithContext(ctx).Where("owner_id = ? AND kind = ? AND dedup_key = ? AND status IN ?", owner, kind, dedup,
		[]string{model.AsyncQueued, model.AsyncRunning}).Limit(1).Find(&t).Error
	if err != nil || t.ID == 0 {
		return nil, err
	}
	return &t, nil
}

// CountOpen is how many queued or running tasks a user has.
func (dao *AsyncTaskDAO) CountOpen(ctx context.Context, owner uint) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("owner_id = ? AND status IN ?", owner, []string{model.AsyncQueued, model.AsyncRunning}).Count(&n).Error
	return n, err
}

func (dao *AsyncTaskDAO) Create(ctx context.Context, t *model.AsyncTask) error {
	return dao.db.WithContext(ctx).Create(t).Error
}

// Get returns a task by id for the worker, which acts for no user.
func (dao *AsyncTaskDAO) Get(ctx context.Context, id uint) (*model.AsyncTask, error) {
	var t model.AsyncTask
	if err := dao.db.WithContext(ctx).First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// GetOwned returns a task of the owner; gorm.ErrRecordNotFound when it is not theirs.
func (dao *AsyncTaskDAO) GetOwned(ctx context.Context, owner, id uint) (*model.AsyncTask, error) {
	var t model.AsyncTask
	if err := dao.db.WithContext(ctx).Where("id = ? AND owner_id = ?", id, owner).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// SetJob records the job of the task center that runs the task, and queues it.
func (dao *AsyncTaskDAO) SetJob(ctx context.Context, id uint, jobID int64) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"job_id": jobID, "status": model.AsyncQueued, "cancel_requested": false, "error": "", "progress": 0, "stage": "", "result": "", "finished_at": nil}).Error
}

// Start marks a queued task running; false when it was cancelled or finished meanwhile.
func (dao *AsyncTaskDAO) Start(ctx context.Context, id uint, at time.Time) (bool, error) {
	res := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ? AND cancel_requested = ?", id, []string{model.AsyncQueued, model.AsyncRunning}, false).
		UpdateColumns(map[string]any{"status": model.AsyncRunning, "started_at": at, "attempts": gorm.Expr("attempts + 1")})
	return res.RowsAffected > 0, res.Error
}

// Progress records how far a running task is.
func (dao *AsyncTaskDAO) Progress(ctx context.Context, id uint, percent int, stage string) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncRunning).
		UpdateColumns(map[string]any{"progress": percent, "stage": stage}).Error
}

// SetPartial keeps the text of the answer written so far, for the page that reads the running task.
func (dao *AsyncTaskDAO) SetPartial(ctx context.Context, id uint, text string) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncRunning).UpdateColumn("partial", text).Error
}

// Finish ends a task in a final state with its result or error.
func (dao *AsyncTaskDAO) Finish(ctx context.Context, id uint, status, result, errText string, at time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"partial": "", "status": status, "result": result, "error": errText, "finished_at": at, "progress": gorm.Expr("CASE WHEN ? = 'succeeded' THEN 100 ELSE progress END", status)}).Error
}

// RequestCancel asks a task of the owner to stop: a queued one is cancelled at once, a running one is told (its worker looks). It returns the
// state it found; gorm.ErrRecordNotFound when it is not theirs.
func (dao *AsyncTaskDAO) RequestCancel(ctx context.Context, owner, id uint, at time.Time) (*model.AsyncTask, error) {
	t, err := dao.GetOwned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	switch t.Status {
	case model.AsyncQueued:
		t.Status = model.AsyncCancelled
		err = dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncQueued).
			UpdateColumns(map[string]any{"status": model.AsyncCancelled, "cancel_requested": true, "finished_at": at}).Error
	case model.AsyncRunning:
		t.CancelRequested = true
		err = dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumn("cancel_requested", true).Error
	}
	return t, err
}

// CancelRequested says whether the owner asked the task to stop.
func (dao *AsyncTaskDAO) CancelRequested(ctx context.Context, id uint) (bool, error) {
	var flag []bool
	err := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).Limit(1).Pluck("cancel_requested", &flag).Error
	return len(flag) > 0 && flag[0], err
}

// AddEvent appends one event to a task, numbered after the last.
func (dao *AsyncTaskDAO) AddEvent(ctx context.Context, taskID uint, typ, data string, at time.Time) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var last []int
		if err := tx.Model(&model.AsyncTaskEvent{}).Where("task_id = ?", taskID).Order("seq DESC").Limit(1).Pluck("seq", &last).Error; err != nil {
			return err
		}
		seq := 1
		if len(last) > 0 {
			seq = last[0] + 1
		}
		return tx.Create(&model.AsyncTaskEvent{TaskID: taskID, Seq: seq, Type: typ, Data: data, CreatedAt: at}).Error
	})
}

// EventsAfter returns the events of a task of the owner with a number above after, at most limit.
func (dao *AsyncTaskDAO) EventsAfter(ctx context.Context, owner, id uint, after, limit int) ([]*model.AsyncTaskEvent, error) {
	if _, err := dao.GetOwned(ctx, owner, id); err != nil {
		return nil, err
	}
	var list []*model.AsyncTaskEvent
	err := dao.db.WithContext(ctx).Where("task_id = ? AND seq > ?", id, after).Order("seq").Limit(limit).Find(&list).Error
	return list, err
}

// ClearEvents removes the events of a task that is queued again.
func (dao *AsyncTaskDAO) ClearEvents(ctx context.Context, id uint) error {
	return dao.db.WithContext(ctx).Where("task_id = ?", id).Delete(&model.AsyncTaskEvent{}).Error
}

// PruneBefore deletes the finished tasks that ended before cutoff, with their events, at most limit of them, and returns how many went.
func (dao *AsyncTaskDAO) PruneBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var deleted int64
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Model(&model.AsyncTask{}).Where("finished_at IS NOT NULL AND finished_at < ?", cutoff).Order("finished_at").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("task_id IN ?", ids).Delete(&model.AsyncTaskEvent{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&model.AsyncTask{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}
