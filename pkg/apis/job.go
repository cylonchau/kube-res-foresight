package apis

import "time"

type JobDTO struct {
	ID                  int64      `json:"id"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	JobType             string     `json:"job_type"`
	TargetType          string     `json:"target_type"`
	TargetID            int64      `json:"target_id"`
	Status              string     `json:"status"`
	RetryCount          int        `json:"retry_count"`
	MaxRetry            int        `json:"max_retry"`
	RetryForever        bool       `json:"retry_forever"`
	RetryInitialSeconds int        `json:"retry_initial_seconds"`
	RetryBackoffFactor  float64    `json:"retry_backoff_factor"`
	RetryMaxSeconds     int        `json:"retry_max_seconds"`
	ScheduledAt         *time.Time `json:"scheduled_at"`
	StartedAt           *time.Time `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at"`
	IdempotencyKey      string     `json:"idempotency_key"`
	ClaimedBy           string     `json:"claimed_by"`
	ClaimExpiresAt      *time.Time `json:"claim_expires_at"`
	Payload             string     `json:"payload"`
	Result              string     `json:"result"`
	TargetName          string     `json:"target_name"`
}

type JobLogReq struct {
	ListOptions
	JobType string `form:"job_type"`
	// TargetType/TargetID filter by the exact target a job ran against.
	// TargetKeyword searches the persisted human-readable target name.
	TargetType    string `form:"target_type"`
	TargetID      int64  `form:"target_id"`
	TargetKeyword string `form:"target_keyword"`
	Status        string `form:"status"`
	Latest        bool   `form:"latest"`
}

type JobRetryReq struct {
	ID int64 `json:"id" binding:"required"`
}
