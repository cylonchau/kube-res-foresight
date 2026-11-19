package model

import "time"

func init() {
	RegisterModel(&AsyncTask{})
	RegisterModel(&AsyncTaskEvent{})
}

// The states of an async task. queued -> running -> succeeded | failed | cancelled; a failed or cancelled task is retried by its owner, which
// queues it again. A task that was running when the platform stopped is run again by the job engine when its claim has expired.
const (
	AsyncQueued    = "queued"
	AsyncRunning   = "running"
	AsyncSucceeded = "succeeded"
	AsyncFailed    = "failed"
	AsyncCancelled = "cancelled"
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
