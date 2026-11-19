package asyncrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
)

// The intervals of a running task.
const (
	// MinProgressInterval is the shortest time between two writes of the progress of a task: calls that come faster are folded and the last one is written.
	MinProgressInterval = 250 * time.Millisecond
	// MinPartialInterval is the same for what the task has done so far.
	MinPartialInterval = 500 * time.Millisecond
	// MaxPartialBytes is the most a task keeps of what it has done so far.
	MaxPartialBytes = 256 << 10
	// DefaultHeartbeat is how often a worker says it is alive, and StaleAfter how long without it a worker is taken for gone.
	DefaultHeartbeat = 10 * time.Second
	StaleAfter       = time.Minute
	// DefaultRetryBase and DefaultRetryMax are the wait before the second attempt and the longest wait between attempts (it doubles each time).
	DefaultRetryBase = 10 * time.Second
	DefaultRetryMax  = 2 * time.Minute
	// MinLostAttempts is how many times a task whose worker was lost is put back at least, whatever its attempts for other failures.
	MinLostAttempts = 3
)

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// Permanent marks an error that trying again cannot mend (a request that is not valid, a permission that is not there).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// Retryable marks an error that trying again may mend, whatever it looks like.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return retryableError{err}
}

// Classify says whether a failure is one that trying again may mend, and the code of why it failed: a timeout and a data source that could not be reached
// are, a request that is not valid or a permission that is not there is not.
func Classify(err error) (retryable bool, code string) {
	var perm permanentError
	if errors.As(err, &perm) {
		return false, model.AsyncCodeFailed
	}
	var forced retryableError
	if errors.As(err, &forced) {
		_, code = Classify(forced.err)
		if code == model.AsyncCodeFailed {
			code = model.AsyncCodeUnreachable
		}
		return true, code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true, model.AsyncCodeTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true, model.AsyncCodeTimeout
		}
		return true, model.AsyncCodeUnreachable
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true, model.AsyncCodeUnreachable
	}
	if promclient.IsTransient(err) {
		return true, model.AsyncCodeUnreachable
	}
	return false, model.AsyncCodeFailed
}

// retryLater is what a run returns to the job engine when the task is to be tried again: the engine puts the job back until the time.
type retryLater struct {
	err error
	at  time.Time
}

func (e retryLater) Error() string {
	return fmt.Sprintf("tried again at %s: %v", e.at.Format(time.RFC3339), e.err)
}
func (e retryLater) RetryAt() time.Time { return e.at }
func (e retryLater) Unwrap() error      { return e.err }

// Runner is the async.run job.
type Runner struct {
	Tasks    *rdb.AsyncTaskDAO
	Handlers map[string]Handler
	Now      func() time.Time
	// CancelPoll is how often a running task is asked whether its owner cancelled it; a second when zero.
	CancelPoll time.Duration
	// HeartbeatEvery is how often the worker says it is alive; DefaultHeartbeat when zero.
	HeartbeatEvery time.Duration
	// RetryBase and RetryMax shape the wait between attempts; the defaults when zero.
	RetryBase, RetryMax time.Duration
	// Retention is how long the result of a finished task is kept; DefaultRetention when nil.
	Retention func(context.Context) time.Duration
}

func (r *Runner) Type() string { return JobType }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) expiry(ctx context.Context, from time.Time) *time.Time {
	return (&Queue{Retention: r.Retention}).expiry(ctx, from)
}

// retryDelay is the wait before attempt number attempts+1: the base, doubling, up to the longest.
func (r *Runner) retryDelay(attempts int) time.Duration {
	base, longest := r.RetryBase, r.RetryMax
	if base <= 0 {
		base = DefaultRetryBase
	}
	if longest <= 0 {
		longest = DefaultRetryMax
	}
	d := base
	for i := 1; i < attempts && d < longest; i++ {
		d *= 2
	}
	if d > longest {
		d = longest
	}
	return d
}

// reporter writes what a handler reports, no faster than the intervals above; flush writes what was folded.
type reporter struct {
	ctx   context.Context
	tasks *rdb.AsyncTaskDAO
	id    uint
	now   func() time.Time

	mu            sync.Mutex
	lastProgress  time.Time
	pending       *progressValue
	lastPartial   time.Time
	pendingResult string
}

type progressValue struct {
	percent int
	stage   string
}

func (p *reporter) Progress(percent int, stage string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99 // 100 is the state succeeded
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now().Sub(p.lastProgress) < MinProgressInterval {
		p.pending = &progressValue{percent, stage}
		return
	}
	p.pending = nil
	p.lastProgress = p.now()
	_ = p.tasks.Progress(context.WithoutCancel(p.ctx), p.id, percent, stage)
}

func (p *reporter) Partial(text string) {
	_ = p.tasks.SetPartial(context.WithoutCancel(p.ctx), p.id, text)
}

func (p *reporter) PartialResult(v any) {
	body, err := json.Marshal(v)
	if err != nil || len(body) > MaxPartialBytes {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now().Sub(p.lastPartial) < MinPartialInterval {
		p.pendingResult = string(body)
		return
	}
	p.pendingResult = ""
	p.lastPartial = p.now()
	_ = p.tasks.SetPartialResult(context.WithoutCancel(p.ctx), p.id, string(body))
}

func (p *reporter) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending != nil {
		_ = p.tasks.Progress(context.WithoutCancel(p.ctx), p.id, p.pending.percent, p.pending.stage)
		p.pending = nil
		p.lastProgress = p.now()
	}
	if p.pendingResult != "" {
		_ = p.tasks.SetPartialResult(context.WithoutCancel(p.ctx), p.id, p.pendingResult)
		p.pendingResult = ""
		p.lastPartial = p.now()
	}
}

func (p *reporter) Event(kind string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = p.tasks.AddEvent(context.WithoutCancel(p.ctx), p.id, kind, string(body), p.now())
}

// run calls the handler and turns a panic into a failure that is not tried again: a bug does not get better by waiting.
func run(ctx context.Context, h Handler, t *model.AsyncTask, rep Reporter) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("an async task panicked", logger.Int("task_id", int(t.ID)), logger.String("kind", t.Kind), logger.Any("panic", r))
			err = Permanent(fmt.Errorf("the task stopped on an internal error"))
		}
	}()
	return h(ctx, t, rep)
}

// Execute runs the task of the payload. A failure of the work is the state of the task, not an error of the job: the owner retries a task, and a failure that
// can be tried again is put back with a wait (the job is deferred to that time), the job engine does not run it again behind their back. A job that finds
// the platform stopping returns the error, so it is run again.
func (r *Runner) Execute(ctx context.Context, job *model.Job) error {
	var p payload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil || p.TaskID == 0 {
		return fmt.Errorf("invalid async.run payload: %v", err)
	}
	t, err := r.Tasks.Get(ctx, p.TaskID)
	if err != nil {
		return nil // the task was deleted (its record retention ended): nothing to run
	}
	switch t.Status {
	case model.AsyncSucceeded, model.AsyncFailed, model.AsyncCancelled, model.AsyncExpired:
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
		_, _ = r.Tasks.Finish(ctx, t.ID, rdb.FinishInfo{Status: model.AsyncCancelled, At: r.now(), ExpiresAt: r.expiry(ctx, r.now()), JobID: job.ID})
		return nil
	}
	attempts := t.Attempts + 1
	handler, ok := r.Handlers[t.Kind]
	if !ok {
		_, _ = r.Tasks.Finish(ctx, t.ID, rdb.FinishInfo{Status: model.AsyncFailed, Error: "no handler for this kind of task", ErrorCode: model.AsyncCodeFailed, At: r.now(), ExpiresAt: r.expiry(ctx, r.now()), JobID: job.ID})
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rep := &reporter{ctx: ctx, tasks: r.Tasks, id: t.ID, now: r.now}
	cancelled := make(chan struct{})
	superseded := make(chan struct{})
	go r.watch(runCtx, cancel, job.ID, t.ID, rep, cancelled, superseded)

	result, runErr := run(runCtx, handler, t, rep)
	rep.flush()
	finish := context.WithoutCancel(ctx)
	info := rdb.FinishInfo{JobID: job.ID}
	select {
	case <-superseded:
		return nil // the task was put back and belongs to another run: this one changes nothing
	case <-cancelled:
		info.Status, info.At = model.AsyncCancelled, r.now()
		info.ExpiresAt = r.expiry(ctx, info.At)
		_, _ = r.Tasks.Finish(finish, t.ID, info)
		return nil
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err() // the platform is stopping: the claim expires and the task is run again
	}
	if runErr != nil {
		retryable, code := Classify(runErr)
		logger.Warn("an async task failed", logger.Int("task_id", int(t.ID)), logger.String("kind", t.Kind), logger.Int("attempt", attempts), logger.String("code", code), logger.Err(runErr))
		msg := runErr.Error()
		if len([]rune(msg)) > 480 {
			msg = string([]rune(msg)[:480])
		}
		if retryable && attempts < t.MaxAttempts {
			next := r.now().Add(r.retryDelay(attempts))
			if ok, err := r.Tasks.Requeue(finish, t.ID, job.ID, msg, code, next); err == nil && ok {
				return retryLater{err: runErr, at: next}
			}
		}
		info.Status, info.Error, info.ErrorCode, info.Retryable, info.At = model.AsyncFailed, msg, code, retryable, r.now()
		info.ExpiresAt = r.expiry(ctx, info.At)
		_, _ = r.Tasks.Finish(finish, t.ID, info)
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		info.Status, info.Error, info.ErrorCode, info.At = model.AsyncFailed, "the result could not be kept", model.AsyncCodeFailed, r.now()
		info.ExpiresAt = r.expiry(ctx, info.At)
		_, _ = r.Tasks.Finish(finish, t.ID, info)
		return nil
	}
	info.Status, info.Result, info.At = model.AsyncSucceeded, string(body), r.now()
	info.ExpiresAt = r.expiry(ctx, info.At)
	_, err = r.Tasks.Finish(finish, t.ID, info)
	return err
}

// watch looks after a running task until it ends: it asks every second whether the owner cancelled it or the task was put back and queued for another run,
// says every few seconds that the worker is alive, and writes the progress that was folded.
func (r *Runner) watch(ctx context.Context, cancel context.CancelFunc, jobID int64, id uint, rep *reporter, cancelled, superseded chan struct{}) {
	poll := r.CancelPoll
	if poll <= 0 {
		poll = time.Second
	}
	every := r.HeartbeatEvery
	if every <= 0 {
		every = DefaultHeartbeat
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rep.flush()
			if time.Since(lastBeat) >= every {
				lastBeat = time.Now()
				_ = r.Tasks.Heartbeat(ctx, id, r.now())
			}
			st, err := r.Tasks.RunState(ctx, id)
			if err != nil {
				continue
			}
			if st.JobID != jobID || (st.Status != model.AsyncRunning && st.Status != model.AsyncCancelling) {
				close(superseded)
				cancel()
				return
			}
			if st.CancelRequested {
				close(cancelled)
				cancel()
				return
			}
		}
	}
}

// Sweeper finds the tasks whose worker is gone (the process was killed, the node was lost) and queues them again, so a task is not stuck as running for
// ever. A worker that says it is alive every ten seconds and has not for a minute is gone; a task that was being cancelled is cancelled; one that has used
// its attempts fails with the code worker_lost, and the owner may retry it.
type Sweeper struct {
	Tasks *rdb.AsyncTaskDAO
	Queue *Queue
	// Stale and Every are StaleAfter and fifteen seconds when zero.
	Stale time.Duration
	Every time.Duration
	Now   func() time.Time
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Once looks once and returns how many tasks it dealt with.
func (s *Sweeper) Once(ctx context.Context) (int, error) {
	stale := s.Stale
	if stale <= 0 {
		stale = StaleAfter
	}
	now := s.now()
	before := now.Add(-stale)
	list, err := s.Tasks.StaleRunning(ctx, before, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range list {
		limit := t.MaxAttempts
		if limit < MinLostAttempts {
			limit = MinLostAttempts
		}
		switch {
		case t.Status == model.AsyncCancelling || t.CancelRequested:
			if _, err := s.Tasks.Finish(ctx, t.ID, rdb.FinishInfo{Status: model.AsyncCancelled, At: now, ExpiresAt: s.Queue.expiry(ctx, now), JobID: t.JobID}); err == nil {
				n++
			}
		case t.Attempts >= limit:
			msg := "the process that was running the task was lost, and the task was tried too many times"
			if _, err := s.Tasks.Finish(ctx, t.ID, rdb.FinishInfo{Status: model.AsyncFailed, Error: msg, ErrorCode: model.AsyncCodeWorkerLost, Retryable: true, At: now, ExpiresAt: s.Queue.expiry(ctx, now), JobID: t.JobID}); err == nil {
				n++
			}
		default:
			ok, err := s.Tasks.Reclaim(ctx, t.ID, before, model.AsyncCodeWorkerLost, "the process that was running the task was lost: it is run again")
			if err != nil || !ok {
				continue
			}
			jobID, err := s.Queue.enqueue(ctx, t)
			if err != nil {
				logger.Warn("a task whose worker was lost could not be queued again", logger.Int("task_id", int(t.ID)), logger.Err(err))
				continue
			}
			if err := s.Tasks.AttachJob(ctx, t.ID, jobID); err != nil {
				continue
			}
			logger.Warn("a task whose worker was lost was queued again", logger.Int("task_id", int(t.ID)), logger.String("kind", t.Kind), logger.Int("attempt", t.Attempts))
			n++
		}
	}
	return n, nil
}

// Run looks every few seconds until ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 15 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.Once(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("the sweep of the async tasks failed", logger.Err(err))
			}
		}
	}
}
