package model

import "time"

func init() { RegisterModel(&ModuleRunState{}) }

// ModuleRunState is what the scheduled pipeline remembers about a module between cycles, so the fallback rules can
// tell "the forecast is late" and "the forecast keeps failing" from a one-off miss, and a recovery needs several
// good cycles in a row. It also remembers which algorithm instance is in use and how many cycles another has led.
type ModuleRunState struct {
	ModuleID  uint      `gorm:"primaryKey;autoIncrement:false" json:"module_id"`
	UpdatedAt time.Time `json:"updated_at"`

	LastRunAt           *time.Time `json:"last_run_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	ConsecutiveFailures int        `gorm:"not null;default:0" json:"consecutive_failures"`
	// HealthyCycles counts the successful cycles in a row since the last degraded recommendation.
	HealthyCycles int    `gorm:"not null;default:0" json:"healthy_cycles"`
	LastError     string `gorm:"type:varchar(512)" json:"last_error"`
	// DegradedFields is a JSON object of field -> reason code of the newest recommendation; empty when it was healthy.
	DegradedFields string `gorm:"type:text" json:"degraded_fields"`

	// Selected is the algorithm instance in use; Challenger the one that has scored higher for ChallengerCycles
	// evaluations in a row (see ForecastModule.SwitchCycles).
	Selected         string `gorm:"type:varchar(40)" json:"selected"`
	Challenger       string `gorm:"type:varchar(40)" json:"challenger"`
	ChallengerCycles int    `gorm:"not null;default:0" json:"challenger_cycles"`
	// EffectiveMaxReplicas is the replica ceiling under dynamic expansion; 0 = the module maximum.
	EffectiveMaxReplicas int `gorm:"not null;default:0" json:"effective_max_replicas"`
}

func (ModuleRunState) TableName() string { return "module_run_state" }
