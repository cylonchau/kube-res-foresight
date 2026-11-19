package model

import "time"

func init() {
	RegisterModel(&AsyncTask{})
	RegisterModel(&AsyncTaskEvent{})
}

// The states of an async task (P11 B1). queued -> running -> succeeded | failed | cancelled. A failure that can be tried again (a timeout, a data source
// that could not be reached) puts the task back to queued with the time of the next attempt until its attempts are used; a running task that stops
// reporting for a minute (the process died) is put back to queued by the sweeper, and the job engine runs it again. The owner asks for a stop of a
// running task with cancel: it is cancelling until the worker has stopped, then cancelled. A failed or cancelled task is queued again by its owner.
// A finished task keeps its result for the retention (assistant.async_retention_hours), then it is expired: the result and what it was given are
// gone and only the record stays, for a month more.
const (
	AsyncQueued     = "queued"
	AsyncRunning    = "running"
	AsyncCancelling = "cancelling"
	AsyncSucceeded  = "succeeded"
	AsyncFailed     = "failed"
	AsyncCancelled  = "cancelled"
	AsyncExpired    = "expired"
)

// AsyncOpen are the states of a task that has not ended.
var AsyncOpen = []string{AsyncQueued, AsyncRunning, AsyncCancelling}

// The codes of why a task failed or was put back.
const (
	AsyncCodeTimeout     = "timeout"
	AsyncCodeUnreachable = "unreachable"
	AsyncCodeWorkerLost  = "worker_lost"
	AsyncCodeFailed      = "failed"
)

// AsyncTask is work a user asked for that takes longer than one request may: it is queued (a job of the task center runs it), and the user comes
// back to read its progress and result. It belongs to the user who asked; only they read, cancel or retry it.
type AsyncTask struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	OwnerID   uint   `gorm:"not null;index" json:"-"`
	OwnerName string `gorm:"type:varchar(128)" json:"-"`
	// Kind says which handler runs it (assistant.chat, ...).
	Kind  string `gorm:"type:varchar(40);not null;index" json:"kind"`
	Title string `gorm:"type:varchar(120)" json:"title"`

	Status   string `gorm:"type:varchar(12);not null;index" json:"status"`
	Progress int    `gorm:"not null;default:0" json:"progress"`
	Stage    string `gorm:"type:varchar(100)" json:"stage"`
	// Payload is the JSON the handler is given; Result the JSON it answered. Both are for the owner only.
	Payload string `gorm:"type:text" json:"-"`
	Result  string `gorm:"type:text" json:"-"`
	Error   string `gorm:"type:varchar(500)" json:"error"`
	// Partial is the text of the answer written so far, while the task runs (the page shows it as it grows); empty once the task ends.
	Partial string `gorm:"type:text" json:"-"`

	// JobID is the job of the task center that runs it now.
	JobID           int64 `gorm:"not null;default:0" json:"-"`
	CancelRequested bool  `gorm:"not null;default:false" json:"-"`
	// DedupKey makes a second identical request while one is open the same task.
	DedupKey string `gorm:"type:varchar(100);index" json:"-"`
	Attempts int    `gorm:"not null;default:0" json:"attempts"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `gorm:"index" json:"finished_at"`

	// ParamsHash is the hash of what the task was given: with the kind and the owner it is what two requests have in common when they ask the same thing.
	ParamsHash string `gorm:"type:varchar(64)" json:"-"`
	// MaxAttempts is how many times the task may be run for a failure that can be tried again; Retryable says whether the last failure was one.
	MaxAttempts int  `gorm:"not null;default:1" json:"-"`
	Retryable   bool `gorm:"not null;default:false" json:"-"`
	// NextRunAt is when the next attempt starts, while the task waits for it.
	NextRunAt *time.Time `json:"-"`
	// HeartbeatAt is the last time the worker said it was alive; a running task whose heartbeat is older than a minute is lost.
	HeartbeatAt *time.Time `json:"-"`
	// ExpiresAt is when the result is dropped.
	ExpiresAt *time.Time `gorm:"index" json:"-"`
	// HasPartial says PartialResult holds what the task has done so far; it is kept when the task fails or is cancelled.
	HasPartial    bool   `gorm:"not null;default:false" json:"-"`
	PartialResult string `gorm:"type:text" json:"-"`
	// FromCache is true when the result is one that was already worked out (P11 B3), not computed for this task.
	FromCache bool   `gorm:"not null;default:false" json:"-"`
	ErrorCode string `gorm:"type:varchar(32)" json:"-"`
	// TraceID is the request id of the request that queued the task: the same id is in the operation log, the audit of the assistant and the log of the gateway.
	TraceID string `gorm:"type:varchar(64);index" json:"-"`
}

func (AsyncTask) TableName() string { return "async_task" }

// AsyncTaskEvent is one step a task reported (a tool read, a stage reached), numbered in order, so a page that was closed reads on from the last
// one it saw.
type AsyncTaskEvent struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	TaskID    uint      `gorm:"not null;index:idx_async_event,priority:1" json:"-"`
	Seq       int       `gorm:"not null;index:idx_async_event,priority:2" json:"seq"`
	Type      string    `gorm:"type:varchar(20)" json:"type"`
	Data      string    `gorm:"type:text" json:"-"`
}

func (AsyncTaskEvent) TableName() string { return "async_task_event" }
