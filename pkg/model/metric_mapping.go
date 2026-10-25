package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterModel(&MetricMapping{})
}

// MetricMapping binds one logical metric at one aggregation level to a PromQL template on a datasource.
type MetricMapping struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"false"`

	DatasourceID uint   `gorm:"not null;index;uniqueIndex:idx_metric_mapping_ds_name,priority:1" json:"datasource_id" column_setting:"true"`
	Name         string `gorm:"type:varchar(100);not null;uniqueIndex:idx_metric_mapping_ds_name,priority:2" json:"name" column_setting:"true"`
	Metric       string `gorm:"type:varchar(20);not null;index" json:"metric" column_setting:"true"`
	Level        string `gorm:"type:varchar(20);not null" json:"level" column_setting:"true"`
	Query        string `gorm:"type:text;not null" json:"query" column_setting:"true"`
	Unit         string `gorm:"type:varchar(20);not null" json:"unit" column_setting:"true"`
	StepSeconds  int    `gorm:"not null;default:60" json:"step_seconds" column_setting:"true"`
	Enabled      bool   `gorm:"not null" json:"enabled" column_setting:"true"`
	Remark       string `gorm:"type:text" json:"remark" column_setting:"true"`
	// PresetID is the built-in preset the mapping was created from; empty for a hand-written mapping.
	PresetID string `gorm:"type:varchar(64);not null;default:''" json:"preset_id" column_setting:"true"`
}

func (MetricMapping) TableName() string { return "metric_mapping" }

// CustomMetric is the kind of a user-written Prometheus metric: its unit is whatever the mapping says.
const CustomMetric = "custom"

// MetricKinds maps each supported logical metric to its canonical unit. Rate and counter
// conversion belongs in the PromQL template; the unit states what the template returns.
var MetricKinds = map[string]string{
	"cpu":      "cores",
	"memory":   "bytes",
	"rps":      "rps",
	"latency":  "seconds",
	"oom":      "count",
	"replicas": "count",

	// GPU, network and user-defined Prometheus metrics. A custom metric keeps the unit of its mapping.
	"gpu":        "gpus",
	"gpu_memory": "bytes",
	"network_rx": "bytes_per_second",
	"network_tx": "bytes_per_second",
	CustomMetric: "",

	// OOM detail, JVM and Kubernetes configuration metrics. They are collected next to the forecast metrics; an
	// input set can hand them to the algorithms that read covariates, and the OOM and JVM ones also act on the
	// recommendation directly (OOM floor, heap pressure).
	"oom_working_set":  "bytes",
	"jvm_heap_used":    "bytes",
	"jvm_heap_max":     "bytes",
	"jvm_nonheap_used": "bytes",
	"jvm_gc_pause":     "seconds",
	"jvm_threads":      "count",
	"k8s_cpu_requests": "cores",
	"k8s_cpu_limits":   "cores",
	"k8s_mem_requests": "bytes",
	"k8s_mem_limits":   "bytes",
	"k8s_cpu_throttle": "seconds",

	// Utilisation, GPU and network detail. A ratio is 0-1 (1 = fully used); PromQL that returns a percentage is scaled
	// by the source (scale 0.01).
	"cpu_util":         "ratio",
	"memory_util":      "ratio",
	"gpu_util":         "ratio",
	"gpu_memory_total": "bytes",
	"network_conn":     "count",
	"network_error":    "ratio",
}

// AuxiliaryMetrics are the collected metrics that are never forecast themselves, in a stable order.
var AuxiliaryMetrics = []string{
	"replicas", "oom", "oom_working_set", "latency", "jvm_heap_used", "jvm_heap_max", "jvm_nonheap_used", "jvm_gc_pause",
	"jvm_threads", "gpu_memory", "network_rx", "network_tx", CustomMetric, "k8s_cpu_requests", "k8s_cpu_limits", "k8s_mem_requests", "k8s_mem_limits", "k8s_cpu_throttle",
	"cpu_util", "memory_util", "gpu_util", "gpu_memory_total", "network_conn", "network_error",
}

// Metric categories: how the pages group the metrics of a dataset.
const (
	CategoryCPU     = "cpu"
	CategoryMemory  = "memory"
	CategoryGPU     = "gpu"
	CategoryNetwork = "network"
	CategoryOther   = "other"
)

// MetricCategories lists the categories in the order the pages show them.
var MetricCategories = []string{CategoryCPU, CategoryMemory, CategoryGPU, CategoryNetwork, CategoryOther}

// KindCategory is the category of a metric kind.
func KindCategory(kind string) string {
	switch kind {
	case "cpu", "cpu_util", "k8s_cpu_requests", "k8s_cpu_limits", "k8s_cpu_throttle":
		return CategoryCPU
	case "memory", "memory_util", "oom_working_set", "jvm_heap_used", "jvm_heap_max", "jvm_nonheap_used", "k8s_mem_requests", "k8s_mem_limits":
		return CategoryMemory
	case "gpu", "gpu_util", "gpu_memory", "gpu_memory_total":
		return CategoryGPU
	case "network_rx", "network_tx", "network_conn", "network_error", "latency":
		return CategoryNetwork
	}
	return CategoryOther
}

// IsAuxiliaryMetric reports whether the metric is collected as context for the forecast metrics.
func IsAuxiliaryMetric(metric string) bool {
	for _, m := range AuxiliaryMetrics {
		if m == metric {
			return true
		}
	}
	return false
}

// MetricLevels lists the aggregation levels and the template variables each one requires.
// RPS is often only available per Service, so the level is recorded, never inferred.
var MetricLevels = map[string][]string{
	"cluster":   {},
	"namespace": {"namespace"},
	"workload":  {"namespace", "workload"},
	"service":   {"namespace", "service"},
	"pod":       {"namespace", "pod"},
	"container": {"namespace", "workload", "container"},
}

// MetricVariables are the placeholders a template may use as {{name}}; range is the
// lookback window (e.g. 5m) injected by the collector.
var MetricVariables = []string{"namespace", "workload", "service", "pod", "container", "range"}

const (
	MinStepSeconds = 15
	MaxStepSeconds = 3600
)

var placeholderPattern = regexp.MustCompile(`\{\{\s*([^{}\s]*)\s*\}\}`)

// TemplateVariables returns the distinct placeholders used in a PromQL template.
func TemplateVariables(query string) []string {
	seen := map[string]struct{}{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(query, -1) {
		seen[match[1]] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateMetricMapping checks the static consistency of a mapping and fills defaults.
// It does not run the query.
func ValidateMetricMapping(m *MetricMapping) error {
	m.Name = strings.TrimSpace(m.Name)
	m.Query = strings.TrimSpace(m.Query)
	if m.Name == "" {
		return fmt.Errorf("name is required")
	}
	unit, ok := MetricKinds[m.Metric]
	if !ok {
		return fmt.Errorf("unsupported metric %q", m.Metric)
	}
	if m.Metric == CustomMetric {
		m.Unit = strings.TrimSpace(m.Unit)
		if m.Unit == "" {
			m.Unit = "value"
		}
	} else {
		if m.Unit == "" {
			m.Unit = unit
		}
		if m.Unit != unit {
			return fmt.Errorf("metric %q must use unit %q", m.Metric, unit)
		}
	}
	required, ok := MetricLevels[m.Level]
	if !ok {
		return fmt.Errorf("unsupported level %q", m.Level)
	}
	if m.Query == "" {
		return fmt.Errorf("query is required")
	}
	if strings.Count(m.Query, "{{") != strings.Count(m.Query, "}}") {
		return fmt.Errorf("query has unbalanced template braces")
	}
	allowed := map[string]struct{}{}
	for _, name := range MetricVariables {
		allowed[name] = struct{}{}
	}
	used := map[string]struct{}{}
	for _, name := range TemplateVariables(m.Query) {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown template variable %q", name)
		}
		used[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := used[name]; !ok {
			return fmt.Errorf("level %q requires template variable {{%s}}", m.Level, name)
		}
	}
	if m.StepSeconds == 0 {
		m.StepSeconds = 60
	}
	if m.StepSeconds < MinStepSeconds || m.StepSeconds > MaxStepSeconds {
		return fmt.Errorf("step_seconds must be between %d and %d", MinStepSeconds, MaxStepSeconds)
	}
	return nil
}
