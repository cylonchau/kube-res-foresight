// Package nodepool recommends the node pool a Pod fits best. It is arithmetic on the size of a Pod and the size of a node, with no state: the same pools and the same Pod give the same
// answer, and the answer says how it was worked out.
package nodepool

import (
	"fmt"
	"sort"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Demand is what one Pod asks of a node: the CPU and memory Requests, and the GPUs.
type Demand struct {
	CPUMilli    int64
	MemoryBytes int64
	GPU         int
}

// Fit is how a Pod fills the nodes of one pool: how many Pods one node holds, and how much of its CPU and memory (and GPUs) they take.
type Fit struct {
	PoolID       uint              `json:"pool_id"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	NodeSelector map[string]string `json:"node_selector"`
	PodsPerNode  int               `json:"pods_per_node"`
	CPUFill      float64           `json:"cpu_fill"`
	MemoryFill   float64           `json:"memory_fill"`
	GPUFill      float64           `json:"gpu_fill,omitempty"`
	// Efficiency is the mean of the shares of the node the Pods take: 1 leaves nothing of a node unused, a pool of the wrong shape for the Pod leaves one of the two nearly empty.
	Efficiency float64 `json:"efficiency"`
}

// Choice is the answer: the pool recommended (Pool is nil when none fits), the reason in words, and the pools that fitted less well.
type Choice struct {
	Pool         *Fit   `json:"pool,omitempty"`
	Reason       string `json:"reason"`
	Alternatives []Fit  `json:"alternatives,omitempty"`
}

func gib(b int64) float64 { return float64(b) / (1 << 30) }

// Pick chooses the pool of the cluster whose nodes the Pod of the demand fills best. A pool that is switched off, in another cluster, or whose node is smaller than the Pod is left out; a
// GPU pool is considered only for a Pod that asks for GPUs, and such a Pod is only given a GPU pool. Among the rest the pool with the highest efficiency wins (a Pod of 200m and 4 GiB
// leaves a 4-core 32-GiB node full on memory and a 8-core 16-GiB node nearly empty on CPU, so the first), then the higher priority, then the smaller node.
func Pick(pools []*model.NodePool, cluster string, d Demand) Choice {
	if d.CPUMilli <= 0 || d.MemoryBytes <= 0 {
		return Choice{Reason: "the recommendation has no CPU and memory for the Pod, so no pool can be fitted"}
	}
	type scored struct {
		fit  Fit
		prio int
		size int64
	}
	var fits []scored
	candidates := 0
	for _, p := range pools {
		if !p.Enabled || (p.Cluster != "" && p.Cluster != cluster) {
			continue
		}
		if (d.GPU > 0) != (p.Kind == model.NodePoolGPU) {
			continue
		}
		candidates++
		if p.NodeCPUMilli <= 0 || p.NodeMemoryBytes <= 0 || d.CPUMilli > p.NodeCPUMilli || d.MemoryBytes > p.NodeMemoryBytes || (d.GPU > 0 && d.GPU > p.NodeGPU) {
			continue
		}
		pods := min(p.NodeCPUMilli/d.CPUMilli, p.NodeMemoryBytes/d.MemoryBytes)
		if d.GPU > 0 {
			pods = min(pods, int64(p.NodeGPU/d.GPU))
		}
		f := Fit{PoolID: p.ID, Name: p.Name, Kind: p.Kind, NodeSelector: p.NodeSelector, PodsPerNode: int(pods)}
		f.CPUFill = float64(pods*d.CPUMilli) / float64(p.NodeCPUMilli)
		f.MemoryFill = float64(pods*d.MemoryBytes) / float64(p.NodeMemoryBytes)
		f.Efficiency = (f.CPUFill + f.MemoryFill) / 2
		if d.GPU > 0 {
			f.GPUFill = float64(pods*int64(d.GPU)) / float64(p.NodeGPU)
			f.Efficiency = (f.CPUFill + f.MemoryFill + f.GPUFill) / 3
		}
		fits = append(fits, scored{f, p.Priority, p.NodeCPUMilli * (p.NodeMemoryBytes >> 20)})
	}
	pod := fmt.Sprintf("a Pod of %dm CPU and %.2f GiB", d.CPUMilli, gib(d.MemoryBytes))
	if d.GPU > 0 {
		pod += fmt.Sprintf(" and %d GPU", d.GPU)
	}
	if len(fits) == 0 {
		switch {
		case candidates == 0 && d.GPU > 0:
			return Choice{Reason: pod + ": no GPU pool is switched on for this cluster"}
		case candidates == 0:
			return Choice{Reason: pod + ": no pool is switched on for this cluster"}
		}
		return Choice{Reason: pod + ": it does not fit a node of any pool"}
	}
	sort.SliceStable(fits, func(i, j int) bool {
		a, b := fits[i], fits[j]
		if d := a.fit.Efficiency - b.fit.Efficiency; d > 1e-9 || d < -1e-9 {
			return a.fit.Efficiency > b.fit.Efficiency
		}
		if a.prio != b.prio {
			return a.prio > b.prio
		}
		return a.size < b.size
	})
	best := fits[0].fit
	out := Choice{Pool: &best}
	out.Reason = fmt.Sprintf("%s (%.1f GiB per core): the pool %q (%s) holds %d of them on a node and fills %.0f%% of its CPU and %.0f%% of its memory", pod,
		gib(d.MemoryBytes)/(float64(d.CPUMilli)/1000), best.Name, best.Kind, best.PodsPerNode, best.CPUFill*100, best.MemoryFill*100)
	for _, s := range fits[1:] {
		out.Alternatives = append(out.Alternatives, s.fit)
	}
	return out
}
