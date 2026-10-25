package job

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"time"
)

// ExecuteNow persists j and synchronously drives it through the same
// lifecycle a poll()-claimed job gets (MarkRunning -> run -> MarkSuccess, or
// retry/fail on error), for callers that need the result immediately instead
// of waiting for the next poll when a follow-up must start immediately.
// record actually published before it can ask the CA to validate.
//
// Without this, callers that need synchronous execution were constructing a
// throwaway *model.Job and calling a service's Execute directly: never
// persisted, invisible in the Jobs admin UI, no retry/backoff on failure,
// and any error either had to be hand-propagated or (worse) silently logged
// and dropped.
//
// j must not have ID, Status or ClaimedBy set — ExecuteNow assigns them
// before creating the row. On success it returns nil; on failure it persists
// the retry/failure state (same backoff policy as job.MaxRetry/RetryCount
// would drive if this were a normal queued job) and returns the original
// error so the caller's own control flow can react to it.
func ExecuteNow(ctx context.Context, jobDAO *rdb.JobDAO, j *model.Job, run func(ctx context.Context, job *model.Job) error) error {
	owner := "inline-" + workerID()
	now := time.Now()
	expires := now.Add(ClaimDuration)
	j.Status = "pending"
	j.ClaimedBy = owner
	j.ClaimExpiresAt = &expires
	if j.MaxRetry <= 0 {
		j.MaxRetry = 3
	}

	if _, err := jobDAO.Create(ctx, j); err != nil {
		return fmt.Errorf("persist inline job: %w", err)
	}
	fields := jobLogFields(j)

	if err := jobDAO.MarkRunning(ctx, j.ID, owner); err != nil {
		logger.Error("Failed to mark inline job running", append(fields, logger.Err(err))...)
		return err
	}

	runErr := run(ctx, j)
	if runErr == nil {
		markSuccess(ctx, jobDAO, owner, j, "OK", fields)
		return nil
	}

	retryOrFail(ctx, jobDAO, owner, j, runErr, fields)
	return runErr
}
