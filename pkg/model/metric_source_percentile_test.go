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

// A source is a display source or an algorithm source: the algorithms read only the second, the dashboard, the history and the charts read both.
func TestADisplaySourceIsNeverReadByAnAlgorithm(t *testing.T) {
	algo := &MetricSource{Key: "cpu", Kind: "cpu", Enabled: true}
	shown := &MetricSource{Key: "r", Kind: "k8s_cpu_requests", Enabled: true, Purpose: PurposeDisplay}
	legacy := &MetricSource{Key: "h", Kind: "oom", Enabled: true, Purpose: "history"}
	off := &MetricSource{Key: "x", Kind: "k8s_cpu_limits", Enabled: false, Purpose: PurposeDisplay}
	sources := []*MetricSource{algo, shown, legacy, off}

	assert.Equal(t, []string{"cpu", "h"}, keysOf((&AlgorithmRun{Sources: sources}).ActiveSources()), "an algorithm does not read what is only shown")
	assert.Equal(t, []string{"cpu", "r", "h"}, keysOf((&AlgorithmRun{Sources: sources, AllSources: true}).ActiveSources()), "the dashboard and the history read everything")

	assert.Equal(t, PurposeDisplay, DefaultPurpose("k8s_cpu_requests"))
	assert.Equal(t, PurposeDisplay, DefaultPurpose("k8s_mem_limits"))
	assert.Equal(t, PurposeAlgorithm, DefaultPurpose("cpu"))
	for _, c := range []struct{ in, want string }{{"both", PurposeAlgorithm}, {"history", PurposeAlgorithm}, {"dashboard", PurposeDisplay}, {"display", PurposeDisplay}} {
		got, ok := NormalPurpose(c.in, "cpu")
		assert.True(t, ok)
		assert.Equal(t, c.want, got, c.in)
	}
	_, ok := NormalPurpose("some", "cpu")
	assert.False(t, ok)

	// what the query answers: the source says, else the level of the template does
	assert.Equal(t, PerTotal, (&MetricSource{}).PerOf("workload"))
	assert.Equal(t, PerPod, (&MetricSource{}).PerOf("container"))
	assert.Equal(t, PerPod, (&MetricSource{Per: PerPod}).PerOf("workload"))
	assert.Equal(t, PerTotal, (&MetricSource{Per: PerTotal}).PerOf("container"))

	// the Requests and the Limits are display sources by default
	s := &MetricSource{Name: "x", Kind: "k8s_cpu_requests", MappingID: 1}
	require.NoError(t, s.Validate())
	assert.Equal(t, PurposeDisplay, s.Purpose)
	s = &MetricSource{Name: "x", Kind: "cpu", MappingID: 1, Per: "some"}
	assert.Error(t, s.Validate())
	s = &MetricSource{Name: "x", Kind: "cpu", MappingID: 1, Purpose: "some"}
	assert.Error(t, s.Validate())
}

// A Requests or Limits source takes a template of its own kind or any CPU / memory template of the same unit, whatever the template was filed under.
func TestARequestsSourceTakesATemplateOfTheSameUnit(t *testing.T) {
	assert.True(t, AcceptsMapping("k8s_cpu_requests", "k8s_cpu_requests"))
	assert.True(t, AcceptsMapping("k8s_cpu_requests", "cpu"), "a template of cores filed under CPU")
	assert.True(t, AcceptsMapping("k8s_mem_limits", "memory"))
	assert.False(t, AcceptsMapping("k8s_mem_limits", "cpu"), "cores are not bytes")
	assert.False(t, AcceptsMapping("k8s_cpu_limits", "cluster_cpu_used"), "a cluster metric is no module metric")
	assert.False(t, AcceptsMapping("cpu", "memory"))
	assert.False(t, AcceptsMapping("cpu", "k8s_cpu_requests"), "an algorithm source takes its own kind only")
}

func keysOf(list []*MetricSource) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Key
	}
	return out
}
