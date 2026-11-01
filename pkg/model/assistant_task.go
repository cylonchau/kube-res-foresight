package model

import "time"

func init() {
	RegisterModel(&AssistantTask{})
	RegisterModel(&AssistantTaskRun{})
}

// Where the task came from.
const (
	AITaskFromChat = "chat"
	AITaskFromPage = "page"
)

// Statuses of a run of an AI task.
const (
	AIRunRunning = "running"
	AIRunSuccess = "success"
	AIRunFailed  = "failed"
)

// AssistantTask is an AI scheduled task: on a schedule the platform puts Prompt to the language model, which answers with the read-only
// tools of the assistant, and the answer is kept and sent to the recipients. It is a framework, not a feature: what a task does is whatever
// its prompt asks ("every Monday, send the usage status of all modules"), and the platform has no code that knows about it.
type AssistantTask struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name      string `gorm:"type:varchar(100);not null" json:"name" column_setting:"true"`
	OwnerID   uint   `gorm:"not null;default:0;index" json:"owner_id" column_setting:"false"`
	OwnerName string `gorm:"type:varchar(128)" json:"owner_name" column_setting:"true"`
	// Prompt is the question put to the model at each run.
	Prompt string `gorm:"type:text;not null" json:"prompt" column_setting:"false"`
	// Schedule is a cron expression (5 fields, minute first; 6 with seconds; @every <duration>) read in the zone of the platform.
	Schedule string `gorm:"type:varchar(100);not null" json:"schedule" column_setting:"true"`
	Enabled  bool   `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
	// LLMID is the model the task asks; 0 is the default one.
	LLMID uint `gorm:"column:llm_id;not null;default:0" json:"llm_id" column_setting:"false"`
	// Lang is the language of the report, zh or en.
	Lang string `gorm:"type:varchar(5);not null;default:'zh'" json:"lang" column_setting:"false"`
	// Recipients is a JSON list of the addresses the report is mailed to.
	Recipients string `gorm:"type:text" json:"-" column_setting:"false"`
	Source     string `gorm:"type:varchar(10);not null;default:'page'" json:"source" column_setting:"true"`

	NextRunAt  *time.Time `gorm:"index" json:"next_run_at" column_setting:"true"`
	LastRunAt  *time.Time `json:"last_run_at" column_setting:"true"`
	LastStatus string     `gorm:"type:varchar(10)" json:"last_status" column_setting:"true"`
	LastError  string     `gorm:"type:varchar(500)" json:"last_error" column_setting:"false"`
}

func (AssistantTask) TableName() string { return "assistant_task" }

// AssistantTaskRun is one run of an AI task: what the model answered, and to whom it was delivered.
type AssistantTaskRun struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	TaskID uint `gorm:"not null;index" json:"task_id" column_setting:"true"`
	// JobID is the job of the task center that made the run (a retry of the job continues the same run); 0 when it was not made by one.
	JobID      uint       `gorm:"not null;default:0;index" json:"job_id" column_setting:"false"`
	Trigger    string     `gorm:"type:varchar(10)" json:"trigger" column_setting:"true"`
	Status     string     `gorm:"type:varchar(10);not null" json:"status" column_setting:"true"`
	StartedAt  time.Time  `json:"started_at" column_setting:"true"`
	FinishedAt *time.Time `json:"finished_at" column_setting:"true"`
	// Output is the report; Tools names the tools the model read with; Delivered is a JSON list of the addresses it was sent to.
	Output        string `gorm:"type:text" json:"output" column_setting:"false"`
	Tools         string `gorm:"type:varchar(500)" json:"tools" column_setting:"false"`
	Delivered     string `gorm:"type:text" json:"-" column_setting:"false"`
	Error         string `gorm:"type:varchar(500)" json:"error" column_setting:"false"`
	DeliveryError string `gorm:"type:varchar(500)" json:"delivery_error" column_setting:"false"`
}

func (AssistantTaskRun) TableName() string { return "assistant_task_run" }
