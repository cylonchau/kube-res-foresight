package apis

import "time"

// ModuleListReq filters the module list.
type ModuleListReq struct {
	ListOptions
	Namespace string `form:"namespace" json:"namespace"`
	Mode      string `form:"mode" json:"mode"`
}

// ModuleCreateReq creates a module: a Kubernetes workload (namespace, kind and name) and the policy for applying
// recommendations to it. Zero values of the optional numbers mean "unset".
type ModuleCreateReq struct {
	Name         string `json:"name" binding:"required"`
	Description  string `json:"description"`
	Enabled      *bool  `json:"enabled"`
	Mode         string `json:"mode"`
	Namespace    string `json:"namespace" binding:"required"`
	WorkloadKind string `json:"workload_kind"`
	Workload     string `json:"workload" binding:"required"`
	// Optional values of the {{service}} and {{container}} PromQL variables of the module's metric sources.
	Service   string `json:"service"`
	Container string `json:"container"`

	// What the module reads: the history window, the forecast period and the data quality limits (zero takes the default).
	LookbackHours            int     `json:"lookback_hours"`
	HorizonMinutes           int     `json:"horizon_minutes"`
	MaxFillSteps             int     `json:"max_fill_steps"`
	StepSeconds              int     `json:"step_seconds"`
	MinPoints                int     `json:"min_points"`
	ScaleUpMaxStep           int     `json:"scale_up_max_step"`
	ScaleDownMaxStep         int     `json:"scale_down_max_step"`
	ScaleUpCooldownSeconds   int     `json:"scale_up_cooldown_seconds"`
	ScaleDownCooldownSeconds int     `json:"scale_down_cooldown_seconds"`
	LimitCPURatio            float64 `json:"limit_cpu_ratio"`
	LimitMemoryRatio         float64 `json:"limit_memory_ratio"`
	WindowSteps              int     `json:"window_steps"`
	RunIntervalMinutes       int     `json:"run_interval_minutes"`
	FallbackMinHistoryRatio  float64 `json:"fallback_min_history_ratio"`
	MaxMissingRatio          float64 `json:"max_missing_ratio"`

	TTLSeconds int     `json:"ttl_seconds"`
	MinScore   float64 `json:"min_score"`

	MinCPUMilli         int64 `json:"min_cpu_milli"`
	MaxCPUMilli         int64 `json:"max_cpu_milli"`
	MinMemoryBytes      int64 `json:"min_memory_bytes"`
	MaxMemoryBytes      int64 `json:"max_memory_bytes"`
	MinReplicas         int   `json:"min_replicas"`
	MaxReplicas         int   `json:"max_replicas"`
	FallbackCPUMilli    int64 `json:"fallback_cpu_milli"`
	FallbackMemoryBytes int64 `json:"fallback_memory_bytes"`
	FallbackReplicas    int   `json:"fallback_replicas"`
	// GPU bounds (whole cards) and the fallback.
	MinGPU      int `json:"min_gpu"`
	MaxGPU      int `json:"max_gpu"`
	FallbackGPU int `json:"fallback_gpu"`

	HeadroomRatio  float64 `json:"headroom_ratio"`
	PodCapacityRPS float64 `json:"pod_capacity_rps"`
	Timezone       string  `json:"timezone"`

	ReplicaMode           string  `json:"replica_mode"`
	CapacityMode          string  `json:"capacity_mode"`
	PodCPURequestMilli    int64   `json:"pod_cpu_request_milli"`
	PodMemoryRequestBytes int64   `json:"pod_memory_request_bytes"`
	TargetUtilization     float64 `json:"target_utilization"`

	// Replica ceiling: fixed, or dynamic (up to HardMaxReplicas, by ExpandStepRatio per cycle).
	MaxReplicasMode string  `json:"max_replicas_mode"`
	HardMaxReplicas int     `json:"hard_max_replicas"`
	ExpandStepRatio float64 `json:"expand_step_ratio"`

	// OOM protection and the JVM heap rule; zero takes the default except JVMHeapPressure, where an explicit 0
	// turns the rule off and absent takes 0.85.
	OOMLookbackDays int      `json:"oom_lookback_days"`
	OOMMemoryUplift float64  `json:"oom_memory_uplift"`
	JVMHeapPressure *float64 `json:"jvm_heap_pressure"`
	RecoverCycles   int      `json:"recover_cycles"`

	// Optional metric sources created together with the module.
	Sources []SourceReq `json:"sources"`
}

// ModuleUpdateReq patches a module; absent fields are unchanged. Bypass is changed only through the control API.
type ModuleUpdateReq struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Enabled      *bool   `json:"enabled"`
	Mode         *string `json:"mode"`
	Namespace    *string `json:"namespace"`
	WorkloadKind *string `json:"workload_kind"`
	Workload     *string `json:"workload"`
	Service      *string `json:"service"`
	Container    *string `json:"container"`

	LookbackHours            *int     `json:"lookback_hours"`
	HorizonMinutes           *int     `json:"horizon_minutes"`
	MaxFillSteps             *int     `json:"max_fill_steps"`
	StepSeconds              *int     `json:"step_seconds"`
	MinPoints                *int     `json:"min_points"`
	ScaleUpMaxStep           *int     `json:"scale_up_max_step"`
	ScaleDownMaxStep         *int     `json:"scale_down_max_step"`
	ScaleUpCooldownSeconds   *int     `json:"scale_up_cooldown_seconds"`
	ScaleDownCooldownSeconds *int     `json:"scale_down_cooldown_seconds"`
	LimitCPURatio            *float64 `json:"limit_cpu_ratio"`
	LimitMemoryRatio         *float64 `json:"limit_memory_ratio"`
	WindowSteps              *int     `json:"window_steps"`
	RunIntervalMinutes       *int     `json:"run_interval_minutes"`
	FallbackMinHistoryRatio  *float64 `json:"fallback_min_history_ratio"`
	MaxMissingRatio          *float64 `json:"max_missing_ratio"`

	TTLSeconds *int     `json:"ttl_seconds"`
	MinScore   *float64 `json:"min_score"`

	MinCPUMilli         *int64 `json:"min_cpu_milli"`
	MaxCPUMilli         *int64 `json:"max_cpu_milli"`
	MinMemoryBytes      *int64 `json:"min_memory_bytes"`
	MaxMemoryBytes      *int64 `json:"max_memory_bytes"`
	MinReplicas         *int   `json:"min_replicas"`
	MaxReplicas         *int   `json:"max_replicas"`
	FallbackCPUMilli    *int64 `json:"fallback_cpu_milli"`
	FallbackMemoryBytes *int64 `json:"fallback_memory_bytes"`
	FallbackReplicas    *int   `json:"fallback_replicas"`
	MinGPU              *int   `json:"min_gpu"`
	MaxGPU              *int   `json:"max_gpu"`
	FallbackGPU         *int   `json:"fallback_gpu"`

	HeadroomRatio  *float64 `json:"headroom_ratio"`
	PodCapacityRPS *float64 `json:"pod_capacity_rps"`
	Timezone       *string  `json:"timezone"`

	ReplicaMode           *string  `json:"replica_mode"`
	CapacityMode          *string  `json:"capacity_mode"`
	PodCPURequestMilli    *int64   `json:"pod_cpu_request_milli"`
	PodMemoryRequestBytes *int64   `json:"pod_memory_request_bytes"`
	TargetUtilization     *float64 `json:"target_utilization"`

	MaxReplicasMode *string  `json:"max_replicas_mode"`
	HardMaxReplicas *int     `json:"hard_max_replicas"`
	ExpandStepRatio *float64 `json:"expand_step_ratio"`

	OOMLookbackDays *int     `json:"oom_lookback_days"`
	OOMMemoryUplift *float64 `json:"oom_memory_uplift"`
	JVMHeapPressure *float64 `json:"jvm_heap_pressure"`
	RecoverCycles   *int     `json:"recover_cycles"`
}

// AlgorithmListReq filters the algorithm list (the catalog with each algorithm's platform-wide setting).
type AlgorithmListReq struct {
	ListOptions
	Category string `form:"category" json:"category"`
	Enabled  string `form:"enabled" json:"enabled"`
}

// AlgorithmUpdateReq changes the platform-wide setting of one algorithm; absent fields are unchanged. Params
// replaces the whole parameter set (a parameter left out takes its default).
type AlgorithmUpdateReq struct {
	// Formula, ParamSchema, MinSamples, Recursive and Category change a custom algorithm; a built-in one refuses them.
	Formula     *string         `json:"formula"`
	ParamSchema *[]ParamSpecReq `json:"param_schema"`
	MinSamples  *int            `json:"min_samples"`
	Recursive   *bool           `json:"recursive"`
	Category    *string         `json:"category"`

	Description *string        `json:"description"`
	Enabled     *bool          `json:"enabled"`
	Params      map[string]any `json:"params"`
	Weight      *float64       `json:"weight"`

	TrainBudgetSeconds    *int     `json:"train_budget_seconds"`
	PromoteMinPoints      *int     `json:"promote_min_points"`
	PromoteMinImprovement *float64 `json:"promote_min_improvement"`
	PromotePeakTolerance  *float64 `json:"promote_peak_tolerance"`
}

// SourceReq creates a metric source of a module. Kind and Resource are checked against each other; Resource
// defaults to the one the kind normally feeds.
type SourceReq struct {
	Name     string `json:"name" binding:"required"`
	Kind     string `json:"kind" binding:"required"`
	Resource string `json:"resource"`
	// MappingID is an existing metric mapping; Custom writes the PromQL here and creates the mapping with the source.
	MappingID   uint               `json:"mapping_id"`
	Custom      *SourceCustomQuery `json:"custom"`
	Priority    int                `json:"priority"`
	Weight      *float64           `json:"weight"`
	Stat        string             `json:"stat"`
	Basis       string             `json:"basis"`
	WindowHours int                `json:"window_hours"`
	Threshold   float64            `json:"threshold"`
	Enabled     *bool              `json:"enabled"`
	Remark      string             `json:"remark"`
	// How the values are shaped into the dataset (see model.MetricSource); zero values take the defaults.
	Scale       float64 `json:"scale"`
	Precision   *int    `json:"precision"`
	Aggregation string  `json:"aggregation"`
	Missing     string  `json:"missing"`
}

// SourceCustomQuery is a PromQL a user writes for a source. The mapping it creates is named after the source and
// measures the kind of the source (a custom metric keeps the unit given here).
type SourceCustomQuery struct {
	DatasourceID uint   `json:"datasource_id" binding:"required"`
	Query        string `json:"query" binding:"required"`
	Level        string `json:"level"`
	Unit         string `json:"unit"`
	StepSeconds  int    `json:"step_seconds"`
}

// SourceUpdateReq patches a source; its kind (and so its stored history) cannot change.
type SourceUpdateReq struct {
	Name      *string `json:"name"`
	Resource  *string `json:"resource"`
	MappingID *uint   `json:"mapping_id"`
	// Custom replaces the query of the source with a PromQL written here: a new mapping is made for it and the one the
	// source had is removed when it was written for this source alone. Give either mapping_id or custom.
	Custom      *SourceCustomQuery `json:"custom"`
	Priority    *int               `json:"priority"`
	Weight      *float64           `json:"weight"`
	Stat        *string            `json:"stat"`
	Basis       *string            `json:"basis"`
	WindowHours *int               `json:"window_hours"`
	Threshold   *float64           `json:"threshold"`
	Enabled     *bool              `json:"enabled"`
	Remark      *string            `json:"remark"`
	Scale       *float64           `json:"scale"`
	Precision   *int               `json:"precision"`
	Aggregation *string            `json:"aggregation"`
	Missing     *string            `json:"missing"`
}

// PeriodReq creates or replaces a fixed period (see decision.FixedPeriod). A weekly period uses Weekdays (a bit mask,
// bit n = time.Weekday(n)) with StartMinute and EndMinute; an absolute one uses AbsStart and AbsEnd.
type PeriodReq struct {
	Name        string     `json:"name" binding:"required"`
	Enabled     *bool      `json:"enabled"`
	Priority    int        `json:"priority"`
	Weekdays    int        `json:"weekdays"`
	StartMinute int        `json:"start_minute"`
	EndMinute   int        `json:"end_minute"`
	AbsStart    *time.Time `json:"abs_start"`
	AbsEnd      *time.Time `json:"abs_end"`
	CPUMilli    int64      `json:"cpu_milli"`
	MemoryBytes int64      `json:"memory_bytes"`
	Replicas    int        `json:"replicas"`
	Remark      string     `json:"remark"`
}

// BacktestReq asks for a rolling backtest of one metric source.
type BacktestReq struct {
	Metric  string `json:"metric" binding:"required"`
	Cutoffs int    `json:"cutoffs"`
}

// BypassReq makes a module keep every original value; the reason is mandatory and audited.
type BypassReq struct {
	Reason string `json:"reason"`
}

// AlgorithmCreateReq creates a custom algorithm: an id, a parameter schema and a formula over the history. The system
// compiles and dry-runs the formula before it is stored; it applies to every module once enabled, like any algorithm.
type AlgorithmCreateReq struct {
	ID          string `json:"id" binding:"required"`
	Category    string `json:"category"`
	Description string `json:"description"`
	// Formula makes a formula algorithm; Base instead derives a variant of a built-in algorithm (its parameter schema
	// is the base's, so none is sent). Exactly one of the two is given.
	Formula     string         `json:"formula"`
	Base        string         `json:"base"`
	ParamSchema []ParamSpecReq `json:"param_schema"`
	MinSamples  int            `json:"min_samples"`
	Recursive   bool           `json:"recursive"`
	Enabled     bool           `json:"enabled"`
	Params      map[string]any `json:"params"`
	Weight      *float64       `json:"weight"`
}

// ParamSpecReq is one parameter of a custom algorithm.
type ParamSpecReq struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     any      `json:"default"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
	Unit        string   `json:"unit"`
	Description string   `json:"description"`
}

// FormulaCheckReq asks the system to compile a formula and forecast a synthetic series with it, without storing it.
type FormulaCheckReq struct {
	Formula     string         `json:"formula"`
	ParamSchema []ParamSpecReq `json:"param_schema"`
	MinSamples  int            `json:"min_samples"`
	Recursive   bool           `json:"recursive"`
	Params      map[string]any `json:"params"`
	Horizon     int            `json:"horizon"`
}
