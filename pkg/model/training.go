package model

import "time"

func init() {
	RegisterModel(&TrainingRun{})
}

// Training run statuses.
const (
	TrainingOK      = "ok"
	TrainingSkipped = "skipped"
	TrainingFailed  = "failed"
)

// TrainingRun is the record of one training-and-forecast run of an algorithm on one source of a module: what it was
// given (the window of history, the parameters, the formula of a custom algorithm), how long it took and how it ended.
// The platform keeps no model weights; the algorithms fit on the history at every run, so this record plus the
// history of the window is what makes a forecast reproducible: the same implementation version, parameters and window
// give the same values (the model version in the record is the hash of exactly these).
type TrainingRun struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID    uint   `gorm:"not null;index:idx_training_lookup,priority:1" json:"module_id" column_setting:"true"`
	Metric      string `gorm:"type:varchar(20);not null;index:idx_training_lookup,priority:2" json:"metric" column_setting:"true"`
	AlgorithmID string `gorm:"type:varchar(40);not null;index:idx_training_lookup,priority:3" json:"algorithm_id" column_setting:"true"`

	// ImplVersion is the version of the implementation (for a custom algorithm, a hash of its formula and parameter
	// schema); ModelVersion the hash of implementation, parameters and sources that stamps the predictions.
	ImplVersion  string `gorm:"type:varchar(40);not null" json:"impl_version" column_setting:"false"`
	ModelVersion string `gorm:"type:varchar(64);not null" json:"model_version" column_setting:"true"`
	Params       string `gorm:"type:text" json:"params" column_setting:"false"`
	// Formula and ParamSchema are the definition of a custom algorithm at the time of the run, so a later edit does
	// not make the record unreproducible.
	Formula     string `gorm:"type:text" json:"formula" column_setting:"false"`
	ParamSchema string `gorm:"type:text" json:"param_schema" column_setting:"false"`

	Status string `gorm:"type:varchar(10);not null;index" json:"status" column_setting:"true"`
	Reason string `gorm:"type:varchar(500)" json:"reason" column_setting:"false"`

	// The window of history the run was given, its grid and how far it forecast.
	Samples      int        `gorm:"not null;default:0" json:"samples" column_setting:"true"`
	StepSeconds  int        `gorm:"not null;default:0" json:"step_seconds" column_setting:"false"`
	HorizonSteps int        `gorm:"not null;default:0" json:"horizon_steps" column_setting:"false"`
	Points       int        `gorm:"not null;default:0" json:"points" column_setting:"false"`
	WindowStart  *time.Time `json:"window_start" column_setting:"false"`
	WindowEnd    *time.Time `json:"window_end" column_setting:"false"`
	DurationMs   int64      `gorm:"not null;default:0" json:"duration_ms" column_setting:"true"`
	// Covariates are the other sources the run read, Dropped the ones it could not get (JSON object with the reason).
	Covariates string `gorm:"type:varchar(255)" json:"covariates" column_setting:"false"`
	Dropped    string `gorm:"type:text" json:"dropped_covariates" column_setting:"false"`

	PredictionTime time.Time `gorm:"not null" json:"prediction_time" column_setting:"true"`
}

func (TrainingRun) TableName() string { return "forecast_training_run" }
