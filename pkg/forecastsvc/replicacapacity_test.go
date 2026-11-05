package forecastsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// A forecast of the memory of the workload gives a replica count too, from the memory one Pod is stated to have; a forecast of anything else
// does not, and a module that does not state the capacity of one Pod says so.
func TestAMemoryForecastGivesAReplicaCountFromThePodMemoryRequest(t *testing.T) {
	m := &model.ForecastModule{ReplicaMode: model.ReplicaModeDynamic, PodCPURequestMilli: 1000, PodMemoryRequestBytes: 8 << 30,
		TargetUtilization: 0.5, HeadroomRatio: 1, MinReplicas: 1, MaxReplicas: 50}

	memory, basis := replicaCapacity(m, "memory")
	require.NotNil(t, memory)
	assert.Contains(t, basis, "Pod memory request")
	assert.Equal(t, 10, memory(40*float64(1<<30)), "40 GiB over 8 GiB at 50% is 10 Pods")
	assert.Equal(t, 1, memory(1), "at least one")

	cpu, _ := replicaCapacity(m, "cpu")
	require.NotNil(t, cpu)
	assert.Equal(t, 4, cpu(2), "ceil(2 cores / (1 core x 50%))")

	none, why := replicaCapacity(m, "rps")
	assert.Nil(t, none)
	assert.Contains(t, why, "only a CPU or memory forecast")

	m.PodMemoryRequestBytes = 0
	none, why = replicaCapacity(m, "memory")
	assert.Nil(t, none)
	assert.Contains(t, why, "does not state the memory capacity of one Pod")
}
