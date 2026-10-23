package apis

import "time"

type TaskDefinitionDTO struct {
	ID                       uint      `json:"id"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
	TaskType                 string    `json:"task_type"`
	Description              string    `json:"description"`
	CronExpr                 string    `json:"cron_expr"`
	Enabled                  bool      `json:"enabled"`
	Params                   string    `json:"params"`
	MaxRetry                 int       `json:"max_retry"`
	RetryForever             bool      `json:"retry_forever"`
	RetryInitialSeconds      int       `json:"retry_initial_seconds"`
	RetryBackoffFactor       float64   `json:"retry_backoff_factor"`
	RetryMaxSeconds          int       `json:"retry_max_seconds"`
	ExecutionIntervalSeconds int       `json:"execution_interval_seconds"`
	WatchIntervalSeconds     int       `json:"watch_interval_seconds"`
	LastRunDisplay           string    `json:"last_run_time"`
	NextRunDisplay           string    `json:"next_run_time"`
}

type TaskUpdateReq struct {
	CronExpr                 *string  `json:"cron_expr"`
	Enabled                  *bool    `json:"enabled"`
	Description              *string  `json:"description"`
	Params                   *string  `json:"params"`
	MaxRetry                 *int     `json:"max_retry" binding:"omitempty,min=1"`
	RetryForever             *bool    `json:"retry_forever"`
	RetryInitialSeconds      *int     `json:"retry_initial_seconds" binding:"omitempty,min=1"`
	RetryBackoffFactor       *float64 `json:"retry_backoff_factor" binding:"omitempty,min=1"`
	RetryMaxSeconds          *int     `json:"retry_max_seconds" binding:"omitempty,min=1"`
	ExecutionIntervalSeconds *int     `json:"execution_interval_seconds" binding:"omitempty,min=0"`
	WatchIntervalSeconds     *int     `json:"watch_interval_seconds" binding:"omitempty,min=0"`
}

type TaskListReq struct {
	ListOptions
}
