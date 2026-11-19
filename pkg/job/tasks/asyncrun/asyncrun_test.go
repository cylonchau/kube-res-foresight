package asyncrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var seq int32

type fixture struct {
	t      *testing.T
	db     *gorm.DB
	queue  *Queue
	runner *Runner
	tasks  *rdb.AsyncTaskDAO
	// handler is what the tasks of kind "work" do
	handler Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:asyncrun%d?mode=memory&cache=shared", atomic.AddInt32(&seq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AsyncTask{}, &model.AsyncTaskEvent{}, &model.Job{}))
	f := &fixture{t: t, db: db, tasks: rdb.NewAsyncTaskDAO(db)}
	f.queue = &Queue{Tasks: f.tasks, Jobs: rdb.NewJobDAO(db)}
	f.runner = &Runner{Tasks: f.tasks, CancelPoll: 10 * time.Millisecond, HeartbeatEvery: 20 * time.Millisecond, RetryBase: 50 * time.Millisecond, RetryMax: time.Second,
		Handlers: map[string]Handler{"work": func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
			return f.handler(ctx, task, rep)
		}}}
	return f
}

func (f *fixture) submit(max int) *model.AsyncTask {
	f.t.Helper()
	task, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 1, OwnerName: "ann", Kind: "work", Title: "a task", Dedup: fmt.Sprintf("d%d", atomic.AddInt32(&seq, 1)),
		TraceID: "op-trace", MaxAttempts: max, Input: map[string]int{"n": 1}})
	require.NoError(f.t, err)
	return task
}

func (f *fixture) job(task *model.AsyncTask) *model.Job {
	f.t.Helper()
	var job model.Job
	require.NoError(f.t, f.db.Where("target_id = ?", task.ID).Order("id DESC").First(&job).Error)
	return &job
}

func (f *fixture) get(id uint) *model.AsyncTask {
	f.t.Helper()
	task, err := f.tasks.Get(context.Background(), id)
	require.NoError(f.t, err)
	return task
}

func TestATaskIsQueuedWithItsHashItsTraceAndItsAttemptsAndRunsToItsResult(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		rep.Progress(40, "half")
		rep.Event("step", map[string]string{"what": "read"})
		return map[string]int{"answer": 42}, nil
	}
	task := f.submit(3)
	assert.Equal(t, model.AsyncQueued, task.Status)
	assert.Equal(t, 3, task.MaxAttempts)
	assert.Equal(t, "op-trace", task.TraceID, "the request that queued it, to find it in the log of the gateway")
	assert.Len(t, task.ParamsHash, 64)
	assert.NotZero(t, task.JobID)

	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	done := f.get(task.ID)
	assert.Equal(t, model.AsyncSucceeded, done.Status)
	assert.JSONEq(t, `{"answer":42}`, done.Result)
	assert.Equal(t, 100, done.Progress)
	assert.Equal(t, 1, done.Attempts)
	require.NotNil(t, done.HeartbeatAt, "the heartbeat starts with the run")
	require.NotNil(t, done.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(DefaultRetention), *done.ExpiresAt, time.Minute, "the result is kept for the retention")
	events, err := f.tasks.EventsAfter(context.Background(), 1, task.ID, 0, 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestAFailureThatCanBeTriedAgainIsPutBackWithAWaitUntilTheAttemptsAreUsed(t *testing.T) {
	f := newFixture(t)
	var calls int32
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return nil, fmt.Errorf("the data source did not answer: %w", context.DeadlineExceeded)
		}
		return "ok", nil
	}
	task := f.submit(3)
	job := f.job(task)

	err := f.runner.Execute(context.Background(), job)
	var later interface{ RetryAt() time.Time }
	require.ErrorAs(t, err, &later, "the job engine is told when to run the job again")
	assert.True(t, later.RetryAt().After(time.Now()))
	first := f.get(task.ID)
	assert.Equal(t, model.AsyncQueued, first.Status, "it is queued again, not failed")
	assert.Equal(t, model.AsyncCodeTimeout, first.ErrorCode)
	assert.True(t, first.Retryable)
	assert.Contains(t, first.Error, "did not answer", "the page can say why it is waiting")
	require.NotNil(t, first.NextRunAt)
	assert.Equal(t, 1, first.Attempts)

	err = f.runner.Execute(context.Background(), job)
	require.ErrorAs(t, err, &later)
	second := f.get(task.ID)
	assert.Equal(t, 2, second.Attempts)
	assert.True(t, second.NextRunAt.After(*first.NextRunAt), "the wait doubles")

	require.NoError(t, f.runner.Execute(context.Background(), job))
	done := f.get(task.ID)
	assert.Equal(t, model.AsyncSucceeded, done.Status)
	assert.Equal(t, 3, done.Attempts)
	assert.Empty(t, done.Error, "the reason of the earlier attempts is gone when it succeeds")
}

func TestWhenTheAttemptsAreUsedTheTaskFailsWithItsCodeAndIsStillOneTheOwnerMayRetry(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	}
	task := f.submit(2)
	job := f.job(task)
	require.Error(t, f.runner.Execute(context.Background(), job))
	require.NoError(t, f.runner.Execute(context.Background(), job), "the second attempt is the last: the job ends")
	failed := f.get(task.ID)
	assert.Equal(t, model.AsyncFailed, failed.Status)
	assert.Equal(t, model.AsyncCodeUnreachable, failed.ErrorCode)
	assert.True(t, failed.Retryable)
	assert.Equal(t, 2, failed.Attempts)
	require.NotNil(t, failed.ExpiresAt)

	retried, err := f.queue.Retry(context.Background(), 1, task.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AsyncQueued, retried.Status)
	assert.Zero(t, retried.Attempts, "a retry of the owner starts the attempts again")
	assert.Empty(t, retried.ErrorCode)
	assert.NotEqual(t, job.ID, retried.JobID)
}

func TestAFailureThatTryingAgainCannotMendEndsTheTaskAtOnceAndAPanicIsOne(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		return nil, errors.New("the module does not exist")
	}
	task := f.submit(5)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	failed := f.get(task.ID)
	assert.Equal(t, model.AsyncFailed, failed.Status)
	assert.False(t, failed.Retryable)
	assert.Equal(t, 1, failed.Attempts, "five attempts were allowed and one was used")

	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) { panic("boom") }
	task = f.submit(5)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	failed = f.get(task.ID)
	assert.Equal(t, model.AsyncFailed, failed.Status)
	assert.NotContains(t, failed.Error, "boom", "a panic is not shown to the owner")

	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		return nil, Permanent(fmt.Errorf("the plan is not valid: %w", context.DeadlineExceeded))
	}
	task = f.submit(5)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	assert.Equal(t, model.AsyncFailed, f.get(task.ID).Status, "marked permanent, it is not tried again whatever it wraps")
}

func TestStoppingARunningTaskIsCancellingUntilTheWorkerHasStoppedAndWhatWasDoneIsKept(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		rep.PartialResult(map[string]any{"done": 2, "of": 5})
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	task := f.submit(3)
	job := f.job(task)
	finished := make(chan error, 1)
	go func() { finished <- f.runner.Execute(context.Background(), job) }()
	<-started

	got, err := f.queue.Cancel(context.Background(), 1, task.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AsyncCancelling, got.Status, "the worker has not stopped yet")
	assert.Equal(t, model.AsyncCancelling, f.get(task.ID).Status)

	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
	done := f.get(task.ID)
	assert.Equal(t, model.AsyncCancelled, done.Status)
	assert.True(t, done.HasPartial, "what was done stays")
	assert.JSONEq(t, `{"done":2,"of":5}`, done.PartialResult)
	assert.NotNil(t, done.ExpiresAt)
}

func TestAQueuedTaskIsCancelledAtOnceAndNeverRuns(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		t.Fatal("a cancelled task must not run")
		return nil, nil
	}
	task := f.submit(1)
	got, err := f.queue.Cancel(context.Background(), 1, task.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AsyncCancelled, got.Status)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	assert.Equal(t, model.AsyncCancelled, f.get(task.ID).Status)
	_, err = f.queue.Cancel(context.Background(), 2, task.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "another user's task does not exist")
}

func TestProgressIsFoldedToFourWritesASecondAndTheLastValueIsNotLost(t *testing.T) {
	f := newFixture(t)
	var seenInside int
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		for i := 1; i <= 60; i++ {
			rep.Progress(i, fmt.Sprintf("part %d", i))
		}
		seenInside = f.get(task.ID).Progress
		return nil, Permanent(errors.New("stop here"))
	}
	task := f.submit(1)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	assert.Equal(t, 1, seenInside, "sixty calls in a moment wrote the first one")
	failed := f.get(task.ID)
	assert.Equal(t, 60, failed.Progress, "the last one was written when the task ended")
	assert.Equal(t, "part 60", failed.Stage)
}

func TestTheWorkerSaysItIsAliveWhileItRuns(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		time.Sleep(150 * time.Millisecond)
		return "ok", nil
	}
	task := f.submit(1)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	done := f.get(task.ID)
	require.NotNil(t, done.HeartbeatAt)
	assert.True(t, done.HeartbeatAt.After(*done.StartedAt), "the heartbeat moved after the start")
}

func TestATaskWhoseWorkerIsLostIsQueuedAgainWithANewJobAndTheOldJobChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) { return "second run", nil }
	task := f.submit(1)
	oldJob := f.job(task)
	// the worker started the task and went away: the heartbeat is old
	old := time.Now().UTC().Add(-5 * time.Minute)
	require.NoError(t, f.db.Model(&model.AsyncTask{}).Where("id = ?", task.ID).UpdateColumns(map[string]any{"status": model.AsyncRunning, "attempts": 1, "started_at": old, "heartbeat_at": old}).Error)

	sweeper := &Sweeper{Tasks: f.tasks, Queue: f.queue}
	n, err := sweeper.Once(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	back := f.get(task.ID)
	assert.Equal(t, model.AsyncQueued, back.Status)
	assert.Equal(t, model.AsyncCodeWorkerLost, back.ErrorCode)
	assert.Equal(t, 1, back.Attempts, "the attempt that was lost counts")
	assert.NotEqual(t, oldJob.ID, back.JobID, "a new job runs it")

	// the claim of the job that the dead worker had expires and the engine runs it again: it finds the task belongs to another job
	require.NoError(t, f.runner.Execute(context.Background(), oldJob))
	assert.Equal(t, model.AsyncQueued, f.get(task.ID).Status)

	var newJob model.Job
	require.NoError(t, f.db.First(&newJob, back.JobID).Error)
	require.NoError(t, f.runner.Execute(context.Background(), &newJob))
	done := f.get(task.ID)
	assert.Equal(t, model.AsyncSucceeded, done.Status)
	assert.Equal(t, 2, done.Attempts)
}

func TestTheSweeperLeavesAliveWorkersAlonePutsAStopThatWasAskedForToCancelledAndFailsWhatWasLostTooOften(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) { return nil, nil }
	fresh, cancelling, tooOften := f.submit(1), f.submit(1), f.submit(1)
	now := time.Now().UTC()
	old := now.Add(-5 * time.Minute)
	set := func(id uint, values map[string]any) {
		require.NoError(t, f.db.Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(values).Error)
	}
	set(fresh.ID, map[string]any{"status": model.AsyncRunning, "started_at": old, "heartbeat_at": now})
	set(cancelling.ID, map[string]any{"status": model.AsyncCancelling, "cancel_requested": true, "started_at": old, "heartbeat_at": old})
	set(tooOften.ID, map[string]any{"status": model.AsyncRunning, "attempts": MinLostAttempts, "started_at": old, "heartbeat_at": old})

	n, err := (&Sweeper{Tasks: f.tasks, Queue: f.queue}).Once(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, model.AsyncRunning, f.get(fresh.ID).Status)
	assert.Equal(t, model.AsyncCancelled, f.get(cancelling.ID).Status)
	lost := f.get(tooOften.ID)
	assert.Equal(t, model.AsyncFailed, lost.Status)
	assert.Equal(t, model.AsyncCodeWorkerLost, lost.ErrorCode)
	assert.True(t, lost.Retryable, "the owner may try again")
}

func TestAWorkerThatIsStillAliveButWasTakenForGoneStopsWhenItsTaskBelongsToAnotherRun(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	var run int32
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		if atomic.AddInt32(&run, 1) > 1 {
			return "ok", nil
		}
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	task := f.submit(1)
	job := f.job(task)
	finished := make(chan error, 1)
	go func() { finished <- f.runner.Execute(context.Background(), job) }()
	<-started

	// the sweeper decides the heartbeat is too old (the database was slow) and queues the task again
	time.Sleep(30 * time.Millisecond)
	n, err := (&Sweeper{Tasks: f.tasks, Queue: f.queue, Stale: 5 * time.Millisecond}).Once(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the old worker did not notice that the task is no longer its own")
	}
	require.NoError(t, <-finished)
	assert.Equal(t, model.AsyncQueued, f.get(task.ID).Status, "the old worker wrote nothing of its own")
}

func TestAnExpiredResultIsDroppedAndOnlyTheRecordStaysUntilItIsDeleted(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		rep.Event("step", "x")
		return map[string]string{"big": "answer"}, nil
	}
	task := f.submit(1)
	require.NoError(t, f.runner.Execute(context.Background(), f.job(task)))
	long := time.Now().UTC().Add(-100 * time.Hour)
	require.NoError(t, f.db.Model(&model.AsyncTask{}).Where("id = ?", task.ID).UpdateColumn("finished_at", long).Error)

	n, err := f.tasks.Expire(context.Background(), time.Now().UTC().Add(-72*time.Hour), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	expired := f.get(task.ID)
	assert.Equal(t, model.AsyncExpired, expired.Status)
	assert.Empty(t, expired.Result)
	assert.Empty(t, expired.Payload, "what it was given is gone as well")
	events, _ := f.tasks.EventsAfter(context.Background(), 1, task.ID, 0, 10)
	assert.Empty(t, events)
	_, err = f.queue.Retry(context.Background(), 1, task.ID)
	assert.ErrorIs(t, err, ErrExpired, "there is nothing left to run again")

	n, err = f.tasks.Expire(context.Background(), time.Now().UTC().Add(-72*time.Hour), 100)
	require.NoError(t, err)
	assert.Zero(t, n, "an expired task is not expired twice")
	n, err = f.tasks.PruneBefore(context.Background(), time.Now().UTC().Add(-200*time.Hour), 100)
	require.NoError(t, err)
	assert.Zero(t, n, "the record stays a month after the result went")
	n, err = f.tasks.PruneBefore(context.Background(), time.Now().UTC().Add(-50*time.Hour), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func TestAnAnswerThatWasWorkedOutBeforeEndsTheTaskAtOnceMarkedAsFromTheCache(t *testing.T) {
	f := newFixture(t)
	f.handler = func(ctx context.Context, task *model.AsyncTask, rep Reporter) (any, error) {
		t.Fatal("nothing is run for an answer that is there")
		return nil, nil
	}
	task, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 1, Kind: "work", Title: "cached", Input: 1,
		Cached: func(context.Context) (any, bool) { return map[string]int{"n": 7}, true }})
	require.NoError(t, err)
	assert.Equal(t, model.AsyncSucceeded, task.Status)
	assert.True(t, task.FromCache)
	assert.JSONEq(t, `{"n":7}`, task.Result)
	assert.Equal(t, 100, task.Progress)
	var jobs int64
	require.NoError(t, f.db.Model(&model.Job{}).Count(&jobs).Error)
	assert.Zero(t, jobs, "no job was made")

	miss, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 1, Kind: "work", Title: "miss", Input: 2, Cached: func(context.Context) (any, bool) { return nil, false }})
	require.NoError(t, err)
	assert.Equal(t, model.AsyncQueued, miss.Status)
	assert.False(t, miss.FromCache)
}

func TestTheTasksOfAnOwnerAreListedWhenTheyAreOpenOrRecentAndNeverTheOthers(t *testing.T) {
	f := newFixture(t)
	mine := f.submit(1)
	_, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 2, Kind: "work", Title: "theirs", Input: 1})
	require.NoError(t, err)
	oldDone := f.submit(1)
	require.NoError(t, f.db.Model(&model.AsyncTask{}).Where("id = ?", oldDone.ID).UpdateColumns(map[string]any{"status": model.AsyncSucceeded, "created_at": time.Now().Add(-48 * time.Hour)}).Error)

	list, err := f.tasks.ListOwned(context.Background(), 1, nil, time.Now().Add(-24*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, list, 1, "an old finished task is not listed")
	assert.Equal(t, mine.ID, list[0].ID)
	list, err = f.tasks.ListOwned(context.Background(), 1, []string{"other"}, time.Now().Add(-24*time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestTheFailuresThatCanBeTriedAgainAreToldFromTheOnesThatCannot(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
		code      string
	}{
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), true, model.AsyncCodeTimeout},
		{"net timeout", &net.DNSError{IsTimeout: true}, true, model.AsyncCodeTimeout},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true, model.AsyncCodeUnreachable},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true, model.AsyncCodeUnreachable},
		{"cut", io.ErrUnexpectedEOF, true, model.AsyncCodeUnreachable},
		{"prometheus 503", &promv1.Error{Type: promv1.ErrServer, Msg: "server error: 503"}, true, model.AsyncCodeUnreachable},
		{"prometheus bad query", &promv1.Error{Type: promv1.ErrBadData, Msg: "parse error"}, false, model.AsyncCodeFailed},
		{"plain", errors.New("the module does not exist"), false, model.AsyncCodeFailed},
		{"permanent deadline", Permanent(context.DeadlineExceeded), false, model.AsyncCodeFailed},
		{"forced", Retryable(errors.New("flaky")), true, model.AsyncCodeUnreachable},
	}
	for _, c := range cases {
		retryable, code := Classify(c.err)
		assert.Equal(t, c.retryable, retryable, c.name)
		assert.Equal(t, c.code, code, c.name)
	}
}

func TestTheWaitBetweenAttemptsDoublesUpToTheLongest(t *testing.T) {
	r := &Runner{RetryBase: 10 * time.Second, RetryMax: time.Minute}
	assert.Equal(t, 10*time.Second, r.retryDelay(1))
	assert.Equal(t, 20*time.Second, r.retryDelay(2))
	assert.Equal(t, 40*time.Second, r.retryDelay(3))
	assert.Equal(t, time.Minute, r.retryDelay(4))
	assert.Equal(t, time.Minute, r.retryDelay(9))
}

func TestOneUserMayHaveFiveOpenTasksAndTheSameQuestionIsTheSameTask(t *testing.T) {
	f := newFixture(t)
	first, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 9, Kind: "work", Title: "t", Dedup: "same", Input: 1})
	require.NoError(t, err)
	again, err := f.queue.SubmitSpec(context.Background(), Spec{Owner: 9, Kind: "work", Title: "t", Dedup: "same", Input: 1})
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	for i := 1; i < MaxOpenPerUser; i++ {
		_, err = f.queue.SubmitSpec(context.Background(), Spec{Owner: 9, Kind: "work", Title: "t", Dedup: fmt.Sprintf("n%d", i), Input: i})
		require.NoError(t, err)
	}
	_, err = f.queue.SubmitSpec(context.Background(), Spec{Owner: 9, Kind: "work", Title: "t", Dedup: "one more", Input: 99})
	assert.ErrorIs(t, err, ErrTooMany)
	// a task that is being cancelled still counts as open
	_, err = f.queue.Cancel(context.Background(), 9, first.ID)
	require.NoError(t, err)
	_, err = f.queue.SubmitSpec(context.Background(), Spec{Owner: 9, Kind: "work", Title: "t", Dedup: "after cancel", Input: 100})
	require.NoError(t, err)
}
