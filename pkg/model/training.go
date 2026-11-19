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
	ID        uint      `gorm:"primaryKey;index:idx_training_time,priority:2" json:"id" column_setting:"true"`
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

	PredictionTime time.Time `gorm:"not null;index:idx_training_time,priority:1" json:"prediction_time" column_setting:"true"`

	// InputHash is the hash of everything the run was given (the history and its end, the grid, the horizon, the parameters, the model version and the covariates):
	// a run that would be given the same is not made again, the forecast of this one stands for it (P11 B3).
	InputHash string `gorm:"type:varchar(40)" json:"input_hash" column_setting:"false"`
	// ReusedFrom is the time of the run whose forecast this record stands for: nothing was computed or stored for it, the rows of that run are its forecast.
	ReusedFrom *time.Time `json:"reused_from" column_setting:"false"`
}

func (TrainingRun) TableName() string { return "forecast_training_run" }
