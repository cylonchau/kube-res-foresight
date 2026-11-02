package rdb

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type JobDAO struct {
	db *gorm.DB
}

func NewJobDAO(db *gorm.DB) *JobDAO {
	return &JobDAO{db: db}
}

// ErrJobNotOwned is returned by the terminal-state transition methods
// (MarkRunning, MarkSuccess, MarkFailed, ScheduleRetry, DeferUntil) when the
// job is no longer claimed by the given workerID — e.g. RecoverStaleJobs
// already reassigned it to another worker because this worker's heartbeat
// lagged. Callers should treat this as "discard my result, someone else owns
// this job now" rather than a hard failure.
var ErrJobNotOwned = errors.New("job is no longer owned by this worker")

// updateOwnedJob applies updates to a job only if it is still claimed by
// workerID, guarding against a stale worker (recovered by RecoverStaleJobs
// and reclaimed by someone else) clobbering another worker's state.
func (dao *JobDAO) updateOwnedJob(ctx context.Context, id int64, workerID string, updates map[string]interface{}) error {
	result := dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND claimed_by = ?", id, workerID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrJobNotOwned
	}
	return nil
}

// Claim atomically preempts a batch of pending Jobs
// ⚑ Idempotent: UPDATE WHERE claimed_by=” ensures only one multi-instance worker succeeds
func (dao *JobDAO) Claim(ctx context.Context, workerID string, limit int) ([]model.Job, error) {
	now := time.Now()
	var jobs []model.Job
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []int64
		if err := tx.Model(&model.Job{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status = 'pending' AND claimed_by = '' AND (scheduled_at IS NULL OR scheduled_at <= ?)", now).
			Order("id ASC").
			Limit(limit).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.Model(&model.Job{}).
			Where("id IN ? AND status = 'pending' AND claimed_by = ''", ids).
			Updates(map[string]interface{}{
				"claimed_by":       workerID,
				"claim_expires_at": now.Add(5 * time.Minute),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return tx.Where("id IN ? AND claimed_by = ? AND status = 'pending'", ids, workerID).Find(&jobs).Error
	})
	return jobs, err
}

// MarkRunning flags Job as running. Fails with ErrJobNotOwned if workerID no
// longer holds the claim (e.g. it was reassigned by RecoverStaleJobs).
func (dao *JobDAO) MarkRunning(ctx context.Context, id int64, workerID string) error {
	return dao.updateOwnedJob(ctx, id, workerID, map[string]interface{}{"status": "running", "started_at": time.Now()})
}

// MarkSuccess flags Job as successfully completed. Fails with ErrJobNotOwned
// if workerID no longer holds the claim.
func (dao *JobDAO) MarkSuccess(ctx context.Context, id int64, workerID string, result string) error {
	return dao.updateOwnedJob(ctx, id, workerID, map[string]interface{}{
		"status": "success", "finished_at": time.Now(),
		"claimed_by": "", "claim_expires_at": nil, "result": result,
	})
}

// MarkFailed flags Job as failed. Fails with ErrJobNotOwned if workerID no
// longer holds the claim.
func (dao *JobDAO) MarkFailed(ctx context.Context, id int64, workerID string, reason string) error {
	return dao.updateOwnedJob(ctx, id, workerID, map[string]interface{}{
		"status": "failed", "finished_at": time.Now(),
		"claimed_by": "", "claim_expires_at": nil, "result": reason,
	})
}

// ScheduleRetry resets the Job back to pending awaiting retry (exponential
// backoff). Fails with ErrJobNotOwned if workerID no longer holds the claim.
func (dao *JobDAO) ScheduleRetry(ctx context.Context, id int64, workerID string, nextRetry int, retryAt time.Time, lastErr string) error {
	return dao.updateOwnedJob(ctx, id, workerID, map[string]interface{}{
		"status": "pending", "retry_count": nextRetry, "scheduled_at": retryAt,
		"claimed_by": "", "claim_expires_at": nil, "result": lastErr,
	})
}

// DeferUntil resets the Job to pending for a handler-requested retry time
// without consuming retry budget. Fails with ErrJobNotOwned if workerID no
// longer holds the claim.
func (dao *JobDAO) DeferUntil(ctx context.Context, id int64, workerID string, retryAt time.Time, lastErr string) error {
	return dao.updateOwnedJob(ctx, id, workerID, map[string]interface{}{
		"status": "pending", "scheduled_at": retryAt,
		"claimed_by": "", "claim_expires_at": nil, "result": lastErr,
	})
}

func (dao *JobDAO) MarkObsoleteByTargets(ctx context.Context, jobType, targetType string, targetIDs []int64, reason string) error {
	if len(targetIDs) == 0 {
		return nil
	}
	return dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("job_type = ? AND target_type = ? AND target_id IN ? AND status IN ?", jobType, targetType, targetIDs, []string{"pending", "running"}).
		Updates(map[string]interface{}{
			"status":           "failed",
			"retry_count":      gorm.Expr("max_retry"),
			"scheduled_at":     nil,
			"finished_at":      time.Now(),
			"claimed_by":       "",
			"claim_expires_at": nil,
			"result":           reason,
		}).Error
}

// ExtendClaim prolongs the heartbeat, avoiding the task from being misjudged as a zombie by Recovery
func (dao *JobDAO) ExtendClaim(ctx context.Context, id int64, workerID string) error {
	return dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND claimed_by = ?", id, workerID).
		Update("claim_expires_at", time.Now().Add(5*time.Minute)).Error
}

// RecoverStaleJobs reverts timed-out claimed Jobs to pending (utilized by
// Recovery goroutine). This covers both status='running' (a task is actually
// executing but its heartbeat lagged) and status='pending' with claimed_by
// still set (Claim() succeeded but execute() returned before ever calling
// MarkRunning — e.g. an over-claimed batch stuck waiting for a worker slot,
// or an early-return on a pre-flight check error). Without the pending
// branch, jobs stuck in that window are never recovered by anything.
func (dao *JobDAO) RecoverStaleJobs(ctx context.Context) (int64, error) {
	result := dao.db.WithContext(ctx).
		Model(&model.Job{}).
		Where("claimed_by != '' AND claim_expires_at < ? AND (status = 'running' OR status = 'pending')", time.Now()).
		Updates(map[string]interface{}{
			"status": "pending", "claimed_by": "", "claim_expires_at": nil,
			"result": "recovered from stale claim",
		})
	return result.RowsAffected, result.Error
}

// Create inserts a new Job record
func (dao *JobDAO) Create(ctx context.Context, job *model.Job) (*model.Job, error) {
	err := dao.db.WithContext(ctx).Create(job).Error
	return job, err
}

// JobTypes lists the job types that exist in the job table.
func (dao *JobDAO) JobTypes(ctx context.Context) ([]string, error) {
	var types []string
	err := dao.db.WithContext(ctx).Model(&model.Job{}).Distinct().Where("job_type <> ''").Order("job_type ASC").Pluck("job_type", &types).Error
	return types, err
}

// CreateIdempotent performs idempotent creation of Jobs (ON CONFLICT DO NOTHING)
func (dao *JobDAO) CreateIdempotent(ctx context.Context, job *model.Job) error {
	return dao.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(job).Error
}

// UpsertManual keeps one manual job per idempotency key. A newer request
// overwrites the previous pending/running/final state and requeues the job.
func (dao *JobDAO) UpsertManual(ctx context.Context, job *model.Job) (*model.Job, error) {
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(job).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Job{}).
			Where("idempotency_key = ?", job.IdempotencyKey).
			Updates(map[string]interface{}{
				"job_type":         job.JobType,
				"target_type":      job.TargetType,
				"target_id":        job.TargetID,
				"target_name":      job.TargetName,
				"status":           "pending",
				"retry_count":      0,
				"max_retry":        job.MaxRetry,
				"scheduled_at":     job.ScheduledAt,
				"started_at":       nil,
				"finished_at":      nil,
				"claimed_by":       "",
				"claim_expires_at": nil,
				"payload":          job.Payload,
				"result":           "manual request overwritten",
			}).Error; err != nil {
			return err
		}
		return tx.Where("idempotency_key = ?", job.IdempotencyKey).First(job).Error
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// UpsertCoalesced keeps one logical job per idempotency key. Pending and
// completed jobs are overwritten and requeued. A running job is not reset under
// its executor; only its payload/metadata are refreshed so new requests are
// coalesced into the already-running slot instead of creating duplicates.
func (dao *JobDAO) UpsertCoalesced(ctx context.Context, job *model.Job) (*model.Job, error) {
	err := dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(job).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Job{}).
			Where("idempotency_key = ? AND status <> ?", job.IdempotencyKey, "running").
			Updates(map[string]interface{}{
				"job_type":         job.JobType,
				"target_type":      job.TargetType,
				"target_id":        job.TargetID,
				"target_name":      job.TargetName,
				"status":           "pending",
				"retry_count":      0,
				"max_retry":        job.MaxRetry,
				"scheduled_at":     job.ScheduledAt,
				"started_at":       nil,
				"finished_at":      nil,
				"claimed_by":       "",
				"claim_expires_at": nil,
				"payload":          job.Payload,
				"result":           "coalesced request overwritten",
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Job{}).
			Where("idempotency_key = ? AND status = ?", job.IdempotencyKey, "running").
			Updates(map[string]interface{}{
				"job_type":    job.JobType,
				"target_type": job.TargetType,
				"target_id":   job.TargetID,
				"target_name": job.TargetName,
				"max_retry":   job.MaxRetry,
				"payload":     job.Payload,
				"result":      "coalesced while running",
			}).Error; err != nil {
			return err
		}
		if err := deleteCoalescedDuplicates(tx, job); err != nil {
			return err
		}
		return tx.Where("idempotency_key = ?", job.IdempotencyKey).First(job).Error
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// UpsertRecurring ensures a Job exists and is set to pending for the next cycle.
// If it already exists and is in a final state (success/failed), it resets it.
// If it's already pending or running, it avoids disrupting the current execution flow.
func (dao *JobDAO) UpsertRecurring(ctx context.Context, job *model.Job) error {
	// 1. Try to create (idempotent)
	err := dao.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(job).Error
	if err != nil {
		return err
	}

	// 2. If already exists, reset to pending if it's in a final state
	return dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("idempotency_key = ? AND status IN ?", job.IdempotencyKey, []string{"success", "failed"}).
		Where("NOT (status = ? AND result LIKE ?)", "failed", "obsolete:%").
		Updates(map[string]interface{}{
			"status":           "pending",
			"retry_count":      0,
			"scheduled_at":     job.ScheduledAt,
			"payload":          job.Payload,
			"result":           "re-scheduled",
			"claimed_by":       "",
			"claim_expires_at": nil,
			"started_at":       nil,
			"finished_at":      nil,
		}).Error
}

// UpsertRecurringCoalesced is the recurring-task variant of UpsertCoalesced.
// It uses a target-scoped idempotency key, but deliberately does not disturb
// pending/running jobs. This preserves retry backoff and avoids a scheduler
// tick turning a timed-out external call into an immediate retry loop.
func (dao *JobDAO) UpsertRecurringCoalesced(ctx context.Context, job *model.Job) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(job).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Job{}).
			Where("idempotency_key = ? AND status IN ?", job.IdempotencyKey, []string{"success", "failed"}).
			Where("NOT (status = ? AND result LIKE ?)", "failed", "obsolete:%").
			Updates(map[string]interface{}{
				"job_type":         job.JobType,
				"target_type":      job.TargetType,
				"target_id":        job.TargetID,
				"target_name":      job.TargetName,
				"status":           "pending",
				"retry_count":      0,
				"max_retry":        job.MaxRetry,
				"scheduled_at":     job.ScheduledAt,
				"payload":          job.Payload,
				"result":           "re-scheduled",
				"claimed_by":       "",
				"claim_expires_at": nil,
				"started_at":       nil,
				"finished_at":      nil,
			}).Error; err != nil {
			return err
		}
		return deleteCoalescedDuplicates(tx, job)
	})
}

func deleteCoalescedDuplicates(tx *gorm.DB, job *model.Job) error {
	if job == nil || job.JobType == "" || job.TargetType == "" || job.TargetID == 0 || job.IdempotencyKey == "" {
		return nil
	}
	return tx.Where(
		"job_type = ? AND target_type = ? AND target_id = ? AND idempotency_key <> ? AND status <> ?",
		job.JobType, job.TargetType, job.TargetID, job.IdempotencyKey, "running",
	).Delete(&model.Job{}).Error
}

// List returns a paginated list of Jobs based on filters
func (dao *JobDAO) List(ctx context.Context, opt *apis.JobLogReq) ([]model.Job, int64, error) {
	var jobs []model.Job
	var total int64
	db := dao.db.WithContext(ctx).Model(&model.Job{})

	if opt.JobType != "" {
		db = db.Where("job_type = ?", opt.JobType)
	}
	if opt.TargetType != "" {
		db = db.Where("target_type = ?", opt.TargetType)
	}
	if opt.TargetID != 0 {
		db = db.Where("target_id = ?", opt.TargetID)
	}
	if opt.TargetKeyword != "" {
		// Re-states the same COALESCE the SELECT above computes target_name
		// from, rather than filtering on the "target_name" alias directly —
		// aliasing an expression into a WHERE clause isn't portable across
		// the sql drivers this DAO runs against (works on some, silently
		// wrong or rejected on others), while repeating the expression is.
		db = db.Where("target_name LIKE ?", "%"+opt.TargetKeyword+"%")
	}
	if opt.Status != "" {
		db = db.Where("status = ?", opt.Status)
	}
	if opt.Keyword != "" {
		db = db.Where("result LIKE ?", "%"+opt.Keyword+"%")
	}
	if opt.Latest {
		db = db.Where(`id IN (
			SELECT MAX(j2.id)
			FROM job j2
			WHERE j2.deleted_at IS NULL
			GROUP BY j2.job_type, j2.target_type, j2.target_id
		)`)
	}

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := db.Order("id desc").Offset(int(opt.Offset())).Limit(int(opt.PageSize)).Find(&jobs).Error
	return jobs, total, err
}

// GetByID returns a job by its ID
func (dao *JobDAO) GetByID(ctx context.Context, id int64) (*model.Job, error) {
	var job model.Job
	err := dao.db.WithContext(ctx).Where("id = ?", id).First(&job).Error
	return &job, err
}

// Reset clears a job's claim and status to pending for retry
// ResetMany puts several jobs back to pending for a manual retry and returns how many moved. A job a
// worker holds (running, or claimed) is left alone instead of being taken from under it.
func (dao *JobDAO) ResetMany(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("id IN ? AND status <> 'running' AND (claimed_by = '' OR claimed_by IS NULL OR status <> 'pending')", ids).
		Updates(map[string]interface{}{
			"status":           "pending",
			"claimed_by":       "",
			"claim_expires_at": nil,
			"started_at":       nil,
			"finished_at":      nil,
			"retry_count":      0,
			"result":           "manual reset",
		})
	return res.RowsAffected, res.Error
}

func (dao *JobDAO) Reset(ctx context.Context, id int64) error {
	return dao.db.WithContext(ctx).Model(&model.Job{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":           "pending",
			"claimed_by":       "",
			"claim_expires_at": nil,
			"started_at":       nil,
			"finished_at":      nil,
			"retry_count":      0,
			"result":           "manual reset",
		}).Error
}

// HasOpenJob says whether a job of the target is waiting or running. A scheduled task whose last run has not finished is
// not queued again: stacked runs execute side by side, each one slows the others, and the pile only grows.
func (dao *JobDAO) HasOpenJob(ctx context.Context, jobType, targetType string, targetID int64) (bool, error) {
	var n int64
	err := dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("job_type = ? AND target_type = ? AND target_id = ? AND status IN ?", jobType, targetType, targetID, []string{"pending", "running"}).
		Limit(1).Count(&n).Error
	return n > 0, err
}

// SupersedeStaleScheduled drops the runs of scheduled tasks that piled up while the engine could not keep up: of the
// waiting jobs of one task only the newest is kept, the others are closed as obsolete (running them late has no value,
// the next run reads the same data). It reports how many were closed.
func (dao *JobDAO) SupersedeStaleScheduled(ctx context.Context) (int64, error) {
	res := dao.db.WithContext(ctx).Model(&model.Job{}).
		Where("target_type = ? AND status = ?", "task_definition", "pending").
		Where("id NOT IN (?)", dao.db.Model(&model.Job{}).Select("MAX(id)").
			Where("target_type = ? AND status = ?", "task_definition", "pending").Group("job_type, target_id")).
		Updates(map[string]interface{}{
			"status": "failed", "retry_count": gorm.Expr("max_retry"), "scheduled_at": nil, "finished_at": time.Now(),
			"claimed_by": "", "claim_expires_at": nil, "result": "obsolete: a newer scheduled run replaced it",
		})
	return res.RowsAffected, res.Error
}

// JobCount is how many jobs of a type are in a status.
type JobCount struct {
	Type   string
	Status string
	Count  int64
}

// CountsSince counts the jobs created since the time, by type and status.
func (dao *JobDAO) CountsSince(ctx context.Context, since time.Time) ([]JobCount, error) {
	var out []JobCount
	err := dao.db.WithContext(ctx).Model(&model.Job{}).Select("job_type AS type, status, COUNT(*) AS count").
		Where("created_at >= ?", since).Group("job_type, status").Scan(&out).Error
	return out, err
}
