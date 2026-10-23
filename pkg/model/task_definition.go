package model

import (
	"gorm.io/gorm"
	"time"
)

func init() {
	RegisterModel(&TaskDefinition{})
}

// TaskDefinition defines the scheduling rules for background jobs
type TaskDefinition struct {
	ID        uint           `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time      `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time      `json:"updated_at" column_setting:"false"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-" column_setting:"false"`

	TaskType                 string  `gorm:"type:varchar(50);uniqueIndex;not null" json:"task_type" column_setting:"true"` // e.g., notify, cleanup
	Description              string  `gorm:"type:varchar(255)" json:"description" column_setting:"true"`
	CronExpr                 string  `gorm:"type:varchar(100);not null" json:"cron_expr" column_setting:"true"` // ms/sec/min/hour/day/month/week, e.g. */100 * * * * * *
	Enabled                  bool    `gorm:"default:true" json:"enabled" column_setting:"true"`
	Params                   string  `gorm:"type:text" json:"params" column_setting:"true"` // task-specific JSON params
	MaxRetry                 int     `gorm:"not null;default:3" json:"max_retry" column_setting:"false"`
	RetryForever             bool    `gorm:"not null;default:false" json:"retry_forever" column_setting:"false"`
	RetryInitialSeconds      int     `gorm:"not null;default:60" json:"retry_initial_seconds" column_setting:"false"`
	RetryBackoffFactor       float64 `gorm:"not null;default:2" json:"retry_backoff_factor" column_setting:"false"`
	RetryMaxSeconds          int     `gorm:"not null;default:3600" json:"retry_max_seconds" column_setting:"false"`
	ExecutionIntervalSeconds int     `gorm:"not null;default:0" json:"execution_interval_seconds" column_setting:"false"`
	WatchIntervalSeconds     int     `gorm:"not null;default:0" json:"watch_interval_seconds" column_setting:"false"`

	LastRunTime *time.Time `json:"-" column_setting:"false"`
	NextRunTime *time.Time `gorm:"index" json:"-" column_setting:"false"`

	// Virtual display fields
	LastRunDisplay string `gorm:"-" json:"last_run_time" column_setting:"true"`
	NextRunDisplay string `gorm:"-" json:"next_run_time" column_setting:"true"`
}

// AfterFind GORM Hook: Format run times for display
func (td *TaskDefinition) AfterFind(tx *gorm.DB) (err error) {
	if td.LastRunTime != nil {
		td.LastRunDisplay = td.LastRunTime.Format("2006-01-02 15:04:05.000")
	} else {
		td.LastRunDisplay = "-"
	}

	if td.NextRunTime != nil {
		td.NextRunDisplay = td.NextRunTime.Format("2006-01-02 15:04:05.000")
	} else {
		td.NextRunDisplay = "-"
	}
	return nil
}

func (TaskDefinition) TableName() string {
	return "task_definition"
}
