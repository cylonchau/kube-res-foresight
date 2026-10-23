package job

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"math"
	"os"
	"time"
)

const (
	ClaimDuration     = 5 * time.Minute
	HeartbeatInterval = 30 * time.Second
	RecoveryInterval  = 1 * time.Minute
)

func workerID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

type Scheduler struct {
	jobDAO       *rdb.JobDAO // Unique DB dependency, encapsulate all via DAO
	workerPool   chan struct{}
	pollInterval time.Duration
	workerID     string
}

func NewScheduler(jobDAO *rdb.JobDAO, maxWorkers int, pollInterval time.Duration) *Scheduler {
	return &Scheduler{
		jobDAO:       jobDAO,
		workerPool:   make(chan struct{}, maxWorkers),
		pollInterval: pollInterval,
		workerID:     workerID(),
	}
}

// idleInterval is how long the scheduler waits to look for jobs again when the last look found none. Looking is a
// transaction on the one database connection (SQLite), and every 10 milliseconds is a hundred of them a second for
// nothing; a job waits at most this long before it is claimed, and while there are jobs the short interval is used.
const idleInterval = 500 * time.Millisecond

func (s *Scheduler) Start(ctx context.Context) {
	logger.Info("Scheduler started", logger.String("worker_id", s.workerID))
	go s.runRecovery(ctx)

	timer := time.NewTimer(s.pollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			wait := s.pollInterval
			if s.poll(ctx) == 0 && wait < idleInterval {
				wait = idleInterval
			}
			timer.Reset(wait)
		}
	}
}

// poll claims the jobs that are due as far as there are free workers and reports how many it claimed.
func (s *Scheduler) poll(ctx context.Context) int {
	// Only claim as many jobs as we actually have free worker slots for.
	// Claiming a full batch while workers are still busy from the previous
	// poll would block this goroutine on the workerPool send below, freezing
	// the scheduler's main loop until slots free up.
	available := cap(s.workerPool) - len(s.workerPool)
	if available <= 0 {
		return 1 // all the workers are busy: look again soon, a slot may free up
	}

	// Atomically preempt via DAO without raw SQL
	jobs, err := s.jobDAO.Claim(ctx, s.workerID, available)
	if err != nil {
		return 0
	}
	if len(jobs) > 0 {
		logger.Info("Scheduler claimed jobs", logger.Int("count", len(jobs)))
	}
	for i := range jobs {
		job := jobs[i]
		s.workerPool <- struct{}{}
		go func(j model.Job) {
			defer func() { <-s.workerPool }()
			s.execute(ctx, &j)
		}(job)
	}
	return len(jobs)
}

func (s *Scheduler) execute(ctx context.Context, job *model.Job) {
	fields := jobLogFields(job)
	logger.Debug("Job execution started", fields...)

	// Mark as running via DAO
	if err := s.jobDAO.MarkRunning(ctx, job.ID, s.workerID); err != nil {
		if errors.Is(err, rdb.ErrJobNotOwned) {
			logger.Warn("Job claim was reassigned before it could start; discarding this attempt", fields...)
		} else {
			logger.Error("Failed to mark job running", append(fields, logger.Err(err))...)
		}
		return
	}

	// Start heartbeat to periodically extend claim_expires_at via DAO
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go s.heartbeat(heartbeatCtx, job.ID)

	task, ok := Get(job.JobType)
	if !ok {
		stopHeartbeat()
		s.markFailed(ctx, job, "unknown job type: "+job.JobType, fields)
		return
	}

	err := task.Execute(ctx, job)
	stopHeartbeat()

	if err == nil {
		// Mark as successful via DAO
		s.markSuccess(ctx, job, "OK", fields)
		logger.Debug("Job finished successfully", fields...)
		return
	}

	s.retryOrFail(ctx, job, err, fields)
}

// markSuccess/markFailed wrap the DAO calls with ErrJobNotOwned-aware
// logging: if the claim was reassigned to another worker in the meantime
// (RecoverStaleJobs), this worker's result is stale and must not be
// reported as a real error.
func (s *Scheduler) markSuccess(ctx context.Context, job *model.Job, result string, fields []logger.Field) {
	markSuccess(ctx, s.jobDAO, s.workerID, job, result, fields)
}

func (s *Scheduler) markFailed(ctx context.Context, job *model.Job, reason string, fields []logger.Field) {
	markFailed(ctx, s.jobDAO, s.workerID, job, reason, fields)
}

// retryOrFail processes a job failure: deferring to a handler-requested retry
// time, scheduling an exponential-backoff retry, or marking the job
// permanently failed once retries are exhausted. It always ends by releasing
// the claim (via one of the DAO calls, all of which clear claimed_by), so
// every caller is responsible for invoking this instead of returning early
// while the job is still claimed.
func (s *Scheduler) retryOrFail(ctx context.Context, job *model.Job, err error, fields []logger.Field) {
	retryOrFail(ctx, s.jobDAO, s.workerID, job, err, fields)
}

func markSuccess(ctx context.Context, jobDAO *rdb.JobDAO, workerID string, job *model.Job, result string, fields []logger.Field) {
	if err := jobDAO.MarkSuccess(ctx, job.ID, workerID, result); err != nil {
		if errors.Is(err, rdb.ErrJobNotOwned) {
			logger.Warn("Job claim was reassigned before completion could be recorded; discarding this result", fields...)
		} else {
			logger.Error("Failed to mark job success", append(fields, logger.Err(err))...)
		}
	}
}

func markFailed(ctx context.Context, jobDAO *rdb.JobDAO, workerID string, job *model.Job, reason string, fields []logger.Field) {
	if err := jobDAO.MarkFailed(ctx, job.ID, workerID, reason); err != nil {
		if errors.Is(err, rdb.ErrJobNotOwned) {
			logger.Warn("Job claim was reassigned before failure could be recorded; discarding this result", fields...)
		} else {
			logger.Error("Failed to mark job failed", append(fields, logger.Err(err))...)
		}
	}
}

func retryOrFail(ctx context.Context, jobDAO *rdb.JobDAO, workerID string, job *model.Job, err error, fields []logger.Field) {
	logReleaseErr := func(releaseErr error) {
		if releaseErr == nil {
			return
		}
		if errors.Is(releaseErr, rdb.ErrJobNotOwned) {
			logger.Warn("Job claim was reassigned before its outcome could be recorded; discarding this result", fields...)
		} else {
			logger.Error("Failed to persist job outcome", append(fields, logger.Err(releaseErr))...)
		}
	}

	nextRetry := job.RetryCount + 1
	var scheduled retryAtError
	if errors.As(err, &scheduled) && scheduled.RetryAt().After(time.Now()) {
		retryAt := scheduled.RetryAt()
		logReleaseErr(jobDAO.DeferUntil(ctx, job.ID, workerID, retryAt, err.Error()))
		logger.Warn("Job reset until requested retry time", append(fields,
			logger.Any("retry_at", retryAt),
			logger.Err(err))...)
		return
	}
	if !job.RetryForever && nextRetry >= job.MaxRetry {
		logReleaseErr(jobDAO.MarkFailed(ctx, job.ID, workerID, err.Error()))
		logger.Error("Job failed permanently", append(fields, logger.Err(err))...)
		return
	}
	initial := job.RetryInitialSeconds
	if initial <= 0 {
		initial = 60
	}
	factor := job.RetryBackoffFactor
	if factor < 1 {
		factor = 2
	}
	maxDelay := job.RetryMaxSeconds
	if maxDelay <= 0 {
		maxDelay = 3600
	}
	seconds := float64(initial) * math.Pow(factor, float64(nextRetry-1))
	if seconds > float64(maxDelay) {
		seconds = float64(maxDelay)
	}
	delay := time.Duration(seconds * float64(time.Second))
	logReleaseErr(jobDAO.ScheduleRetry(ctx, job.ID, workerID, nextRetry, time.Now().Add(delay), err.Error()))
	retryFields := append(fields,
		logger.Int("retry_count", nextRetry),
		logger.Any("delay", delay),
		logger.Err(err))
	logger.Warn("Job failed, scheduled retry", retryFields...)
}

type retryAtError interface {
	error
	RetryAt() time.Time
}

func jobLogFields(job *model.Job) []logger.Field {
	return []logger.Field{
		logger.Int64("job_id", job.ID),
		logger.String("job_type", job.JobType),
		logger.String("target_type", job.TargetType),
		logger.Int64("target_id", job.TargetID),
		logger.String("target_name", job.TargetName),
	}
}

func (s *Scheduler) heartbeat(ctx context.Context, jobID int64) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Extend heartbeat via DAO
			s.jobDAO.ExtendClaim(ctx, jobID, s.workerID)
		}
	}
}

func (s *Scheduler) runRecovery(ctx context.Context) {
	ticker := time.NewTicker(RecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Recover zombie Jobs via DAO
			count, err := s.jobDAO.RecoverStaleJobs(ctx)
			if err == nil && count > 0 {
				logger.Info("Recovery reset stale jobs", logger.Int64("count", count))
			}
		}
	}
}
