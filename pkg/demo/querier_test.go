package demo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignalOfRecognisesPresetSeriesAndRefusesOthers(t *testing.T) {
	for query, want := range map[string]Signal{
		`sum(rate(container_cpu_usage_seconds_total{a="b"}[1m]))`:                        SignalCPU,
		`sum(container_memory_working_set_bytes{a="b"})`:                                 SignalMemory,
		`sum(rate(istio_requests_total{a="b"}[1m]))`:                                     SignalRPS,
		`sum(kube_deployment_status_replicas_available{a="b"})`:                          SignalReplicas,
		`histogram_quantile(0.95, rate(istio_request_duration_milliseconds_bucket[1m]))`: SignalLatency,
	} {
		got, ok := SignalOf(query)
		require.True(t, ok, query)
		assert.Equal(t, want, got, query)
	}
	_, ok := SignalOf(`up`)
	assert.False(t, ok)
	_, err := Querier{}.QueryRange(context.Background(), `up`, time.Now(), time.Now().Add(time.Hour), time.Minute)
	assert.Error(t, err, "the demo datasource does not pretend to answer arbitrary queries")
}

func TestTheArtificialWorkloadIsDeterministicAndSelfConsistent(t *testing.T) {
	ts := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC) // a Wednesday afternoon
	assert.Equal(t, RPS(ts, 7), RPS(ts, 7), "no randomness")
	weekend := time.Date(2026, 3, 7, 15, 0, 0, 0, time.UTC)
	assert.Less(t, RPS(weekend, 0), RPS(ts, 0), "weekends are quieter")

	// pods run at their target utilization when serving their capacity: the capacity estimate relies on it
	rps := Value(SignalRPS, ts, 0)
	cpu := Value(SignalCPU, ts, 0)
	replicas := Value(SignalReplicas, ts, 0)
	assert.GreaterOrEqual(t, replicas*PodCapacityRPS, rps, "enough pods for the load")
	assert.InDelta(t, rps/PodCapacityRPS*PodCPUCores*TargetUtil, cpu, 1e-9)
}
