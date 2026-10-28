package model

import "time"

func init() { RegisterModel(&ScaleOutcome{}) }

// ScaleOutcome is the record of one scale-up the platform decided ahead of the load: how far ahead it looked, what the algorithms
// predicted, and afterwards what the load needed, when the replicas were Ready and whether that was before the load needed them. It
// is what the next lead is learned from: the measured ready times and the lateness of the scale-ups that came too late.
type ScaleOutcome struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID uint `gorm:"not null;index:idx_scale_outcome_module,priority:1" json:"module_id" column_setting:"true"`
	// Version is the recommendation that asked for the replicas; DecidedAt when it was made.
	Version   int       `gorm:"not null" json:"version" column_setting:"true"`
	DecidedAt time.Time `gorm:"not null;index:idx_scale_outcome_module,priority:2" json:"decided_at" column_setting:"true"`
	// FromReplicas were in force, ToReplicas were asked for; Emergency says the live load asked for them, not the forecast.
	FromReplicas int  `gorm:"not null" json:"from_replicas" column_setting:"true"`
	ToReplicas   int  `gorm:"not null" json:"to_replicas" column_setting:"true"`
	Emergency    bool `gorm:"not null;default:false" json:"emergency" column_setting:"true"`
	// LeadSeconds is how far ahead the decision looked and LeadSource where the Ready time in it came from (measured or configured).
	LeadSeconds float64 `gorm:"not null;default:0" json:"lead_seconds" column_setting:"true"`
	LeadSource  string  `gorm:"type:varchar(16)" json:"lead_source" column_setting:"false"`
	// Algorithm is the algorithm the replicas are credited to; Votes is the JSON of every vote (algorithm, replicas, credibility).
	Algorithm string `gorm:"type:varchar(40)" json:"algorithm" column_setting:"true"`
	Votes     string `gorm:"type:text" json:"votes" column_setting:"false"`

	// Evaluation, filled in once the time has passed. Verdict is pending, timely, late or unneeded.
	Verdict      string     `gorm:"type:varchar(12);not null;default:'pending';index" json:"verdict" column_setting:"true"`
	Final        bool       `gorm:"not null;default:false;index" json:"final" column_setting:"false"`
	EvaluatedAt  *time.Time `json:"evaluated_at" column_setting:"false"`
	ActualNeed   int        `gorm:"not null;default:0" json:"actual_need" column_setting:"true"`
	NeededAt     *time.Time `json:"needed_at" column_setting:"true"`
	ReadyAt      *time.Time `json:"ready_at" column_setting:"true"`
	ReadySeconds float64    `gorm:"not null;default:0" json:"ready_seconds" column_setting:"true"`
	LateSeconds  float64    `gorm:"not null;default:0" json:"late_seconds" column_setting:"true"`
	SlackSeconds float64    `gorm:"not null;default:0" json:"slack_seconds" column_setting:"true"`
	Note         string     `gorm:"type:varchar(300)" json:"note" column_setting:"false"`
}

func (ScaleOutcome) TableName() string { return "scale_outcome" }
