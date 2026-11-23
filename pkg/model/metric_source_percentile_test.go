package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestASourceStatisticAndAggregationAcceptAnyPercentile(t *testing.T) {
	for _, level := range []string{"p40", "p41", "p42", "p95", "p99.9"} {
		s := MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Stat: level, Aggregation: level}
		assert.NoError(t, s.Validate(), level)
		assert.Equal(t, level, s.Stat)
	}
	for _, bad := range []string{"p0", "p100", "pp", "p99.99999", "p1e1"} {
		s := MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Stat: bad}
		assert.Error(t, s.Validate(), bad)
		s = MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Aggregation: bad}
		assert.Error(t, s.Validate(), bad)
	}
}

// A source is for the dashboard, for the history or for both: each reading takes the sources that are for it, and the requests are for the dashboard.
func TestASourceIsForTheDashboardTheHistoryOrBoth(t *testing.T) {
	both := &MetricSource{Key: "cpu", Kind: "cpu", Enabled: true}
	dash := &MetricSource{Key: "r", Kind: "k8s_cpu_requests", Enabled: true, Purpose: PurposeDashboard}
	hist := &MetricSource{Key: "h", Kind: "oom", Enabled: true, Purpose: PurposeHistory}
	sources := []*MetricSource{both, dash, hist}

	history := (&AlgorithmRun{Sources: sources}).ActiveSources()
	assert.Equal(t, []string{"cpu", "h"}, keysOf(history), "the history does not read what is for the dashboard")
	board := (&AlgorithmRun{Sources: sources, Dashboard: true}).ActiveSources()
	assert.Equal(t, []string{"cpu", "r"}, keysOf(board), "the dashboard does not read what is for the history")

	assert.Equal(t, PurposeDashboard, DefaultPurpose("k8s_cpu_requests"))
	assert.Equal(t, PurposeDashboard, DefaultPurpose("k8s_mem_requests"))
	assert.Equal(t, PurposeBoth, DefaultPurpose("k8s_mem_limits"), "the OOM protection reads the memory limit from the history")
	assert.Equal(t, PurposeBoth, DefaultPurpose("cpu"))

	// what the query answers: the source says, else the level of the template does
	assert.Equal(t, PerTotal, (&MetricSource{}).PerOf("workload"))
	assert.Equal(t, PerPod, (&MetricSource{}).PerOf("container"))
	assert.Equal(t, PerPod, (&MetricSource{Per: PerPod}).PerOf("workload"))
	assert.Equal(t, PerTotal, (&MetricSource{Per: PerTotal}).PerOf("container"))

	s := &MetricSource{Name: "x", Kind: "k8s_cpu_requests", MappingID: 1, Purpose: PurposeHistory}
	assert.Error(t, s.Validate(), "the CPU requests are not for the history alone")
	s = &MetricSource{Name: "x", Kind: "k8s_cpu_requests", MappingID: 1}
	require.NoError(t, s.Validate())
	assert.Equal(t, PurposeDashboard, s.Purpose)
	s = &MetricSource{Name: "x", Kind: "cpu", MappingID: 1, Per: "some"}
	assert.Error(t, s.Validate())
}

func keysOf(list []*MetricSource) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Key
	}
	return out
}
