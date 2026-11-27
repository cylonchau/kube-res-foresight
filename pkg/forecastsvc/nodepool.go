package forecastsvc

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/nodepool"
)

// chooseNodePool is the node pool the Pod of a decision fits best (see package nodepool), nil for a module that does not use pools. The Pod is the CPU and memory that were
// decided; when the decision gave none (a module that keeps what it has) it is the Pod the module states.
func (s *Service) chooseNodePool(ctx context.Context, m *model.ForecastModule, rec decision.Recommendation) *nodepool.Choice {
	if !m.NodePoolEnabled || s.NodePools == nil {
		return nil
	}
	d := nodepool.Demand{}
	if rec.CPUMilli.Set {
		d.CPUMilli = rec.CPUMilli.Value
	} else {
		d.CPUMilli = m.PodCPURequestMilli
	}
	if rec.MemoryBytes.Set {
		d.MemoryBytes = rec.MemoryBytes.Value
	} else {
		d.MemoryBytes = m.PodMemoryRequestBytes
	}
	if rec.GPU.Set && rec.GPU.Value > 0 {
		d.GPU = int(rec.GPU.Value)
	}
	pools, err := s.NodePools.Enabled(ctx)
	if err != nil {
		return &nodepool.Choice{Reason: "the node pools could not be read: " + err.Error()}
	}
	choice := nodepool.Pick(pools, m.Cluster, d)
	return &choice
}
