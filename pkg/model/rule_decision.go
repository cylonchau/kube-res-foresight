package model

import "time"

func init() { RegisterModel(&RuleDecision{}) }

// RuleDecision is one calculation of the fixed formula of the resource analysis for one module (the second source of a decision, next to the forecasts of the algorithms): from the
// P90, P95 and P99 of the real use over the period of the recommendation, the replicas the module needs and the CPU and memory a Pod should request, with what the module has now.
// It forecasts nothing. Whether it is followed is the module's choice (DecisionSource) and how far it goes is the module's mode: this row only says what the formula gave, and the
// recommendation that took it (Recommendation.RuleDecisionID) and the actions of the operator on that recommendation (ScalingExecution.DecisionVersion) say what was done with it.
type RuleDecision struct {
	ID        uint      `gorm:"primaryKey;index:idx_rule_decision_module,priority:2" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
	// CheckedAt is when the calculation was last made with the same result: a result that does not change is not stored again, it is checked again.
	CheckedAt time.Time `gorm:"index" json:"checked_at"`

	ModuleID uint `gorm:"not null;index:idx_rule_decision_module,priority:1" json:"module_id"`
	// Period is the label of the period the percentiles were read over (the recommendation period, 7d for example); Caliber is "" when the three percentiles together decide, or P90, P95, P99.
	Period  string `gorm:"type:varchar(12);not null;default:''" json:"period"`
	Caliber string `gorm:"type:varchar(4);not null;default:''" json:"caliber"`
	// Mode is the mode of the module when the calculation was made.
	Mode string `gorm:"type:varchar(16);not null;default:''" json:"mode"`

	// the replicas: what the workload has, what the formula needs, the difference and what decides it
	ReplicasSet         bool   `gorm:"not null;default:false" json:"replicas_set"`
	CurrentReplicas     int    `gorm:"not null;default:0" json:"current_replicas"`
	RecommendedReplicas int    `gorm:"not null;default:0" json:"recommended_replicas"`
	ReplicasDelta       int    `gorm:"not null;default:0" json:"replicas_delta"`
	Direction           string `gorm:"type:varchar(8);not null;default:''" json:"direction"`
	Binding             string `gorm:"type:varchar(8);not null;default:''" json:"binding"`
	ThisRound           int    `gorm:"not null;default:0" json:"this_round"`
	ReplicasFormula     string `gorm:"type:varchar(600);not null;default:''" json:"replicas_formula"`

	// the Pod spec (Requests): CPU in millicores, memory in bytes; the Limits the formula recommends beside them
	CPUSet                 bool  `gorm:"not null;default:false" json:"cpu_set"`
	CPUCurrentMilli        int64 `gorm:"not null;default:0" json:"cpu_current_milli"`
	CPURecommendedMilli    int64 `gorm:"not null;default:0" json:"cpu_recommended_milli"`
	CPULimitMilli          int64 `gorm:"not null;default:0" json:"cpu_limit_milli"`
	MemorySet              bool  `gorm:"not null;default:false" json:"memory_set"`
	MemoryCurrentBytes     int64 `gorm:"not null;default:0" json:"memory_current_bytes"`
	MemoryRecommendedBytes int64 `gorm:"not null;default:0" json:"memory_recommended_bytes"`
	MemoryLimitBytes       int64 `gorm:"not null;default:0" json:"memory_limit_bytes"`

	// the use of one Pod over the period, in cores and bytes: what the formula was made of
	CPUP90    float64 `gorm:"column:cpu_p90;not null;default:0" json:"cpu_p90"`
	CPUP95    float64 `gorm:"column:cpu_p95;not null;default:0" json:"cpu_p95"`
	CPUP99    float64 `gorm:"column:cpu_p99;not null;default:0" json:"cpu_p99"`
	MemoryP90 float64 `gorm:"column:memory_p90;not null;default:0" json:"memory_p90"`
	MemoryP95 float64 `gorm:"column:memory_p95;not null;default:0" json:"memory_p95"`
	MemoryP99 float64 `gorm:"column:memory_p99;not null;default:0" json:"memory_p99"`

	CPUStatus    string `gorm:"type:varchar(12);not null;default:''" json:"cpu_status"`
	MemoryStatus string `gorm:"type:varchar(12);not null;default:''" json:"memory_status"`
	Confidence   string `gorm:"type:varchar(8);not null;default:''" json:"confidence"`
	// Certain says the formula can be followed: the confidence is not low, no verdict is unknown and every figure of the module was read. A calculation that is not certain is shown
	// and is not used for a decision.
	Certain bool   `gorm:"not null;default:false" json:"certain"`
	Caution string `gorm:"type:varchar(120);not null;default:''" json:"caution"`
	Partial bool   `gorm:"not null;default:false" json:"partial"`
	Reasons string `gorm:"type:varchar(400);not null;default:''" json:"reasons"`
	// Formula is the CPU and memory formulas written out with their numbers.
	Formula string `gorm:"type:varchar(1200);not null;default:''" json:"formula"`
}

func (RuleDecision) TableName() string { return "rule_decision" }
