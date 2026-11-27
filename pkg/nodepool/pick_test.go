package nodepool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const gi = int64(1) << 30

func pool(id uint, name, kind string, cpuCores int64, memGiB int64, gpu int) *model.NodePool {
	return &model.NodePool{ID: id, Name: name, Kind: kind, Enabled: true, NodeCPUMilli: cpuCores * 1000, NodeMemoryBytes: memGiB * gi, NodeGPU: gpu,
		NodeSelector: map[string]string{"pool": name}}
}

func TestAPodThatIsMostlyMemoryIsGivenTheMemoryPool(t *testing.T) {
	pools := []*model.NodePool{pool(1, "cpu-8c16g", model.NodePoolCPU, 8, 16, 0), pool(2, "mem-4c32g", model.NodePoolMemory, 4, 32, 0)}
	got := Pick(pools, "", Demand{CPUMilli: 200, MemoryBytes: 4 * gi})
	require.NotNil(t, got.Pool, got.Reason)
	assert.Equal(t, "mem-4c32g", got.Pool.Name, got.Reason)
	assert.Equal(t, 8, got.Pool.PodsPerNode)
	assert.InDelta(t, 1.0, got.Pool.MemoryFill, 1e-9)
	assert.Len(t, got.Alternatives, 1)
	assert.Contains(t, got.Reason, "mem-4c32g")
}

func TestAPodThatIsMostlyCPUIsGivenTheCPUPool(t *testing.T) {
	pools := []*model.NodePool{pool(1, "cpu-8c16g", model.NodePoolCPU, 8, 16, 0), pool(2, "mem-4c32g", model.NodePoolMemory, 4, 32, 0)}
	got := Pick(pools, "", Demand{CPUMilli: 2000, MemoryBytes: 3 * gi})
	require.NotNil(t, got.Pool, got.Reason)
	assert.Equal(t, "cpu-8c16g", got.Pool.Name, got.Reason)
}

func TestOnlyAGPUPodIsGivenAGPUPoolAndAGPUPodNothingElse(t *testing.T) {
	pools := []*model.NodePool{pool(1, "cpu", model.NodePoolCPU, 8, 16, 0), pool(2, "gpu", model.NodePoolGPU, 16, 64, 4)}
	assert.Equal(t, "cpu", Pick(pools, "", Demand{CPUMilli: 1000, MemoryBytes: 2 * gi}).Pool.Name)
	got := Pick(pools, "", Demand{CPUMilli: 4000, MemoryBytes: 8 * gi, GPU: 1})
	require.NotNil(t, got.Pool)
	assert.Equal(t, "gpu", got.Pool.Name)
	assert.Equal(t, 4, got.Pool.PodsPerNode)
	assert.Nil(t, Pick([]*model.NodePool{pools[0]}, "", Demand{CPUMilli: 1000, MemoryBytes: gi, GPU: 1}).Pool, "no GPU pool: nothing")
}

func TestAPoolThatIsOffInAnotherClusterOrTooSmallIsLeftOut(t *testing.T) {
	off := pool(1, "off", model.NodePoolMemory, 4, 32, 0)
	off.Enabled = false
	elsewhere := pool(2, "elsewhere", model.NodePoolMemory, 4, 32, 0)
	elsewhere.Cluster = "other"
	small := pool(3, "small", model.NodePoolCPU, 2, 4, 0)
	ok := pool(4, "ok", model.NodePoolGeneral, 8, 32, 0)
	got := Pick([]*model.NodePool{off, elsewhere, small, ok}, "prod", Demand{CPUMilli: 3000, MemoryBytes: 8 * gi})
	require.NotNil(t, got.Pool, got.Reason)
	assert.Equal(t, "ok", got.Pool.Name)
	assert.Empty(t, got.Alternatives)
	none := Pick([]*model.NodePool{small}, "prod", Demand{CPUMilli: 3000, MemoryBytes: 8 * gi})
	assert.Nil(t, none.Pool)
	assert.Contains(t, none.Reason, "does not fit")
	assert.Contains(t, Pick(nil, "prod", Demand{}).Reason, "no CPU and memory")
}

func TestEqualFitsGoToTheHigherPriorityThenTheSmallerNode(t *testing.T) {
	a, b := pool(1, "a", model.NodePoolGeneral, 8, 32, 0), pool(2, "b", model.NodePoolGeneral, 8, 32, 0)
	b.Priority = 5
	assert.Equal(t, "b", Pick([]*model.NodePool{a, b}, "", Demand{CPUMilli: 1000, MemoryBytes: 4 * gi}).Pool.Name)
}

func TestANodePoolIsValidated(t *testing.T) {
	p := &model.NodePool{Name: "mem", Kind: model.NodePoolMemory, NodeSelector: map[string]string{"pool": "mem"}, NodeCPUMilli: 4000, NodeMemoryBytes: 32 * gi, Enabled: true}
	require.NoError(t, p.Validate())
	for name, mutate := range map[string]func(*model.NodePool){
		"no name":          func(p *model.NodePool) { p.Name = "" },
		"unknown kind":     func(p *model.NodePool) { p.Kind = "fast" },
		"no labels":        func(p *model.NodePool) { p.NodeSelector = nil },
		"bad label key":    func(p *model.NodePool) { p.NodeSelector = map[string]string{"-bad": "x"} },
		"bad label value":  func(p *model.NodePool) { p.NodeSelector = map[string]string{"pool": "has space"} },
		"no node size":     func(p *model.NodePool) { p.NodeCPUMilli = 0 },
		"gpu pool no gpus": func(p *model.NodePool) { p.Kind = model.NodePoolGPU },
		"gpus off a gpu":   func(p *model.NodePool) { p.NodeGPU = 2 },
	} {
		q := *p
		q.NodeSelector = map[string]string{"pool": "mem"}
		mutate(&q)
		assert.Error(t, q.Validate(), name)
	}
	ok := *p
	ok.NodeSelector = map[string]string{"node.kubernetes.io/pool": "mem-32g", "disk": ""}
	assert.NoError(t, ok.Validate())
}
