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

// Open returns the owner's queued, running or cancelling task with the dedup key, nil when there is none.
func (dao *AsyncTaskDAO) Open(ctx context.Context, owner uint, kind, dedup string) (*model.AsyncTask, error) {
	if dedup == "" {
		return nil, nil
	}
	var t model.AsyncTask
	err := dao.db.WithContext(ctx).Where("owner_id = ? AND kind = ? AND dedup_key = ? AND status IN ?", owner, kind, dedup, model.AsyncOpen).Limit(1).Find(&t).Error
	if err != nil || t.ID == 0 {
		return nil, err
	}
	return &t, nil
}

// CountOpen is how many tasks of a user have not ended.
func (dao *AsyncTaskDAO) CountOpen(ctx context.Context, owner uint) (int64, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("owner_id = ? AND status IN ?", owner, model.AsyncOpen).Count(&n).Error
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

// ListOwned returns the tasks of the owner, the newest first: the ones that have not ended and the ones that ended after since, of the kinds given
// (all kinds when none). It is how a page that was closed finds the tasks it queued.
func (dao *AsyncTaskDAO) ListOwned(ctx context.Context, owner uint, kinds []string, since time.Time, limit int) ([]*model.AsyncTask, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	q := dao.db.WithContext(ctx).Where("owner_id = ? AND (status IN ? OR created_at >= ?)", owner, model.AsyncOpen, since.UTC())
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}
	var list []*model.AsyncTask
	err := q.Order("id DESC").Limit(limit).Find(&list).Error
	return list, err
}

// SetJob records the job of the task center that runs the task, and queues it as a new run: what the last run left (progress, result, error,
// what it had done) is cleared and the attempts start again.
func (dao *AsyncTaskDAO) SetJob(ctx context.Context, id uint, jobID int64) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "job_id": jobID, "status": model.AsyncQueued, "cancel_requested": false, "error": "", "error_code": "", "retryable": false, "progress": 0, "stage": "",
			"result": "", "partial": "", "partial_result": "", "has_partial": false, "from_cache": false, "finished_at": nil, "expires_at": nil, "next_run_at": nil, "heartbeat_at": nil, "attempts": 0}).Error
}

// Start marks a queued task running; false when it was cancelled or finished meanwhile. The heartbeat starts with it.
func (dao *AsyncTaskDAO) Start(ctx context.Context, id uint, at time.Time) (bool, error) {
	res := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ? AND cancel_requested = ?", id, []string{model.AsyncQueued, model.AsyncRunning}, false).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncRunning, "started_at": at, "heartbeat_at": at, "next_run_at": nil, "attempts": gorm.Expr("attempts + 1")})
	return res.RowsAffected > 0, res.Error
}

// Heartbeat says the worker of a running task is alive.
func (dao *AsyncTaskDAO) Heartbeat(ctx context.Context, id uint, at time.Time) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ?", id, []string{model.AsyncRunning, model.AsyncCancelling}).
		UpdateColumn("heartbeat_at", at).Error
}

// RunState is what a worker looks at to know whether it should go on: whether its owner asked it to stop, which job the task belongs to now (a
// task that was put back and queued again belongs to another), and its state.
type RunState struct {
	CancelRequested bool
	JobID           int64
	Status          string
}

func (dao *AsyncTaskDAO) RunState(ctx context.Context, id uint) (RunState, error) {
	var rows []RunState
	err := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).Limit(1).Select("cancel_requested, job_id, status").Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return RunState{}, err
	}
	return rows[0], nil
}

// Progress records how far a running task is.
func (dao *AsyncTaskDAO) Progress(ctx context.Context, id uint, percent int, stage string) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ?", id, []string{model.AsyncRunning, model.AsyncCancelling}).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "progress": percent, "stage": stage}).Error
}

// SetPartial keeps the text of the answer written so far, for the page that reads the running task.
func (dao *AsyncTaskDAO) SetPartial(ctx context.Context, id uint, text string) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncRunning).UpdateColumns(map[string]any{"partial": text, "updated_at": time.Now().UTC()}).Error
}

// SetPartialResult keeps what the task has done so far (JSON), for the page and for the owner of a task that fails or is cancelled.
func (dao *AsyncTaskDAO) SetPartialResult(ctx context.Context, id uint, body string) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ?", id, []string{model.AsyncRunning, model.AsyncCancelling}).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "partial_result": body, "has_partial": true}).Error
}

// FinishInfo is how a task ended.
type FinishInfo struct {
	Status    string
	Result    string
	Error     string
	ErrorCode string
	// Retryable says a failure was of a kind that trying again can mend.
	Retryable bool
	At        time.Time
	ExpiresAt *time.Time
	// JobID, when not zero, is the job that ended the task: a job that no longer owns the task (it was put back and queued again) changes nothing.
	JobID int64
	// FromCache marks a result that was worked out before.
	FromCache bool
}

// Finish ends a task in a final state with its result or error. What it had done so far stays when it failed or was cancelled. It says whether the
// task was changed.
func (dao *AsyncTaskDAO) Finish(ctx context.Context, id uint, f FinishInfo) (bool, error) {
	updates := map[string]any{"updated_at": time.Now().UTC(), "partial": "", "status": f.Status, "result": f.Result, "error": f.Error, "error_code": f.ErrorCode, "retryable": f.Retryable, "finished_at": f.At,
		"expires_at": f.ExpiresAt, "from_cache": f.FromCache, "next_run_at": nil,
		"progress": gorm.Expr("CASE WHEN ? = 'succeeded' THEN 100 ELSE progress END", f.Status)}
	if f.Status == model.AsyncSucceeded {
		updates["partial_result"], updates["has_partial"] = "", false
	}
	q := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id)
	if f.JobID != 0 {
		q = q.Where("job_id = ?", f.JobID)
	}
	res := q.UpdateColumns(updates)
	return res.RowsAffected > 0, res.Error
}

// Requeue puts a running task back to queued for another attempt at the time given, with the reason it is tried again.
func (dao *AsyncTaskDAO) Requeue(ctx context.Context, id uint, jobID int64, errText, code string, next time.Time) (bool, error) {
	res := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND job_id = ? AND status IN ?", id, jobID, []string{model.AsyncRunning, model.AsyncCancelling}).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncQueued, "error": errText, "error_code": code, "retryable": true, "next_run_at": next, "partial": "", "stage": "", "progress": 0, "cancel_requested": false})
	return res.RowsAffected > 0, res.Error
}

// RequestCancel asks a task of the owner to stop: a queued one is cancelled at once, a running one becomes cancelling and its worker, which looks, stops
// it. It returns the state it found; gorm.ErrRecordNotFound when it is not theirs.
func (dao *AsyncTaskDAO) RequestCancel(ctx context.Context, owner, id uint, at time.Time, expires *time.Time) (*model.AsyncTask, error) {
	t, err := dao.GetOwned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	switch t.Status {
	case model.AsyncQueued:
		t.Status = model.AsyncCancelled
		err = dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncQueued).
			UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncCancelled, "cancel_requested": true, "finished_at": at, "expires_at": expires, "next_run_at": nil}).Error
	case model.AsyncRunning:
		t.Status, t.CancelRequested = model.AsyncCancelling, true
		err = dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status = ?", id, model.AsyncRunning).
			UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncCancelling, "cancel_requested": true}).Error
	}
	return t, err
}

// CancelRequested says whether the owner asked the task to stop.
func (dao *AsyncTaskDAO) CancelRequested(ctx context.Context, id uint) (bool, error) {
	st, err := dao.RunState(ctx, id)
	return st.CancelRequested, err
}

// StaleRunning returns the tasks that are running or cancelling and whose worker has not said it is alive since before: the process is gone.
func (dao *AsyncTaskDAO) StaleRunning(ctx context.Context, before time.Time, limit int) ([]*model.AsyncTask, error) {
	var list []*model.AsyncTask
	err := dao.db.WithContext(ctx).Where("status IN ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at < ?) OR (heartbeat_at IS NULL AND started_at IS NOT NULL AND started_at < ?))",
		[]string{model.AsyncRunning, model.AsyncCancelling}, before.UTC(), before.UTC()).Order("id").Limit(limit).Find(&list).Error
	return list, err
}

// Reclaim puts a task whose worker is gone back to queued, if it is still so (the worker may have answered while it was being found): the code says why.
// It says whether it did.
func (dao *AsyncTaskDAO) Reclaim(ctx context.Context, id uint, before time.Time, code, errText string) (bool, error) {
	res := dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ? AND status IN ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at < ?) OR (heartbeat_at IS NULL AND started_at IS NOT NULL AND started_at < ?))",
		id, []string{model.AsyncRunning, model.AsyncCancelling}, before.UTC(), before.UTC()).
		UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncQueued, "error": errText, "error_code": code, "retryable": true, "partial": "", "cancel_requested": false})
	return res.RowsAffected > 0, res.Error
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

// Expire drops the result of the finished tasks that ended before cutoff, at most limit of them: what they were given, what they answered and what they
// had done are cleared and their events deleted, and they become expired. Only the record stays. It returns how many it expired.
func (dao *AsyncTaskDAO) Expire(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var expired int64
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Model(&model.AsyncTask{}).Where("finished_at IS NOT NULL AND finished_at < ? AND status IN ?", cutoff.UTC(),
			[]string{model.AsyncSucceeded, model.AsyncFailed, model.AsyncCancelled}).Order("finished_at").Limit(limit).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("task_id IN ?", ids).Delete(&model.AsyncTaskEvent{}).Error; err != nil {
			return err
		}
		res := tx.Model(&model.AsyncTask{}).Where("id IN ?", ids).
			UpdateColumns(map[string]any{"updated_at": time.Now().UTC(), "status": model.AsyncExpired, "payload": "", "result": "", "partial": "", "partial_result": "", "has_partial": false})
		expired = res.RowsAffected
		return res.Error
	})
	return expired, err
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

// AttachJob records the job of the task center that runs the task now, without touching anything else of it.
func (dao *AsyncTaskDAO) AttachJob(ctx context.Context, id uint, jobID int64) error {
	return dao.db.WithContext(ctx).Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(map[string]any{"job_id": jobID, "updated_at": time.Now().UTC()}).Error
}
