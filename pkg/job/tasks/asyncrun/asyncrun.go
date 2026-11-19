// Package asyncrun runs the async tasks of the users (P11 B1, B2): work that takes longer than one request may. A task is a row (who asked, what, its state,
// progress, what it has done so far and its result) and a job of the task center that runs it, so it has the claim, the heartbeat and the recovery of every
// job: a task that was running when the platform stopped is run again, and one that was queued is not lost. On top of that the task has its own life: a
// failure that can be tried again (a timeout, a data source that could not be reached) is tried again after a wait until its attempts are used; a worker
// that stops saying it is alive for a minute is found by the sweeper and its task is queued again; a stop asked for is cancelling until the worker has
// stopped; a result that is old enough is dropped (expired) and the record stays. The handlers, one for each kind of task, are given by the packages
// that know the work.
package asyncrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// JobType is the job of the task center that runs one async task.
const JobType = "async.run"

// TargetType is the kind of target of a run in the task center.
const TargetType = "async_task"

// MaxOpenPerUser is how many queued or running tasks one user may have.
const MaxOpenPerUser = 5

// DefaultRetention is how long the result of a finished task is kept when the system parameter cannot be read.
const DefaultRetention = 72 * time.Hour

// Errors of submitting a task.
var (
	ErrTooMany   = errors.New("there are already 5 tasks running or waiting: wait for one to finish or cancel one")
	ErrNotFinal  = errors.New("the task is still queued or running")
	ErrNotQueued = errors.New("the task cannot be changed in its state")
	ErrExpired   = errors.New("the result of the task has expired: ask again")
)

// Reporter is how a handler tells its owner how far it is.
type Reporter interface {
	// Progress says how far the task is (0-100) and what it is doing. Calls that come faster than four a second are folded: the last one is written.
	Progress(percent int, stage string)
	// Event adds a step the owner can read on, numbered in order.
	Event(kind string, data any)
	// Partial keeps the text of the answer written so far, for the owner who is watching.
	Partial(text string)
	// PartialResult keeps what the task has done so far (any value that marshals to JSON): the owner reads it while the task runs, and it stays if the
	// task fails or is cancelled.
	PartialResult(v any)
}

// Handler does the work of one kind of task. It returns the result (kept as JSON for the owner) or an error. It must stop when ctx ends: the
// owner cancelled the task, or the platform is stopping.
type Handler func(ctx context.Context, task *model.AsyncTask, rep Reporter) (result any, err error)

// Queue makes and queues tasks.
type Queue struct {
	Tasks *rdb.AsyncTaskDAO
	Jobs  *rdb.JobDAO
	Now   func() time.Time
	// Retention is how long the result of a finished task is kept; DefaultRetention when nil.
	Retention func(context.Context) time.Duration
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *Queue) expiry(ctx context.Context, from time.Time) *time.Time {
	d := DefaultRetention
	if q.Retention != nil {
		if got := q.Retention(ctx); got > 0 {
			d = got
		}
	}
	at := from.UTC().Add(d)
	return &at
}

// jobSeq makes the key of every job different, whatever the clock says.
var jobSeq atomic.Int64

type payload struct {
	TaskID uint `json:"task_id"`
}

// enqueue makes the job of the task center that runs the task and returns its id. The key is unique to the run, so a task that is queued again has a
// new job.
func (q *Queue) enqueue(ctx context.Context, t *model.AsyncTask) (int64, error) {
	body, err := json.Marshal(payload{TaskID: t.ID})
	if err != nil {
		return 0, err
	}
	job := &model.Job{JobType: JobType, Status: "pending", Payload: string(body), MaxRetry: 1, RetryInitialSeconds: 5, RetryBackoffFactor: 2, RetryMaxSeconds: 60,
		IdempotencyKey: fmt.Sprintf("async-%d-%d-%d", t.ID, q.now().UnixNano(), jobSeq.Add(1)), TargetType: TargetType, TargetID: int64(t.ID), TargetName: t.Title}
	if _, err := q.Jobs.Create(ctx, job); err != nil {
		return 0, err
	}
	return job.ID, nil
}

// Spec is a task to queue.
type Spec struct {
	Owner     uint
	OwnerName string
	// Kind says which handler runs it; Title is what the owner sees; Dedup makes a second identical request while one is open the same task.
	Kind  string
	Title string
	Dedup string
	// TraceID is the request id of the request that asks: it is kept with the task.
	TraceID string
	// MaxAttempts is how many times the task may be run for a failure that can be tried again (1 when zero: it is not tried again by itself).
	MaxAttempts int
	// Input is what the handler is given (JSON).
	Input any
	// Cached, when it answers a result, ends the task at once as succeeded with that result and from_cache set: the same question was already worked out
	// and nothing has changed since (P11 B3).
	Cached func(ctx context.Context) (result any, ok bool)
}

// Hash is the hash of what a task is given, the key two identical requests have in common.
func Hash(kind string, input []byte) string {
	sum := sha256.Sum256(append([]byte(kind+"|"), input...))
	return hex.EncodeToString(sum[:])
}

// Submit makes a task of the owner and queues it. A second identical request (the same dedup key) while one is open returns the open task.
func (q *Queue) Submit(ctx context.Context, owner uint, ownerName, kind, title, dedup string, input any) (*model.AsyncTask, error) {
	return q.SubmitSpec(ctx, Spec{Owner: owner, OwnerName: ownerName, Kind: kind, Title: title, Dedup: dedup, Input: input})
}

// SubmitSpec is Submit with the options of a Spec.
func (q *Queue) SubmitSpec(ctx context.Context, sp Spec) (*model.AsyncTask, error) {
	if open, err := q.Tasks.Open(ctx, sp.Owner, sp.Kind, sp.Dedup); err != nil || open != nil {
		return open, err
	}
	if n, err := q.Tasks.CountOpen(ctx, sp.Owner); err != nil {
		return nil, err
	} else if n >= MaxOpenPerUser {
		return nil, ErrTooMany
	}
	body, err := json.Marshal(sp.Input)
	if err != nil {
		return nil, err
	}
	now := q.now().UTC()
	if sp.MaxAttempts < 1 {
		sp.MaxAttempts = 1
	}
	t := &model.AsyncTask{OwnerID: sp.Owner, OwnerName: sp.OwnerName, Kind: sp.Kind, Title: sp.Title, Status: model.AsyncQueued, Payload: string(body), DedupKey: sp.Dedup,
		ParamsHash: Hash(sp.Kind, body), MaxAttempts: sp.MaxAttempts, TraceID: sp.TraceID, CreatedAt: now, UpdatedAt: now}
	if sp.Cached != nil {
		if result, ok := sp.Cached(ctx); ok {
			if raw, err := json.Marshal(result); err == nil {
				t.Status, t.Result, t.FromCache, t.Progress = model.AsyncSucceeded, string(raw), true, 100
				t.StartedAt, t.FinishedAt, t.ExpiresAt = &now, &now, q.expiry(ctx, now)
				if err := q.Tasks.Create(ctx, t); err != nil {
					return nil, err
				}
				return t, nil
			}
		}
	}
	if err := q.Tasks.Create(ctx, t); err != nil {
		return nil, err
	}
	jobID, err := q.enqueue(ctx, t)
	if err == nil {
		err = q.Tasks.SetJob(ctx, t.ID, jobID)
	}
	if err != nil {
		_, _ = q.Tasks.Finish(ctx, t.ID, rdb.FinishInfo{Status: model.AsyncFailed, Error: "could not be queued", ErrorCode: model.AsyncCodeFailed, At: now})
		return nil, err
	}
	return q.Tasks.Get(ctx, t.ID)
}

// Cancel asks the owner's task to stop: a queued one is cancelled at once, a running one when its worker next looks (within a second).
func (q *Queue) Cancel(ctx context.Context, owner, id uint) (*model.AsyncTask, error) {
	now := q.now().UTC()
	return q.Tasks.RequestCancel(ctx, owner, id, now, q.expiry(ctx, now))
}

// Retry queues a failed or cancelled task of the owner again, with what it was given the first time.
func (q *Queue) Retry(ctx context.Context, owner, id uint) (*model.AsyncTask, error) {
	t, err := q.Tasks.GetOwned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if t.Status == model.AsyncExpired {
		return nil, ErrExpired
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
	jobID, err := q.enqueue(ctx, t)
	if err != nil {
		return nil, err
	}
	if err := q.Tasks.SetJob(ctx, id, jobID); err != nil {
		return nil, err
	}
	return q.Tasks.GetOwned(ctx, owner, id)
}
