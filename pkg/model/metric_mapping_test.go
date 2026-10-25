package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validMapping() *MetricMapping {
	return &MetricMapping{
		DatasourceID: 1, Name: "cpu-workload", Metric: "cpu", Level: "workload",
		Query: `sum(rate(container_cpu_usage_seconds_total{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`,
	}
}

func TestValidateMetricMappingFillsDefaults(t *testing.T) {
	m := validMapping()
	require.NoError(t, ValidateMetricMapping(m))
	assert.Equal(t, "cores", m.Unit)
	assert.Equal(t, 60, m.StepSeconds)
}

func TestValidateMetricMappingRejectsInvalid(t *testing.T) {
	cases := map[string]func(*MetricMapping){
		"empty name":        func(m *MetricMapping) { m.Name = "  " },
		"unknown metric":    func(m *MetricMapping) { m.Metric = "disk" },
		"wrong unit":        func(m *MetricMapping) { m.Unit = "bytes" },
		"unknown level":     func(m *MetricMapping) { m.Level = "node" },
		"empty query":       func(m *MetricMapping) { m.Query = "" },
		"unbalanced braces": func(m *MetricMapping) { m.Query = "up{namespace=\"{{namespace\"}" },
		"unknown variable":  func(m *MetricMapping) { m.Query += ` + {{cluster}}` },
		"missing required":  func(m *MetricMapping) { m.Query = `sum(rate(x{namespace="{{namespace}}"}[{{range}}]))` },
		"step too small":    func(m *MetricMapping) { m.StepSeconds = 5 },
		"step too large":    func(m *MetricMapping) { m.StepSeconds = 7200 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validMapping()
			mutate(m)
			assert.Error(t, ValidateMetricMapping(m))
		})
	}
}

func TestValidateMetricMappingLevelRequirements(t *testing.T) {
	m := &MetricMapping{Name: "rps", Metric: "rps", Level: "service", Query: `sum(rate(http_requests_total{namespace="{{namespace}}",service="{{service}}"}[{{range}}]))`}
	require.NoError(t, ValidateMetricMapping(m))
	cluster := &MetricMapping{Name: "c", Metric: "memory", Level: "cluster", Query: `sum(container_memory_working_set_bytes)`}
	require.NoError(t, ValidateMetricMapping(cluster))
	assert.Equal(t, "bytes", cluster.Unit)
}

func TestTemplateVariablesIsDistinctAndSorted(t *testing.T) {
	assert.Equal(t, []string{"namespace", "range"}, TemplateVariables(`a{n="{{namespace}}"}[{{ range }}] + b{n="{{namespace}}"}`))
}
