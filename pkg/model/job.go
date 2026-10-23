package model

import (
	"gorm.io/gorm"
	"time"
)

// Job is the main table for the background background job queue.
// Using the database instead of MQ ensures traceability and stateless multi-instance deployment.
type Job struct {
	ID        int64          `gorm:"primaryKey;autoIncrement" json:"id" column_setting:"true"`
	CreatedAt time.Time      `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time      `json:"updated_at" column_setting:"false"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`

	// JobType identifies a registered task implementation.
	JobType string `gorm:"type:varchar(50);index;not null" json:"job_type" column_setting:"true"`

	// Target entity type and identifier form a polymorphic reference (no
	// single table TargetID always points at), so it can't be a real FK; any
	// cleanup keyed off a specific target has to stay application-level (see
	// task handlers can identify work by target_type/target_id).
	TargetType string `gorm:"type:varchar(50)" json:"target_type" column_setting:"false"`
	TargetID   int64  `gorm:"index" json:"target_id" column_setting:"false"`

	// Status transitions: pending → running → success | failed
	// Note: there is no longer a retrying status. Retries are implemented by resetting back to pending + scheduled_at
	// This ensures each execution is a clean running state, facilitating auditing.
	Status string `gorm:"type:varchar(20);index;not null;default:'pending'" json:"status" column_setting:"true"`

	// Retry mechanism
	RetryCount          int     `gorm:"default:0" json:"retry_count" column_setting:"true"`
	MaxRetry            int     `gorm:"default:3" json:"max_retry" column_setting:"false"`
	RetryForever        bool    `gorm:"not null;default:false" json:"retry_forever" column_setting:"false"`
	RetryInitialSeconds int     `gorm:"not null;default:60" json:"retry_initial_seconds" column_setting:"false"`
	RetryBackoffFactor  float64 `gorm:"not null;default:2" json:"retry_backoff_factor" column_setting:"false"`
	RetryMaxSeconds     int     `gorm:"not null;default:3600" json:"retry_max_seconds" column_setting:"false"`

	// Scheduled execution time (nil means immediate execution)
	ScheduledAt *time.Time `gorm:"index" json:"scheduled_at" column_setting:"true"`
	StartedAt   *time.Time `json:"started_at" column_setting:"true"`
	FinishedAt  *time.Time `json:"finished_at" column_setting:"true"`

	// ── Idempotency Assurance Fields ────────────────────────────────────
	// IdempotencyKey is utilized for the unique constraint deduction during the Seeder phase (see UNIQUE index)
	// Format convention: {job_type}-{target_id}, e.g. "notify-42".
	// This ensures each resource has exactly one Job record per task type.
	IdempotencyKey string `gorm:"type:varchar(255);uniqueIndex:udx_idempotency,where:deleted_at IS NULL" json:"idempotency_key" column_setting:"false"`

	// ClaimedBy tracks the ID of the Worker instance (hostname + pid) that preemptively claimed this Job
	// Empty means it has not been claimed by any Worker yet
	ClaimedBy string `gorm:"type:varchar(255)" json:"claimed_by" column_setting:"false"`

	// ClaimExpiresAt implies the Worker must complete the task before this time; otherwise, it is considered a zombie Job, and the Recovery goroutine resets it
	// Increased on every Worker heartbeat (e.g. +30s)
	ClaimExpiresAt *time.Time `gorm:"index" json:"claim_expires_at" column_setting:"false"`
	// ─────────────────────────────────────────────────────

	// Custom task parameters (JSON string)
	Payload string `gorm:"type:text" json:"payload" column_setting:"false"`

	// Execution summary or error details
	Result string `gorm:"type:text" json:"result" column_setting:"true"`

	// ── Audit Display Fields ──────────────────────────────────────────
	TargetName string `gorm:"type:varchar(255);comment:Target name for audit visibility" json:"target_name" column_setting:"true"`
}

func (Job) TableName() string { return "job" }

func init() {
	RegisterModel(&Job{})
}
