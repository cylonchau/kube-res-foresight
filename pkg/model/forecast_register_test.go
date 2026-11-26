package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestARegisteredModuleNeedsItsBoundsAndASourceBeforeItIsSwitchedOn(t *testing.T) {
	m := &ForecastModule{AutoRegistered: true, WorkloadKind: KindDeployment}
	assert.Equal(t, []string{BlockerMaxReplicas, BlockerSources}, m.MissingToEnable())
	m.MaxReplicas, m.MinReplicas = 5, 8
	assert.Equal(t, []string{BlockerReplicaSpan, BlockerSources}, m.MissingToEnable())
	m.MinReplicas = 2
	m.Sources = []*MetricSource{{Kind: "replicas", Enabled: true}, {Kind: "cpu", Enabled: false}}
	assert.Equal(t, []string{BlockerSources}, m.MissingToEnable(), "only a CPU or memory source that is on counts")
	m.Sources = append(m.Sources, &MetricSource{Kind: "memory", Enabled: true})
	assert.Empty(t, m.MissingToEnable())
	// a DaemonSet has no replicas to bound
	ds := &ForecastModule{AutoRegistered: true, WorkloadKind: KindDaemonSet, Sources: []*MetricSource{{Kind: "cpu", Enabled: true}}}
	assert.Empty(t, ds.MissingToEnable())
	// a module made by hand is never held back
	assert.Empty(t, (&ForecastModule{}).MissingToEnable())
}
