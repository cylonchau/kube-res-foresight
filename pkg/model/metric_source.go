package model

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/multimetric"
)

func init() {
	RegisterModel(&MetricSource{})
	RegisterModel(&AlgorithmScore{})
	RegisterModel(&AdmissionDecision{})
}

// Metric source resources: what a source feeds in the recommendation.
const (
	ResourceCPU      = multimetric.ResourceCPU
	ResourceMemory   = multimetric.ResourceMemory
	ResourceGPU      = multimetric.ResourceGPU
	ResourceReplicas = multimetric.ResourceReplicas // a throughput signal such as requests per second
	ResourceSignal   = multimetric.ResourceSignal   // pressure only
)

// Source bases: which values the calculation reduces.
const (
	BasisForecast = "forecast" // the predicted values over the horizon
	BasisHistory  = "history"  // the measured values over WindowHours (an OOM count has no forecast worth trusting)
)

// SourceResources lists the resources in a stable order.
var SourceResources = []string{ResourceCPU, ResourceMemory, ResourceGPU, ResourceReplicas, ResourceSignal}

// MetricSource is one metric of a module the user added: a Prometheus query (through a metric mapping) with the
// role it plays in the recommendation. A module can have several sources of one kind, for example the CPU of the Pod
// and the CPU of the host serving its GPU, each with its own priority, weight, statistic and trigger threshold.
type MetricSource struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"false"`

	ModuleID uint `gorm:"not null;uniqueIndex:idx_metric_source_key,priority:1;uniqueIndex:idx_metric_source_name,priority:1" json:"module_id" column_setting:"true"`
	// Key names the stored series of the source (and the metric label of its predictions); it is generated from
	// the kind and never changes.
	Key  string `gorm:"type:varchar(20);not null;uniqueIndex:idx_metric_source_key,priority:2" json:"key" column_setting:"true"`
	Name string `gorm:"type:varchar(100);not null;uniqueIndex:idx_metric_source_name,priority:2" json:"name" column_setting:"true"`
	Kind string `gorm:"type:varchar(24);not null" json:"kind" column_setting:"true"`
	// Resource is what the source feeds: cpu, memory, gpu, a throughput signal (replicas) or pressure only (signal).
	Resource  string `gorm:"type:varchar(16);not null" json:"resource" column_setting:"true"`
	MappingID uint   `gorm:"not null;default:0" json:"mapping_id" column_setting:"true"`

	Priority int     `gorm:"not null;default:0" json:"priority" column_setting:"true"`
	Weight   float64 `gorm:"not null;default:1" json:"weight" column_setting:"true"`
	// Stat reduces the values to one demand: avg, p50, p90, p95, p99, max, sum or last.
	Stat string `gorm:"type:varchar(8);not null;default:'p95'" json:"stat" column_setting:"true"`
	// Basis is forecast (reduce the predicted horizon) or history (reduce the measured window of WindowHours).
	Basis       string `gorm:"type:varchar(10);not null;default:'forecast'" json:"basis" column_setting:"false"`
	WindowHours int    `gorm:"not null;default:0" json:"window_hours" column_setting:"false"`
	// Threshold is the demand at which the source asks for more capacity; 0 = no trigger.
	Threshold float64 `gorm:"not null;default:0" json:"threshold" column_setting:"true"`
	Enabled   bool    `gorm:"not null" json:"enabled" column_setting:"true"`
	Remark    string  `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`

	// How the values of the source are shaped into the dataset, so metrics of different units and scales are
	// comparable for every algorithm. Scale multiplies what the PromQL returns (0.001 turns millicores into cores, 0.01
	// a percentage into a ratio); Precision is the number of decimals kept; Aggregation combines the samples that fall
	// into one step of the dataset when the module samples more coarsely than the query steps; Missing says how a step
	// without a sample is filled when a forecast is prepared (up to the module max_fill_steps).
	Scale       float64 `gorm:"not null;default:1" json:"scale" column_setting:"false"`
	Precision   int     `gorm:"not null;default:6" json:"precision" column_setting:"false"`
	Aggregation string  `gorm:"type:varchar(8);not null;default:'avg'" json:"aggregation" column_setting:"false"`
	Missing     string  `gorm:"type:varchar(16);not null;default:'interpolate'" json:"missing" column_setting:"false"`
}

func (MetricSource) TableName() string { return "metric_source" }

var sourceName = regexp.MustCompile(`^[^\s].{0,98}[^\s]$|^[^\s]$`)

// Aggregations of the samples of one dataset step.
const (
	AggAvg  = "avg"
	AggMax  = "max"
	AggMin  = "min"
	AggLast = "last"
	AggSum  = "sum"
)

// SourceAggregations lists the aggregations a source can use.
var SourceAggregations = []string{AggAvg, AggMax, AggMin, AggLast, AggSum}

// Ways to fill a step without a sample.
const (
	MissingInterpolate = "interpolate"  // a straight line between the samples around the gap
	MissingForwardFill = "forward_fill" // the last sample before the gap
	MissingZero        = "zero"         // 0: for counts where no sample means nothing happened
	MissingSkip        = "skip"         // no fill: a gap ends the usable history
)

// SourceMissing lists the ways a source can fill a gap.
var SourceMissing = []string{MissingInterpolate, MissingForwardFill, MissingZero, MissingSkip}

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// DefaultShape is the shape of a source that keeps its values as queried.
const DefaultShape = "x1|p6|avg"

// Shape is the part of the source that changes the stored values; the history is collected again when it changes.
func (s *MetricSource) Shape() string {
	return fmt.Sprintf("x%g|p%d|%s", s.Scale, s.Precision, s.Aggregation)
}

// DefaultMissing is how a kind normally fills a gap: a count where no sample means nothing happened is 0, a level is
// interpolated.
func DefaultMissing(kind string) string {
	switch kind {
	case "oom", "network_error":
		return MissingZero
	}
	return MissingInterpolate
}

// DefaultResource is the resource a kind normally feeds.
func DefaultResource(kind string) string {
	switch kind {
	case "cpu":
		return ResourceCPU
	case "memory":
		return ResourceMemory
	case "gpu", "gpu_memory":
		return ResourceGPU
	case "rps":
		return ResourceReplicas
	}
	return ResourceSignal
}

// DefaultBasis is where a kind's values normally come from.
func DefaultBasis(kind string) string {
	switch kind {
	case "oom", "oom_working_set", "replicas", "k8s_cpu_requests", "k8s_cpu_limits", "k8s_mem_requests", "k8s_mem_limits":
		return BasisHistory
	}
	return BasisForecast
}

// Validate checks the source on its own and fills the defaults.
func (s *MetricSource) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if !sourceName.MatchString(s.Name) {
		return fmt.Errorf("name is required and at most 100 characters")
	}
	unit, known := MetricKinds[s.Kind]
	_ = unit
	if !known {
		return fmt.Errorf("unsupported metric kind %q", s.Kind)
	}
	if s.MappingID == 0 {
		return fmt.Errorf("a metric mapping is required")
	}
	if s.Resource == "" {
		s.Resource = DefaultResource(s.Kind)
	}
	valid := false
	for _, r := range SourceResources {
		valid = valid || r == s.Resource
	}
	if !valid {
		return fmt.Errorf("resource must be one of %v", SourceResources)
	}
	// a CPU, memory or GPU metric can only feed its own resource; throughput and custom metrics are free
	switch s.Kind {
	case "cpu", "memory", "gpu", "gpu_memory":
		if s.Resource != DefaultResource(s.Kind) {
			return fmt.Errorf("a %s source feeds %s only", s.Kind, DefaultResource(s.Kind))
		}
	}
	if s.Stat == "" {
		s.Stat = multimetric.StatP95
	}
	if !multimetric.ValidStat(s.Stat) {
		return fmt.Errorf("stat must be one of avg, p50, p90, p95, p99, max, sum, last")
	}
	if s.Basis == "" {
		s.Basis = DefaultBasis(s.Kind)
	}
	if s.Basis != BasisForecast && s.Basis != BasisHistory {
		return fmt.Errorf("basis must be forecast or history")
	}
	if s.WindowHours < 0 || s.WindowHours > 24*180 {
		return fmt.Errorf("window_hours must be between 0 and %d", 24*180)
	}
	if s.Basis == BasisHistory && s.WindowHours == 0 {
		s.WindowHours = 24
	}
	if s.Weight < 0 {
		return fmt.Errorf("weight must not be negative")
	}
	if s.Threshold < 0 {
		return fmt.Errorf("threshold must not be negative")
	}
	if s.Priority < 0 || s.Priority > 1000 {
		return fmt.Errorf("priority must be between 0 and 1000")
	}
	if s.Scale == 0 {
		s.Scale = 1
	}
	if math.IsNaN(s.Scale) || math.IsInf(s.Scale, 0) || s.Scale <= 0 || s.Scale > 1e12 || s.Scale < 1e-12 {
		return fmt.Errorf("scale must be a positive number between 1e-12 and 1e12")
	}
	if s.Precision < 0 || s.Precision > 12 {
		return fmt.Errorf("precision must be between 0 and 12 decimals")
	}
	if s.Aggregation == "" {
		s.Aggregation = AggAvg
	}
	if !oneOf(s.Aggregation, SourceAggregations) {
		return fmt.Errorf("aggregation must be one of %v", SourceAggregations)
	}
	if s.Missing == "" {
		s.Missing = DefaultMissing(s.Kind)
	}
	if !oneOf(s.Missing, SourceMissing) {
		return fmt.Errorf("missing must be one of %v", SourceMissing)
	}
	return nil
}

// Category is the category of the source for the pages.
func (s *MetricSource) Category() string { return KindCategory(s.Kind) }

// Spec is the source as the calculation sees it.
func (s *MetricSource) Spec() multimetric.Source {
	return multimetric.Source{Key: s.Key, Name: s.Name, Kind: s.Kind, Resource: s.Resource, Priority: s.Priority, Weight: s.Weight,
		Stat: s.Stat, Threshold: s.Threshold}
}

// ---- scores and admission records

// AlgorithmScore is the grade of one algorithm for one module over one evaluation, kept so the score history and the
// ranking can be read back.
type AlgorithmScore struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID    uint    `gorm:"not null;index:idx_algorithm_score,priority:1" json:"module_id" column_setting:"true"`
	Algorithm   string  `gorm:"type:varchar(40);not null;index:idx_algorithm_score,priority:2" json:"algorithm" column_setting:"true"` // the algorithm id
	AlgorithmID string  `gorm:"type:varchar(40);not null" json:"algorithm_id" column_setting:"false"`
	Score       float64 `gorm:"not null" json:"score" column_setting:"true"`
	// the column is not called rank: that is a reserved word in MySQL 8, which an ORDER BY would trip over
	Rank    int `gorm:"column:rank_no;not null;default:0" json:"rank" column_setting:"true"`
	Windows int `gorm:"not null;default:0" json:"windows" column_setting:"true"`
	// Detail is the JSON of scoring.Result: the accuracies, the OOM penalty and the completeness behind the score.
	Detail      string    `gorm:"type:text" json:"detail" column_setting:"false"`
	WindowStart time.Time `json:"window_start" column_setting:"false"`
	WindowEnd   time.Time `json:"window_end" column_setting:"false"`
	// Selected is true when the algorithm was the one in use after this evaluation.
	Selected bool `gorm:"not null;default:false" json:"selected" column_setting:"true"`
}

func (AlgorithmScore) TableName() string { return "algorithm_score" }

// AdmissionDecision is the answer given to one admission request.
type AdmissionDecision struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `gorm:"index" json:"created_at" column_setting:"true"`

	ModuleID  uint   `gorm:"not null;index" json:"module_id" column_setting:"true"`
	Namespace string `gorm:"type:varchar(253)" json:"namespace" column_setting:"true"`
	Workload  string `gorm:"type:varchar(253)" json:"workload" column_setting:"true"`
	Container string `gorm:"type:varchar(253)" json:"container" column_setting:"true"`
	// Applied is true when the answer changes the Pod; Reason says why or why not.
	Applied bool   `gorm:"not null" json:"applied" column_setting:"true"`
	Reason  string `gorm:"type:varchar(512)" json:"reason" column_setting:"true"`
	Version int    `gorm:"not null;default:0" json:"version" column_setting:"true"`
	// The requests the answer carries (0 = left alone).
	CPUMilli    int64 `gorm:"not null;default:0" json:"cpu_milli" column_setting:"true"`
	MemoryBytes int64 `gorm:"not null;default:0" json:"memory_bytes" column_setting:"true"`
	GPU         int   `gorm:"not null;default:0" json:"gpu" column_setting:"true"`
	// The values the Pod asked for, when the caller sent them.
	OriginalCPUMilli    int64 `gorm:"not null;default:0" json:"original_cpu_milli" column_setting:"false"`
	OriginalMemoryBytes int64 `gorm:"not null;default:0" json:"original_memory_bytes" column_setting:"false"`
}

func (AdmissionDecision) TableName() string { return "admission_decision" }
