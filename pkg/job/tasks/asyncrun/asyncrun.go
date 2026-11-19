// Package asyncrun runs the async tasks of the users (B1): work that takes longer than one request may. A task is a row (who asked, what, its state,
// progress and result) and a job of the task center that runs it, so it has the claim, the heartbeat and the recovery of every job: a task that
// was running when the platform stopped is run again when its claim has expired, and one that was queued is not lost. The handlers, one for each
// kind of task, are given by the packages that know the work.
package asyncrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// JobType is the job of the task center that runs one async task.
const JobType = "async.run"

// TargetType is the kind of target of a run in the task center.
const TargetType = "async_task"

// MaxOpenPerUser is how many queued or running tasks one user may have.
const MaxOpenPerUser = 5

// Errors of submitting a task.
var (
	ErrTooMany   = errors.New("there are already 5 tasks running or waiting: wait for one to finish or cancel one")
	ErrNotFinal  = errors.New("the task is still queued or running")
	ErrNotQueued = errors.New("the task cannot be changed in its state")
)

// Reporter is how a handler tells its owner how far it is.
type Reporter interface {
	// Progress says how far the task is (0-100) and what it is doing.
	Progress(percent int, stage string)
	// Event adds a step the owner can read on, numbered in order.
	Event(kind string, data any)
	// Partial keeps the text of the answer written so far, for the owner who is watching.
	Partial(text string)
}

// Handler does the work of one kind of task. It returns the result (kept as JSON for the owner) or an error. It must stop when ctx ends: the
// owner cancelled the task, or the platform is stopping.
type Handler func(ctx context.Context, task *model.AsyncTask, rep Reporter) (result any, err error)

// Queue makes and queues tasks.
type Queue struct {
	Tasks *rdb.AsyncTaskDAO
	Jobs  *rdb.JobDAO
	Now   func() time.Time
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

type payload struct {
	TaskID uint `json:"task_id"`
}

func (q *Queue) enqueue(ctx context.Context, t *model.AsyncTask, attempt int) error {
	body, err := json.Marshal(payload{TaskID: t.ID})
	if err != nil {
		return err
	}
	job := &model.Job{JobType: JobType, Status: "pending", Payload: string(body), MaxRetry: 1, RetryInitialSeconds: 5, RetryBackoffFactor: 2, RetryMaxSeconds: 60,
		IdempotencyKey: fmt.Sprintf("async-%d-%d", t.ID, attempt), TargetType: TargetType, TargetID: int64(t.ID), TargetName: t.Title}
	if _, err := q.Jobs.Create(ctx, job); err != nil {
		return err
	}
	return q.Tasks.SetJob(ctx, t.ID, job.ID)
}

// Submit makes a task of the owner and queues it. A second identical request (the same dedup key) while one is open returns the open task.
func (q *Queue) Submit(ctx context.Context, owner uint, ownerName, kind, title, dedup string, input any) (*model.AsyncTask, error) {
	if open, err := q.Tasks.Open(ctx, owner, kind, dedup); err != nil || open != nil {
		return open, err
	}
	if n, err := q.Tasks.CountOpen(ctx, owner); err != nil {
		return nil, err
	} else if n >= MaxOpenPerUser {
		return nil, ErrTooMany
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	t := &model.AsyncTask{OwnerID: owner, OwnerName: ownerName, Kind: kind, Title: title, Status: model.AsyncQueued, Payload: string(body), DedupKey: dedup,
		CreatedAt: q.now().UTC(), UpdatedAt: q.now().UTC()}
	if err := q.Tasks.Create(ctx, t); err != nil {
		return nil, err
	}
	if err := q.enqueue(ctx, t, 1); err != nil {
		_ = q.Tasks.Finish(ctx, t.ID, model.AsyncFailed, "", "could not be queued", q.now().UTC())
		return nil, err
	}
	return q.Tasks.Get(ctx, t.ID)
}

// Cancel asks the owner's task to stop: a queued one is cancelled at once, a running one when its worker next looks (within a second).
func (q *Queue) Cancel(ctx context.Context, owner, id uint) (*model.AsyncTask, error) {
	return q.Tasks.RequestCancel(ctx, owner, id, q.now().UTC())
}

// Retry queues a failed or cancelled task of the owner again, with what it was given the first time.
func (q *Queue) Retry(ctx context.Context, owner, id uint) (*model.AsyncTask, error) {
	t, err := q.Tasks.GetOwned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if t.Status != model.AsyncFailed && t.Status != model.AsyncCancelled {
		return nil, ErrNotFinal
	}
	if n, err := q.Tasks.CountOpen(ctx, owner); err != nil {
		return nil, err
	} else if n >= MaxOpenPerUser {
		return nil, ErrTooMany
	}
	if err := q.Tasks.ClearEvents(ctx, id); err != nil {
		return nil, err
	}
	if err := q.enqueue(ctx, t, t.Attempts+1); err != nil {
		return nil, err
	}
	return q.Tasks.GetOwned(ctx, owner, id)
}

// Runner is the async.run job.
type Runner struct {
	Tasks    *rdb.AsyncTaskDAO
	Handlers map[string]Handler
	Now      func() time.Time
	// CancelPoll is how often a running task is asked whether its owner cancelled it; a second when zero.
	CancelPoll time.Duration
}

func (r *Runner) Type() string { return JobType }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

type reporter struct {
	ctx   context.Context
	tasks *rdb.AsyncTaskDAO
	id    uint
	now   func() time.Time
}

func (p reporter) Progress(percent int, stage string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99 // 100 is the state succeeded
	}
	_ = p.tasks.Progress(context.WithoutCancel(p.ctx), p.id, percent, stage)
}

func (p reporter) Partial(text string) {
	_ = p.tasks.SetPartial(context.WithoutCancel(p.ctx), p.id, text)
}

func (p reporter) Event(kind string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = p.tasks.AddEvent(context.WithoutCancel(p.ctx), p.id, kind, string(body), p.now())
}

// Execute runs the task of the payload. A failure of the work is the state of the task, not an error of the job: the owner retries a task, the job
// engine does not run it again behind their back. A job that finds the platform stopping returns the error, so it is run again.
func (r *Runner) Execute(ctx context.Context, job *model.Job) error {
	var p payload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil || p.TaskID == 0 {
		return fmt.Errorf("invalid async.run payload: %v", err)
	}
	t, err := r.Tasks.Get(ctx, p.TaskID)
	if err != nil {
		return nil // the task was deleted (its retention ended): nothing to run
	}
	switch t.Status {
	case model.AsyncSucceeded, model.AsyncFailed, model.AsyncCancelled:
		return nil
	}
	if t.JobID != 0 && t.JobID != job.ID {
		return nil // a newer run of the task was queued
	}
	started, err := r.Tasks.Start(ctx, t.ID, r.now())
	if err != nil {
		return err
	}
	if !started {
		_ = r.Tasks.Finish(ctx, t.ID, model.AsyncCancelled, "", "", r.now())
		return nil
	}
	handler, ok := r.Handlers[t.Kind]
	if !ok {
		_ = r.Tasks.Finish(ctx, t.ID, model.AsyncFailed, "", "no handler for this kind of task", r.now())
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancelled := make(chan struct{})
	go func() {
		poll := r.CancelPoll
		if poll <= 0 {
			poll = time.Second
		}
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if asked, err := r.Tasks.CancelRequested(runCtx, t.ID); err == nil && asked {
					close(cancelled)
					cancel()
					return
				}
			}
		}
	}()

	result, runErr := handler(runCtx, t, reporter{ctx: ctx, tasks: r.Tasks, id: t.ID, now: r.now})
	finish := context.WithoutCancel(ctx)
	select {
	case <-cancelled:
		_ = r.Tasks.Finish(finish, t.ID, model.AsyncCancelled, "", "", r.now())
		return nil
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err() // the platform is stopping: the claim expires and the task is run again
	}
	if runErr != nil {
		logger.Warn("an async task failed", logger.Int("task_id", int(t.ID)), logger.String("kind", t.Kind), logger.Err(runErr))
		msg := runErr.Error()
		if len([]rune(msg)) > 480 {
			msg = string([]rune(msg)[:480])
		}
		_ = r.Tasks.Finish(finish, t.ID, model.AsyncFailed, "", msg, r.now())
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		_ = r.Tasks.Finish(finish, t.ID, model.AsyncFailed, "", "the result could not be kept", r.now())
		return nil
	}
	return r.Tasks.Finish(finish, t.ID, model.AsyncSucceeded, string(body), "", r.now())
}
