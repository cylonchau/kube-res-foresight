package model

import "time"

func init() {
	RegisterModel(&OverprovisionAnalysis{})
}

// OverprovisionAnalysis is the stored result of the over-provisioning analysis of one resource of one module over one window (P10 A2): what a Pod
// requested, what it used, what the use supports, how sure that is and why. A run of the analysis adds one row per window and resource; the rows are
// the history that can be viewed, compared and traced, and the dashboard reads the newest of each. CPU is in cores and memory in bytes; Pod figures
// are per Pod, Total figures are over all the Pods. It holds no forecast.
type OverprovisionAnalysis struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index:idx_op_created" json:"created_at"`

	ModuleID      uint   `gorm:"not null;index:idx_op_latest,priority:1" json:"module_id"`
	Resource      string `gorm:"type:varchar(8);not null;index:idx_op_latest,priority:2" json:"resource"`
	WindowMinutes int    `gorm:"not null;index:idx_op_latest,priority:3" json:"window_minutes"`

	Replicas  int     `gorm:"not null;default:0" json:"replicas"`
	Request   float64 `gorm:"not null;default:0" json:"request"`
	Limit     float64 `gorm:"column:limit_value;not null;default:0" json:"limit"`
	LimitFrom string  `gorm:"type:varchar(8);not null;default:''" json:"limit_from"`

	Points         int     `gorm:"not null;default:0" json:"points"`
	ExpectedPoints int     `gorm:"not null;default:0" json:"expected_points"`
	Coverage       float64 `gorm:"not null;default:0" json:"coverage"`
	P50            float64 `gorm:"not null;default:0" json:"p50"`
	P90            float64 `gorm:"column:p90;not null;default:0" json:"p90"`
	P95            float64 `gorm:"not null;default:0" json:"p95"`
	P99            float64 `gorm:"not null;default:0" json:"p99"`
	Max            float64 `gorm:"column:max_value;not null;default:0" json:"max"`
	TotalP50       float64 `gorm:"not null;default:0" json:"total_p50"`
	TotalP90       float64 `gorm:"column:total_p90;not null;default:0" json:"total_p90"`
	TotalP95       float64 `gorm:"not null;default:0" json:"total_p95"`
	TotalP99       float64 `gorm:"not null;default:0" json:"total_p99"`
	TotalMax       float64 `gorm:"column:total_max_value;not null;default:0" json:"total_max"`

	Suggested   float64 `gorm:"not null;default:0" json:"suggested"`
	Ratio       float64 `gorm:"not null;default:0" json:"ratio"`
	Numerator   float64 `gorm:"not null;default:0" json:"numerator"`
	Denominator float64 `gorm:"not null;default:0" json:"denominator"`
	Reclaimable float64 `gorm:"not null;default:0" json:"reclaimable"`

	Status     string `gorm:"type:varchar(12);not null;default:'unknown'" json:"status"`
	Grade      string `gorm:"type:varchar(12);not null;default:''" json:"grade"`
	Confidence string `gorm:"type:varchar(8);not null;default:'low'" json:"confidence"`
	Risk       string `gorm:"type:varchar(8);not null;default:'low'" json:"risk"`

	Throttle      float64 `gorm:"not null;default:0" json:"throttle"`
	OOMKills      float64 `gorm:"column:oom_kills;not null;default:0" json:"oom_kills"`
	OOMWorkingSet float64 `gorm:"column:oom_working_set;not null;default:0" json:"oom_working_set"`

	// Reasons are the codes of why the result is what it is, separated by commas; Formula is the suggestion written out with its numbers.
	Reasons        string `gorm:"type:varchar(400);not null;default:''" json:"-"`
	Formula        string `gorm:"type:varchar(600);not null;default:''" json:"formula"`
	FormulaVersion string `gorm:"type:varchar(12);not null;default:''" json:"formula_version"`
}

func (OverprovisionAnalysis) TableName() string { return "overprovision_analysis" }
