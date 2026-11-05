package model

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	RegisterModel(&ForecastModule{})
	RegisterModel(&ModulePeriod{})
	RegisterModel(&MetricSeries{})
	RegisterModel(&MetricSample{})
	RegisterModel(&Prediction{})
	RegisterModel(&BacktestRun{})
	RegisterModel(&Recommendation{})
}

// Module modes. Each mode adds one capability on top of the previous one; Observe never writes anywhere.
const (
	ModeObserve   = "observe"
	ModeRecommend = "recommend"
	ModeAdmission = "admission"
	ModeAuto      = "auto"
)

// Prediction modes of a module (ForecastModule.PredictMode). They apply only while PredictEnabled is true.
const (
	// PredictNone does not forecast: the module runs on its fixed default configuration (the fallback values, else what the workload
	// declares).
	PredictNone = "none"
	// PredictFixed forecasts with the one algorithm the module names (PredictAlgorithm); no ranking, no switching.
	PredictFixed = "fixed"
	// PredictDynamic forecasts with every enabled algorithm and follows them by their scores (the ranking of the algorithm center).
	PredictDynamic = "dynamic"
)

// PredictModes lists the prediction modes.
var PredictModes = []string{PredictNone, PredictFixed, PredictDynamic}

// AlgorithmDecides says whether the algorithm may decide the recommendation of the module: every one may in dynamic mode (by score),
// only the named one in fixed mode. The others still forecast, are scored and drawn; they are never followed.
func (m *ForecastModule) AlgorithmDecides(key string) bool {
	return m.PredictMode != PredictFixed || key == m.PredictAlgorithm
}

// FixedGraceFactor is how many times the TTL the forecast of the algorithm a module is fixed to may be old before it counts as stale:
// the administrator named it, so one failed cycle or a short gap in the data does not send the module back to the default.
const FixedGraceFactor = 3

// DefaultTimezone is the zone the weekly fixed periods of a module are read in when it names none: UTC+8, the zone of the platform.
const DefaultTimezone = "Asia/Shanghai"

// DefaultOOMWeight is the share of the memory score one OOM kill takes away when the OOM weight is switched on and sets no value of its
// own.
const DefaultOOMWeight = 0.2

// MaxDatasetStepSeconds is the coarsest sampling step of a dataset (one day).
const MaxDatasetStepSeconds = 86400

// Forecast metrics a module can predict, matching MetricMapping.Metric.
const (
	MetricCPU    = "cpu"
	MetricMemory = "memory"
	MetricRPS    = "rps"
	// MetricReplicas is collected for capacity estimation only; it is never forecast.
	MetricReplicas = "replicas"
)

// ForecastMetrics lists the metrics that drive a recommendation.
var ForecastMetrics = []string{MetricCPU, MetricMemory, MetricRPS}

// CollectedMetrics are the metrics a module collects history for: the forecast ones plus the ready replica
// count used to estimate one pod's capacity.
var CollectedMetrics = []string{MetricCPU, MetricMemory, MetricRPS, MetricReplicas}

// Replica modes. Fixed keeps the original formula (peak x headroom / fixed capacity); dynamic solves the
// replica count against throughput, CPU and memory with the measured uncertainty of each algorithm.
const (
	ReplicaModeFixed   = "fixed"
	ReplicaModeDynamic = "dynamic"
)

// Replica ceiling modes.
const (
	MaxReplicasFixed   = "fixed"
	MaxReplicasDynamic = "dynamic"
)

// Capacity modes: where the throughput of one pod comes from.
const (
	CapacityModeManual   = "manual"
	CapacityModeEstimate = "estimate"
)

// Workload kinds a module can manage. A DaemonSet has no replica count: only its requests are adjusted.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
)

// WorkloadKinds lists the supported kinds.
var WorkloadKinds = []string{KindDeployment, KindStatefulSet, KindDaemonSet}

// Defaults of the algorithm ranking that are not worth a setting of their own: how many recent windows an algorithm
// instance is scored on, how many points a challenger must lead by and for how many evaluations in a row before it
// replaces the instance in use.
const (
	DefaultScoreWindows = 6
	DefaultSwitchMargin = 2.0
	DefaultSwitchCycles = 2
)

// ForecastModule is a Kubernetes workload that kube-res-foresight adjusts: a Deployment, StatefulSet or DaemonSet named by
// its namespace and name. It owns the metrics to read (its metric sources, with their thresholds), the history window
// and the forecast period, and the policy for applying recommendations (bounds, fallback, headroom, mode). Every
// enabled algorithm (see AlgorithmConfig) is applied to it automatically. Rows are hard-deleted so the unique name
// can be reused. Zero values of the optional numbers mean "unset".
type ForecastModule struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name        string `gorm:"type:varchar(100);uniqueIndex;not null" json:"name" column_setting:"true"`
	Description string `gorm:"type:varchar(255)" json:"description" column_setting:"true"`
	Enabled     bool   `gorm:"not null" json:"enabled" column_setting:"true"`
	Mode        string `gorm:"type:varchar(16);not null;default:'observe'" json:"mode" column_setting:"true"`

	// The workload: namespace, kind and name. Nothing else identifies it (a container or a Service plays no part).
	Namespace    string `gorm:"type:varchar(253);not null;uniqueIndex:idx_module_workload,priority:1" json:"namespace" column_setting:"true"`
	WorkloadKind string `gorm:"type:varchar(16);not null;default:'Deployment';uniqueIndex:idx_module_workload,priority:2" json:"workload_kind" column_setting:"true"`
	Workload     string `gorm:"type:varchar(253);not null;uniqueIndex:idx_module_workload,priority:3" json:"workload" column_setting:"true"`

	// Service and Container are optional values for the {{service}} and {{container}} variables of the PromQL templates
	// of the metric sources (a request-rate mapping reads the Service, a per-container mapping the container). They
	// play no part in what the module is, and requests and limits are adjusted for every container of the workload.
	Service   string `gorm:"type:varchar(253)" json:"service" column_setting:"false"`
	Container string `gorm:"type:varchar(253)" json:"container" column_setting:"false"`

	// LookbackHours is the history collected, kept and trained on; HorizonMinutes the forecast period (how far
	// ahead every algorithm forecasts); MaxFillSteps the longest gap that may be interpolated.
	// StepSeconds is the sampling granularity of the dataset: every source is collected and aggregated onto a grid of
	// this step (5m, 15m, 1h, 1d ...), so all algorithms see the same time points. 0 keeps the step of each source's
	// own query. MinPoints is the fewest points the dataset of a source must have before it is forecast at all (on
	// top of what each algorithm needs); RunIntervalMinutes how often the module is collected and forecast by the
	// scheduler (0 = every cycle of the task).
	StepSeconds int `gorm:"not null;default:0" json:"step_seconds" column_setting:"false"`
	// WindowSteps is how many of the newest steps an algorithm trains on (the window length); 0 uses the whole history
	// range. The history range keeps more than the window, so a window can be changed without collecting again.
	WindowSteps        int `gorm:"not null;default:0" json:"window_steps" column_setting:"false"`
	MinPoints          int `gorm:"not null;default:0" json:"min_points" column_setting:"false"`
	RunIntervalMinutes int `gorm:"not null;default:0" json:"run_interval_minutes" column_setting:"false"`

	LookbackHours  int `gorm:"not null;default:720" json:"lookback_hours" column_setting:"false"`
	HorizonMinutes int `gorm:"not null;default:15" json:"horizon_minutes" column_setting:"false"`
	MaxFillSteps   int `gorm:"not null;default:3" json:"max_fill_steps" column_setting:"false"`
	// Data quality: a source whose history covers less than FallbackMinHistoryRatio of what was observed as one
	// contiguous run, or that misses more than MaxMissingRatio of its steps, is not forecast.
	FallbackMinHistoryRatio float64 `gorm:"not null;default:0.8" json:"fallback_min_history_ratio" column_setting:"false"`
	MaxMissingRatio         float64 `gorm:"not null;default:0.3" json:"max_missing_ratio" column_setting:"false"`

	// PredictEnabled turns the forecasting of the module on. It is off by default: a new module only collects its history and runs on its
	// fixed default configuration until someone switches the prediction on. PredictMode says how it then predicts (none, fixed, dynamic) and
	// PredictAlgorithm names the algorithm of the fixed mode.
	PredictEnabled   bool   `gorm:"not null;default:false" json:"predict_enabled" column_setting:"true"`
	PredictMode      string `gorm:"type:varchar(10);not null;default:'dynamic'" json:"predict_mode" column_setting:"true"`
	PredictAlgorithm string `gorm:"type:varchar(40)" json:"predict_algorithm" column_setting:"false"`

	// OOM protection, apart from every other resource setting. It concerns memory only: when the workload was OOM-killed within
	// OOMLookbackDays the memory recommendation is never below the working set before the kill (or the limit) times OOMMemoryUplift, and
	// the kills count against the memory score of an algorithm that under-provisioned memory (never against the CPU score) when
	// OOMWeightEnabled is on, by OOMWeight of the memory score per kill. Off, the kills do not enter any score.
	OOMLookbackDays  int     `gorm:"not null;default:14" json:"oom_lookback_days" column_setting:"false"`
	OOMMemoryUplift  float64 `gorm:"not null;default:1.2" json:"oom_memory_uplift" column_setting:"false"`
	OOMWeightEnabled bool    `gorm:"not null;default:false" json:"oom_weight_enabled" column_setting:"false"`
	OOMWeight        float64 `gorm:"not null;default:0.2" json:"oom_weight" column_setting:"false"`
	// JVMHeapPressure is the heap used / heap max above which the replica recommendation is raised; 0 disables.
	JVMHeapPressure float64 `gorm:"not null;default:0.85" json:"jvm_heap_pressure" column_setting:"false"`
	// RecoverCycles is how many successful cycles in a row end a degradation.
	RecoverCycles int `gorm:"not null;default:2" json:"recover_cycles" column_setting:"false"`

	// TTLSeconds is how long a recommendation, and the forecast it rests on, stays valid.
	TTLSeconds int `gorm:"not null;default:900" json:"ttl_seconds" column_setting:"false"`

	Bypass       bool   `gorm:"not null" json:"bypass" column_setting:"true"`
	BypassReason string `gorm:"type:varchar(255)" json:"bypass_reason" column_setting:"false"`

	// MinScore is the lowest score (0-100) an algorithm instance may have for its forecast to be used; below it the
	// fallback values apply. 0 accepts any score.
	MinScore float64 `gorm:"not null;default:0" json:"min_score" column_setting:"false"`

	// Replica ceiling. MaxReplicas is the normal upper limit. In dynamic mode a forecast above it may raise the
	// effective ceiling when the algorithm in use scored at least MinScore, by at most ExpandStepRatio of the
	// current ceiling per cycle, and never beyond HardMaxReplicas, which nothing may exceed. MinReplicas is a hard floor.
	MaxReplicasMode string  `gorm:"type:varchar(10);not null;default:'fixed'" json:"max_replicas_mode" column_setting:"false"`
	HardMaxReplicas int     `gorm:"not null;default:0" json:"hard_max_replicas" column_setting:"false"`
	ExpandStepRatio float64 `gorm:"not null;default:0" json:"expand_step_ratio" column_setting:"false"`

	// Hard bounds and the module fallback; 0 = unset.
	MinCPUMilli    int64 `gorm:"not null;default:0" json:"min_cpu_milli" column_setting:"false"`
	MaxCPUMilli    int64 `gorm:"not null;default:0" json:"max_cpu_milli" column_setting:"false"`
	MinMemoryBytes int64 `gorm:"not null;default:0" json:"min_memory_bytes" column_setting:"false"`
	MaxMemoryBytes int64 `gorm:"not null;default:0" json:"max_memory_bytes" column_setting:"false"`
	MinReplicas    int   `gorm:"not null;default:0" json:"min_replicas" column_setting:"false"`
	MaxReplicas    int   `gorm:"not null;default:0" json:"max_replicas" column_setting:"false"`

	// How fast the operator moves the replicas of the workload, per direction: the most replicas one action adds or
	// removes and the time that must pass since the last action in that direction. 0 = no limit.
	ScaleUpMaxStep           int `gorm:"not null;default:0" json:"scale_up_max_step" column_setting:"false"`
	ScaleDownMaxStep         int `gorm:"not null;default:0" json:"scale_down_max_step" column_setting:"false"`
	ScaleUpCooldownSeconds   int `gorm:"not null;default:0" json:"scale_up_cooldown_seconds" column_setting:"false"`
	ScaleDownCooldownSeconds int `gorm:"not null;default:0" json:"scale_down_cooldown_seconds" column_setting:"false"`

	// LimitCPURatio and LimitMemoryRatio make admission set the limits of a new Pod too: limit = recommended request x
	// ratio (at least 1). 0 leaves the limit as the workload declares it. Memory is the one to watch: a ratio of 1
	// makes the request the OOM line.
	LimitCPURatio    float64 `gorm:"not null;default:0" json:"limit_cpu_ratio" column_setting:"false"`
	LimitMemoryRatio float64 `gorm:"not null;default:0" json:"limit_memory_ratio" column_setting:"false"`

	FallbackCPUMilli    int64 `gorm:"not null;default:0" json:"fallback_cpu_milli" column_setting:"false"`
	FallbackMemoryBytes int64 `gorm:"not null;default:0" json:"fallback_memory_bytes" column_setting:"false"`
	FallbackReplicas    int   `gorm:"not null;default:0" json:"fallback_replicas" column_setting:"false"`
	MinGPU              int   `gorm:"not null;default:0" json:"min_gpu" column_setting:"false"`
	MaxGPU              int   `gorm:"not null;default:0" json:"max_gpu" column_setting:"false"`
	FallbackGPU         int   `gorm:"not null;default:0" json:"fallback_gpu" column_setting:"false"`

	// HeadroomRatio >= 1 multiplies the predicted peak; PodCapacityRPS is the verified safe load of one pod.
	HeadroomRatio  float64 `gorm:"not null;default:1" json:"headroom_ratio" column_setting:"false"`
	PodCapacityRPS float64 `gorm:"not null;default:0" json:"pod_capacity_rps" column_setting:"false"`

	// Dynamic replica solving. ReplicaMode "" or "fixed" keeps the original formula and ignores the fields below.
	ReplicaMode           string  `gorm:"type:varchar(16);not null;default:'fixed'" json:"replica_mode" column_setting:"false"`
	CapacityMode          string  `gorm:"type:varchar(16);not null;default:'manual'" json:"capacity_mode" column_setting:"false"`
	PodCPURequestMilli    int64   `gorm:"not null;default:0" json:"pod_cpu_request_milli" column_setting:"false"`
	PodMemoryRequestBytes int64   `gorm:"not null;default:0" json:"pod_memory_request_bytes" column_setting:"false"`
	TargetUtilization     float64 `gorm:"not null;default:0" json:"target_utilization" column_setting:"false"`

	Timezone string `gorm:"type:varchar(64);not null;default:'Asia/Shanghai'" json:"timezone" column_setting:"false"` // IANA zone of weekly fixed periods; UTC+8 unless set

	// Sources are the metric sources of the module (loaded with it, not a column).
	Sources []*MetricSource `gorm:"-" json:"sources"`
}

func (ForecastModule) TableName() string { return "forecast_module" }

// Predicts reports whether the module forecasts at all: the prediction is switched on and its mode is not "none".
func (m *ForecastModule) Predicts() bool {
	return m.PredictEnabled && m.PredictMode != PredictNone
}

// OOMScoreWeight is the share of the memory score one OOM kill takes away: 0 unless the OOM weight is switched on.
func (m *ForecastModule) OOMScoreWeight() float64 {
	if !m.OOMWeightEnabled {
		return 0
	}
	if m.OOMWeight > 0 {
		return m.OOMWeight
	}
	return DefaultOOMWeight
}

// Data is the module as the owner of its metrics and history, for the steps that need no algorithm.
func (m *ForecastModule) Data() *AlgorithmRun { return NewRun(m, &AlgorithmConfig{}) }

// HasReplicas reports whether the workload kind has a replica count the platform may set.
func (m *ForecastModule) HasReplicas() bool { return m.WorkloadKind != KindDaemonSet }

// Validate checks the module's own consistency and fills defaults.
func (m *ForecastModule) Validate() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch m.Mode {
	case "":
		m.Mode = ModeObserve
	case ModeObserve, ModeRecommend, ModeAdmission, ModeAuto:
	default:
		return fmt.Errorf("unsupported mode %q", m.Mode)
	}
	m.Namespace, m.Workload = strings.TrimSpace(m.Namespace), strings.TrimSpace(m.Workload)
	m.Service, m.Container = strings.TrimSpace(m.Service), strings.TrimSpace(m.Container)
	if m.Namespace == "" || m.Workload == "" {
		return fmt.Errorf("namespace and workload name are required")
	}
	if m.WorkloadKind == "" {
		m.WorkloadKind = KindDeployment
	}
	known := false
	for _, k := range WorkloadKinds {
		known = known || k == m.WorkloadKind
	}
	if !known {
		return fmt.Errorf("workload_kind must be one of %v", WorkloadKinds)
	}
	if m.LookbackHours == 0 {
		m.LookbackHours = 720
	}
	if m.LookbackHours < 1 || m.LookbackHours > 24*180 {
		return fmt.Errorf("lookback_hours must be between 1 and %d", 24*180)
	}
	if m.HorizonMinutes == 0 {
		m.HorizonMinutes = 15
	}
	if m.HorizonMinutes < 1 || m.HorizonMinutes > 24*60 {
		return fmt.Errorf("horizon_minutes must be between 1 and 1440")
	}
	if m.StepSeconds != 0 && (m.StepSeconds < MinStepSeconds || m.StepSeconds > MaxDatasetStepSeconds) {
		return fmt.Errorf("step_seconds must be 0 (the step of each source) or between %d and %d", MinStepSeconds, MaxDatasetStepSeconds)
	}
	for name, ratio := range map[string]float64{"limit_cpu_ratio": m.LimitCPURatio, "limit_memory_ratio": m.LimitMemoryRatio} {
		if ratio != 0 && (ratio < 1 || ratio > 10) {
			return fmt.Errorf("%s must be 0 (leave the limit alone) or between 1 and 10", name)
		}
	}
	for name, v := range map[string]int{"scale_up_max_step": m.ScaleUpMaxStep, "scale_down_max_step": m.ScaleDownMaxStep} {
		if v < 0 || v > 1000 {
			return fmt.Errorf("%s must be between 0 and 1000", name)
		}
	}
	for name, v := range map[string]int{"scale_up_cooldown_seconds": m.ScaleUpCooldownSeconds, "scale_down_cooldown_seconds": m.ScaleDownCooldownSeconds} {
		if v < 0 || v > 86400 {
			return fmt.Errorf("%s must be between 0 and 86400", name)
		}
	}
	if m.MinPoints < 0 || m.MinPoints > 100000 {
		return fmt.Errorf("min_points must be between 0 and 100000")
	}
	if m.WindowSteps < 0 || m.WindowSteps > 1000000 {
		return fmt.Errorf("window_steps must be between 0 and 1000000")
	}
	if m.WindowSteps > 0 && m.WindowSteps < m.MinPoints {
		return fmt.Errorf("window_steps (%d) is shorter than min_points (%d): the window could never hold the minimum data", m.WindowSteps, m.MinPoints)
	}
	if m.RunIntervalMinutes < 0 || m.RunIntervalMinutes > 30*24*60 {
		return fmt.Errorf("run_interval_minutes must be between 0 and %d", 30*24*60)
	}
	if m.StepSeconds > 0 {
		step := time.Duration(m.StepSeconds) * time.Second
		if time.Duration(m.LookbackHours)*time.Hour < time.Duration(max(m.MinPoints, 1))*step {
			return fmt.Errorf("lookback_hours (%d) is shorter than min_points x step (%d x %ds): the history range cannot hold the minimum data", m.LookbackHours, max(m.MinPoints, 1), m.StepSeconds)
		}
		if time.Duration(m.HorizonMinutes)*time.Minute < step {
			return fmt.Errorf("horizon_minutes (%d) is shorter than one step (%ds): the forecast would be empty", m.HorizonMinutes, m.StepSeconds)
		}
		if m.RunIntervalMinutes > 0 && time.Duration(m.RunIntervalMinutes)*time.Minute < step {
			return fmt.Errorf("run_interval_minutes (%d) is shorter than one step (%ds): there would be no new data between two runs", m.RunIntervalMinutes, m.StepSeconds)
		}
	}
	// the limit is in steps, so the time it covers follows the step: 10000 steps are about 3.5 days at 30 seconds
	if m.MaxFillSteps < 0 || m.MaxFillSteps > 10000 {
		return fmt.Errorf("max_fill_steps must be between 0 and 10000")
	}
	if m.FallbackMinHistoryRatio == 0 {
		m.FallbackMinHistoryRatio = 0.8
	}
	if m.FallbackMinHistoryRatio < 0.05 || m.FallbackMinHistoryRatio > 1 {
		return fmt.Errorf("fallback_min_history_ratio must be between 0.05 and 1")
	}
	if m.MaxMissingRatio == 0 {
		m.MaxMissingRatio = 0.3
	}
	if m.MaxMissingRatio < 0.01 || m.MaxMissingRatio > 0.9 {
		return fmt.Errorf("max_missing_ratio must be between 0.01 and 0.9")
	}
	if m.TTLSeconds == 0 {
		m.TTLSeconds = 900
	}
	if m.TTLSeconds < 30 || m.TTLSeconds > 24*3600 {
		return fmt.Errorf("ttl_seconds must be between 30 and 86400")
	}
	if m.HeadroomRatio == 0 {
		m.HeadroomRatio = 1
	}
	if m.HeadroomRatio < 1 || m.HeadroomRatio > 10 {
		return fmt.Errorf("headroom_ratio must be between 1 and 10")
	}
	if m.PodCapacityRPS < 0 {
		return fmt.Errorf("pod_capacity_rps must not be negative")
	}
	switch m.ReplicaMode {
	case "":
		m.ReplicaMode = ReplicaModeFixed
	case ReplicaModeFixed, ReplicaModeDynamic:
	default:
		return fmt.Errorf("replica_mode must be fixed or dynamic")
	}
	switch m.CapacityMode {
	case "":
		m.CapacityMode = CapacityModeManual
	case CapacityModeManual, CapacityModeEstimate:
	default:
		return fmt.Errorf("capacity_mode must be manual or estimate")
	}
	if m.PodCPURequestMilli < 0 || m.PodMemoryRequestBytes < 0 {
		return fmt.Errorf("pod requests must not be negative")
	}
	if m.TargetUtilization < 0 || m.TargetUtilization > 1 {
		return fmt.Errorf("target_utilization must be between 0 and 1")
	}
	switch m.PredictMode {
	case "":
		m.PredictMode = PredictDynamic
	case PredictNone, PredictFixed, PredictDynamic:
	default:
		return fmt.Errorf("predict_mode must be one of %v", PredictModes)
	}
	m.PredictAlgorithm = strings.TrimSpace(m.PredictAlgorithm)
	if m.PredictMode == PredictFixed && m.PredictAlgorithm == "" && m.PredictEnabled {
		return fmt.Errorf("predict_algorithm is required when the prediction mode is fixed")
	}
	if m.PredictMode != PredictFixed {
		m.PredictAlgorithm = ""
	}
	if m.OOMWeight == 0 {
		m.OOMWeight = DefaultOOMWeight
	}
	if m.OOMWeight < 0 || m.OOMWeight > 1 {
		return fmt.Errorf("oom_weight must be between 0 and 1")
	}
	if m.OOMLookbackDays == 0 {
		m.OOMLookbackDays = 14
	}
	if m.OOMLookbackDays < 1 || m.OOMLookbackDays > 180 {
		return fmt.Errorf("oom_lookback_days must be between 1 and 180")
	}
	if m.OOMMemoryUplift == 0 {
		m.OOMMemoryUplift = 1.2
	}
	if m.OOMMemoryUplift < 1 || m.OOMMemoryUplift > 3 {
		return fmt.Errorf("oom_memory_uplift must be between 1 and 3")
	}
	if m.JVMHeapPressure < 0 || m.JVMHeapPressure > 1 {
		return fmt.Errorf("jvm_heap_pressure must be between 0 and 1 (0 disables the rule)")
	}
	if m.RecoverCycles == 0 {
		m.RecoverCycles = 2
	}
	if m.RecoverCycles < 1 || m.RecoverCycles > 20 {
		return fmt.Errorf("recover_cycles must be between 1 and 20")
	}
	if m.MinScore < 0 || m.MinScore > 100 {
		return fmt.Errorf("min_score must be between 0 and 100")
	}
	switch m.MaxReplicasMode {
	case "":
		m.MaxReplicasMode = MaxReplicasFixed
	case MaxReplicasFixed, MaxReplicasDynamic:
	default:
		return fmt.Errorf("max_replicas_mode must be fixed or dynamic")
	}
	if m.HardMaxReplicas < 0 || m.ExpandStepRatio < 0 || m.ExpandStepRatio > 5 {
		return fmt.Errorf("hard_max_replicas must not be negative and expand_step_ratio must be between 0 and 5")
	}
	if m.MaxReplicasMode == MaxReplicasDynamic {
		if m.MaxReplicas <= 0 || m.HardMaxReplicas < m.MaxReplicas {
			return fmt.Errorf("dynamic expansion needs max_replicas and a hard_max_replicas that is not below it")
		}
		if m.ExpandStepRatio == 0 {
			m.ExpandStepRatio = 0.25
		}
	}
	if m.HardMaxReplicas > 0 && m.MaxReplicas > m.HardMaxReplicas {
		return fmt.Errorf("max_replicas exceeds hard_max_replicas")
	}
	if m.MinGPU < 0 || m.MaxGPU < 0 || m.FallbackGPU < 0 || (m.MaxGPU > 0 && m.MinGPU > m.MaxGPU) {
		return fmt.Errorf("invalid gpu bounds")
	}
	if m.Timezone == "" {
		m.Timezone = DefaultTimezone
	}
	if _, err := time.LoadLocation(m.Timezone); err != nil {
		return fmt.Errorf("unknown timezone %q", m.Timezone)
	}
	for name, v := range map[string]int64{
		"min_cpu_milli": m.MinCPUMilli, "max_cpu_milli": m.MaxCPUMilli, "min_memory_bytes": m.MinMemoryBytes,
		"max_memory_bytes": m.MaxMemoryBytes, "fallback_cpu_milli": m.FallbackCPUMilli, "fallback_memory_bytes": m.FallbackMemoryBytes,
		"min_replicas": int64(m.MinReplicas), "max_replicas": int64(m.MaxReplicas), "fallback_replicas": int64(m.FallbackReplicas),
	} {
		if v < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	if m.MaxCPUMilli > 0 && m.MinCPUMilli > m.MaxCPUMilli {
		return fmt.Errorf("min_cpu_milli exceeds max_cpu_milli")
	}
	if m.MaxMemoryBytes > 0 && m.MinMemoryBytes > m.MaxMemoryBytes {
		return fmt.Errorf("min_memory_bytes exceeds max_memory_bytes")
	}
	if m.MaxReplicas > 0 && m.MinReplicas > m.MaxReplicas {
		return fmt.Errorf("min_replicas exceeds max_replicas")
	}
	return nil
}

// ModulePeriod is an administrator fixed-value window (see decision.FixedPeriod).
type ModulePeriod struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"false"`

	ModuleID    uint       `gorm:"not null;uniqueIndex:idx_module_period,priority:1" json:"module_id" column_setting:"true"`
	Name        string     `gorm:"type:varchar(100);not null;uniqueIndex:idx_module_period,priority:2" json:"name" column_setting:"true"`
	Enabled     bool       `gorm:"not null" json:"enabled" column_setting:"true"`
	Priority    int        `gorm:"not null;default:0" json:"priority" column_setting:"true"`
	Weekdays    int        `gorm:"not null;default:0" json:"weekdays" column_setting:"true"` // bit n = time.Weekday(n)
	StartMinute int        `gorm:"not null;default:0" json:"start_minute" column_setting:"true"`
	EndMinute   int        `gorm:"not null;default:0" json:"end_minute" column_setting:"true"`
	AbsStart    *time.Time `json:"abs_start" column_setting:"true"`
	AbsEnd      *time.Time `json:"abs_end" column_setting:"true"`
	CPUMilli    int64      `gorm:"not null;default:0" json:"cpu_milli" column_setting:"true"`
	MemoryBytes int64      `gorm:"not null;default:0" json:"memory_bytes" column_setting:"true"`
	Replicas    int        `gorm:"not null;default:0" json:"replicas" column_setting:"true"`
	Remark      string     `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`
}

func (ModulePeriod) TableName() string { return "module_period" }

// MetricSeries identifies the stored history of one metric source of one algorithm instance.
type MetricSeries struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`

	ModuleID    uint   `gorm:"not null;uniqueIndex:idx_metric_series,priority:1" json:"module_id"`
	Metric      string `gorm:"type:varchar(20);not null;uniqueIndex:idx_metric_series,priority:2" json:"metric"`
	MappingID   uint   `gorm:"not null" json:"mapping_id"`
	StepSeconds int    `gorm:"not null" json:"step_seconds"`
	// Shape is the scale, precision and aggregation the stored samples were made with (MetricSource.Shape).
	Shape string `gorm:"type:varchar(40);not null;default:''" json:"shape"`
}

func (MetricSeries) TableName() string { return "metric_series" }

// MetricSample is one stored point. The primary key makes re-collecting a window idempotent. Times are UTC unix
// seconds; values are finite (NaN samples are counted by the quality report and not stored).
type MetricSample struct {
	SeriesID uint    `gorm:"primaryKey;autoIncrement:false" json:"series_id"`
	TS       int64   `gorm:"primaryKey;autoIncrement:false" json:"ts"`
	Value    float64 `gorm:"not null" json:"value"`
}

func (MetricSample) TableName() string { return "metric_sample" }

// Prediction is one forecast point. It is never overwritten: a new run adds rows. QuantilePermille 500 is the
// point forecast.
type Prediction struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`

	ModuleID         uint      `gorm:"not null;uniqueIndex:idx_prediction,priority:1;index:idx_prediction_lookup,priority:1" json:"module_id"`
	Metric           string    `gorm:"type:varchar(20);not null;uniqueIndex:idx_prediction,priority:2;index:idx_prediction_lookup,priority:2" json:"metric"`
	PredictionTime   time.Time `gorm:"not null;uniqueIndex:idx_prediction,priority:3;index:idx_prediction_lookup,priority:3" json:"prediction_time"`
	TargetTime       time.Time `gorm:"not null;uniqueIndex:idx_prediction,priority:4" json:"target_time"`
	AlgorithmID      string    `gorm:"type:varchar(40);not null;uniqueIndex:idx_prediction,priority:5" json:"algorithm_id"`
	ModelVersion     string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_prediction,priority:6" json:"model_version"`
	QuantilePermille int       `gorm:"not null;default:500;uniqueIndex:idx_prediction,priority:7" json:"quantile_permille"`
	Value            float64   `gorm:"not null" json:"value"`
	Unit             string    `gorm:"type:varchar(20);not null" json:"unit"`
}

func (Prediction) TableName() string { return "forecast_prediction" }

// BacktestRun records one rolling-origin comparison with the baseline and the promotion verdict.
type BacktestRun struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID     uint      `gorm:"not null;index:idx_backtest_lookup,priority:1" json:"module_id" column_setting:"true"`
	Metric       string    `gorm:"type:varchar(20);not null;index:idx_backtest_lookup,priority:2" json:"metric" column_setting:"true"`
	AlgorithmID  string    `gorm:"type:varchar(40);not null;index:idx_backtest_lookup,priority:3" json:"algorithm_id" column_setting:"true"`
	ModelVersion string    `gorm:"type:varchar(64);not null" json:"model_version" column_setting:"false"`
	Params       string    `gorm:"type:text" json:"params" column_setting:"false"`
	BaselineID   string    `gorm:"type:varchar(40)" json:"baseline_id" column_setting:"true"`
	WindowStart  time.Time `json:"window_start" column_setting:"false"`
	WindowEnd    time.Time `json:"window_end" column_setting:"false"`
	StepSeconds  int       `gorm:"not null" json:"step_seconds" column_setting:"false"`
	HorizonSteps int       `gorm:"not null" json:"horizon_steps" column_setting:"false"`
	Cutoffs      int       `gorm:"not null" json:"cutoffs" column_setting:"false"`
	Metrics      string    `gorm:"type:text" json:"metrics" column_setting:"false"`          // JSON backtest.Result
	BaselineRes  string    `gorm:"type:text" json:"baseline_metrics" column_setting:"false"` // JSON backtest.Result
	Promoted     bool      `gorm:"not null" json:"promoted" column_setting:"true"`
	Reason       string    `gorm:"type:varchar(512)" json:"reason" column_setting:"true"`
}

func (BacktestRun) TableName() string { return "backtest_run" }

// Recommendation is an immutable, versioned snapshot of what a module should run with. The decision is stored
// with the inputs it was made from, so it can be replayed and explained.
type Recommendation struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID   uint      `gorm:"not null;uniqueIndex:idx_recommendation_version,priority:1" json:"module_id" column_setting:"true"`
	Version    int       `gorm:"not null;uniqueIndex:idx_recommendation_version,priority:2" json:"version" column_setting:"true"`
	Mode       string    `gorm:"type:varchar(16);not null" json:"mode" column_setting:"true"`
	ValidUntil time.Time `gorm:"index" json:"valid_until" column_setting:"true"`

	CPUSet         bool   `gorm:"not null" json:"cpu_set" column_setting:"false"`
	CPUMilli       int64  `gorm:"not null;default:0" json:"cpu_milli" column_setting:"true"`
	CPUSource      string `gorm:"type:varchar(20)" json:"cpu_source" column_setting:"true"`
	MemorySet      bool   `gorm:"not null" json:"memory_set" column_setting:"false"`
	MemoryBytes    int64  `gorm:"not null;default:0" json:"memory_bytes" column_setting:"true"`
	MemorySource   string `gorm:"type:varchar(20)" json:"memory_source" column_setting:"true"`
	ReplicasSet    bool   `gorm:"not null" json:"replicas_set" column_setting:"false"`
	Replicas       int    `gorm:"not null;default:0" json:"replicas" column_setting:"true"`
	ReplicasSource string `gorm:"type:varchar(20)" json:"replicas_source" column_setting:"true"`
	GPUSet         bool   `gorm:"not null;default:false" json:"gpu_set" column_setting:"false"`
	GPU            int    `gorm:"not null;default:0" json:"gpu" column_setting:"true"`
	GPUSource      string `gorm:"type:varchar(20)" json:"gpu_source" column_setting:"true"`
	// Algorithm and Score are the instance whose forecast decided the recommendation and its score; empty when the
	// recommendation did not come from a forecast.
	Algorithm string  `gorm:"type:varchar(40)" json:"algorithm" column_setting:"true"`
	Score     float64 `gorm:"not null;default:0" json:"score" column_setting:"true"`
	// EffectiveMaxReplicas is the replica ceiling in force: the module maximum, or the raised one under dynamic expansion.
	EffectiveMaxReplicas int `gorm:"not null;default:0" json:"effective_max_replicas" column_setting:"false"`

	Detail string `gorm:"type:text" json:"detail" column_setting:"false"` // JSON: per-field reasons and the inputs used
	// Degraded is true when at least one field came from the fallback chain (module fallback or the original value)
	// because the forecast could not be used; DegradeReasons is a JSON object of field -> reason code. OOMFloor is
	// true when the OOM protection raised the memory value.
	Degraded       bool   `gorm:"not null;default:false;index" json:"degraded" column_setting:"true"`
	DegradeReasons string `gorm:"type:text" json:"degrade_reasons" column_setting:"false"`
	OOMFloor       bool   `gorm:"not null;default:false" json:"oom_floor" column_setting:"true"`
	// ResourceVersion is the version of the Pod resource spec (CPU and memory) the snapshot carries: it changes only when the
	// spec does, so every snapshot between two changes has the same one. ResourceChangedAt is when it last changed; ResourceHeld
	// is true when this snapshot kept the spec in force although the forecast computed another value.
	ResourceVersion   int        `gorm:"not null;default:0" json:"resource_version" column_setting:"true"`
	ResourceChangedAt *time.Time `json:"resource_changed_at" column_setting:"false"`
	ResourceHeld      bool       `gorm:"not null;default:false" json:"resource_held" column_setting:"true"`
	// Snapshot marks the recommendations that are kept as history: the first, every one whose outcome differs from the one before
	// it (replicas, resource spec, algorithm, degradation, mode) and one an hour at least. The others are the same answer
	// repeated every cycle: they keep the current recommendation valid and are dropped soon after. SnapshotReason says why.
	Snapshot       bool   `gorm:"not null;default:false;index" json:"snapshot" column_setting:"true"`
	SnapshotReason string `gorm:"type:varchar(200)" json:"snapshot_reason" column_setting:"false"`
	// Urgent says the replicas came from the load measured now because the forecast was behind it: the operator acts at once, without
	// the cooldown or the step limit of a scale-up.
	Urgent bool `gorm:"not null;default:false" json:"urgent" column_setting:"false"`
}

func (Recommendation) TableName() string { return "recommendation" }

func init() { RegisterModel(&PredictionEvaluation{}) }

// PredictionEvaluation scores one finished prediction run against the actuals that arrived afterwards. The
// prediction rows are never touched: the evaluation is a separate, append-only fact.
type PredictionEvaluation struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID         uint      `gorm:"not null;uniqueIndex:idx_prediction_eval,priority:1;index:idx_prediction_eval_lookup,priority:1" json:"module_id" column_setting:"true"`
	Metric           string    `gorm:"type:varchar(20);not null;uniqueIndex:idx_prediction_eval,priority:2;index:idx_prediction_eval_lookup,priority:2" json:"metric" column_setting:"true"`
	AlgorithmID      string    `gorm:"type:varchar(40);not null;uniqueIndex:idx_prediction_eval,priority:3;index:idx_prediction_eval_lookup,priority:3" json:"algorithm_id" column_setting:"true"`
	ModelVersion     string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_prediction_eval,priority:4" json:"model_version" column_setting:"false"`
	PredictionTime   time.Time `gorm:"not null;uniqueIndex:idx_prediction_eval,priority:5;index:idx_prediction_eval_lookup,priority:4" json:"prediction_time" column_setting:"true"`
	QuantilePermille int       `gorm:"not null;default:500" json:"quantile_permille" column_setting:"false"`
	Points           int       `gorm:"not null" json:"points" column_setting:"true"`    // forecast steps that had an actual
	Expected         int       `gorm:"not null" json:"expected" column_setting:"false"` // forecast steps in the run
	MAE              float64   `gorm:"not null" json:"mae" column_setting:"true"`
	RMSE             float64   `gorm:"not null" json:"rmse" column_setting:"false"`
	WAPE             float64   `gorm:"not null" json:"wape" column_setting:"true"`
	Bias             float64   `gorm:"not null" json:"bias" column_setting:"false"`      // mean(prediction - actual), positive = over-prediction
	PeakUnder        float64   `gorm:"not null" json:"peak_under" column_setting:"true"` // relative shortfall of the predicted peak
	Coverage         float64   `gorm:"not null" json:"coverage" column_setting:"false"`  // share of actuals at or below the forecast
}

func (PredictionEvaluation) TableName() string { return "prediction_evaluation" }

func init() { RegisterModel(&ScalingExecution{}) }

// Scaling execution results reported by the operator.
const (
	ExecApplied  = "applied"
	ExecObserved = "observed"
	ExecHeld     = "held"
	ExecConflict = "conflict"
	ExecError    = "error"
)

// ScalingExecutions are the operator's reports: what it read, decided and did for a workload. They are the
// audit trail that lets a decision be traced to the replicas that actually ran.
type ScalingExecution struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID        uint   `gorm:"not null;index" json:"module_id" column_setting:"true"`
	Namespace       string `gorm:"type:varchar(253);not null" json:"namespace" column_setting:"true"`
	Scaler          string `gorm:"type:varchar(253);not null" json:"scaler" column_setting:"true"`
	TargetKind      string `gorm:"type:varchar(32);not null" json:"target_kind" column_setting:"true"`
	TargetName      string `gorm:"type:varchar(253);not null" json:"target_name" column_setting:"true"`
	Current         int    `gorm:"not null" json:"current" column_setting:"true"`
	Ready           int    `gorm:"not null" json:"ready" column_setting:"true"`
	Desired         int    `gorm:"not null" json:"desired" column_setting:"true"`
	Action          string `gorm:"type:varchar(16);not null" json:"action" column_setting:"true"`
	Result          string `gorm:"type:varchar(16);not null;index" json:"result" column_setting:"true"`
	Reason          string `gorm:"type:varchar(512)" json:"reason" column_setting:"true"`
	DecisionVersion int64  `gorm:"not null;default:0" json:"decision_version" column_setting:"false"`
	// RecordID is the id the operator gave the action when it took it, unique across the platform: a report that is sent again (the
	// answer was lost, or the platform was away and the operator kept it) is stored once. Rows written before this field existed have
	// none. CreatedAt is when the operator acted; ReportedAt is when the platform heard of it, later by the length of an outage.
	RecordID   *string    `gorm:"type:varchar(64);uniqueIndex" json:"record_id" column_setting:"true"`
	ReportedAt *time.Time `json:"reported_at" column_setting:"false"`
	// DecisionSource is what the replicas followed (decision, cache, fallback or frozen), DegradeState the state of the module in an
	// outage of the platform (normal, cached, holding, degraded, recovering) and Operator the Pod that acted.
	DecisionSource string `gorm:"type:varchar(16)" json:"decision_source" column_setting:"false"`
	DegradeState   string `gorm:"type:varchar(16)" json:"degrade_state" column_setting:"false"`
	Operator       string `gorm:"type:varchar(253)" json:"operator" column_setting:"false"`
}

func (ScalingExecution) TableName() string { return "scaling_execution" }
