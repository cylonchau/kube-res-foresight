package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// The cluster and the nodes, as the Prometheus data source of the modules sees them now (P10 A3). Every figure is the answer of the data source to a query of the
// set the pages and the Grafana dashboard share (package resourcequery, the parameters query.*): the queries are shown with the figures, so they can be
// checked in Grafana, and a system whose metrics have other names writes its own in the parameter. None of it is stored; what is stored is what the operator
// reported (package inventorysvc).

// ClusterCapacity is what the cluster has to give, has given out and uses right now. A figure the data source has no series for is left out with a note.
type ClusterCapacity struct {
	Datasource CostSource `json:"datasource"`
	At         time.Time  `json:"at"`
	Nodes      int        `json:"nodes"`
	NodesReady int        `json:"nodes_ready"`
	Pods       int        `json:"pods"`
	// AllocatableCPU is in cores, AllocatableMemory in bytes; RequestedCPU and RequestedMemory are the sums of the requests of the Pods.
	AllocatableCPU    float64 `json:"allocatable_cpu"`
	AllocatableMemory float64 `json:"allocatable_memory"`
	RequestedCPU      float64 `json:"requested_cpu"`
	RequestedMemory   float64 `json:"requested_memory"`
	// UsedCPU is in cores and UsedMemory in bytes: what all the containers use now; 0 with a note when the data source has no such series.
	UsedCPU    float64 `json:"used_cpu"`
	UsedMemory float64 `json:"used_memory"`
	// Queries are the PromQL that was sent, by what it asks for.
	Queries []ClusterQuery `json:"queries"`
	Notes   []string       `json:"notes"`
}

// ClusterQuery is a query sent for the cluster.
type ClusterQuery struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// Queries is the set of PromQL in force: the default of each, replaced by the parameter query.<name> when the parameter has a text.
func (s *Service) Queries(ctx context.Context) resourcequery.Set {
	if s.Config == nil {
		return resourcequery.Defaults()
	}
	return resourcequery.Load(ctx, s.Config)
}

// ClusterDatasourceID is the data source the cluster and node figures are asked of: the system parameter cluster.datasource_id, else the data source of the
// metric mapping of the first enabled module that has a metric source (preferring the module asked for).
func (s *Service) ClusterDatasourceID(ctx context.Context, moduleID uint) (uint, error) {
	if id := uint(s.Count(ctx, timing.ClusterDatasource)); id != 0 {
		return id, nil
	}
	ids := []uint{}
	if moduleID != 0 {
		ids = append(ids, moduleID)
	}
	modules, err := s.Modules.ListEnabled(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range modules {
		if m.ID != moduleID {
			ids = append(ids, m.ID)
		}
	}
	for _, id := range ids {
		sources, err := s.Modules.Sources(ctx, id)
		if err != nil {
			return 0, err
		}
		for _, src := range sources {
			if src.Enabled && src.MappingID != 0 {
				if mp, err := s.Mappings.GetByID(ctx, src.MappingID); err == nil && mp.DatasourceID != 0 {
					return mp.DatasourceID, nil
				}
			}
		}
	}
	return 0, ErrCostNoDatasource
}

func (s *Service) clusterQuerier(ctx context.Context, datasourceID uint) (*model.Datasource, error) {
	ds, err := s.Datasources.GetByID(ctx, datasourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("the data source %d does not exist", datasourceID)
		}
		return nil, err
	}
	return ds, nil
}

// ClusterCapacityOf asks the Prometheus data source about the cluster: the data source of the module when module_id is given and datasourceID is 0 (else the
// one the parameter or the first module names).
func (s *Service) ClusterCapacityOf(ctx context.Context, moduleID, datasourceID uint) (*ClusterCapacity, error) {
	if moduleID != 0 {
		if _, err := s.Modules.Get(ctx, moduleID); err != nil {
			return nil, err
		}
	}
	if datasourceID == 0 {
		id, err := s.ClusterDatasourceID(ctx, moduleID)
		if err != nil {
			return nil, err
		}
		datasourceID = id
	}
	ds, err := s.clusterQuerier(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	q, err := s.vectorQuerierOf(ctx, ds)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCostCluster, err)
	}
	set := s.Queries(ctx)
	at := s.now()
	out := &ClusterCapacity{Datasource: CostSource{ID: ds.ID, Name: ds.Name}, At: at, Notes: []string{}, Queries: []ClusterQuery{}}
	var nodes, ready, pods float64
	// the figures are independent: they are asked together, and the gate of the data source keeps their number in bounds
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(name string, into *float64) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := resourcequery.Expand(set[name], nil)
			got, err := scalarOf(ctx, q, text, at)
			mu.Lock()
			defer mu.Unlock()
			out.Queries = append(out.Queries, ClusterQuery{Name: name, Query: text})
			if err != nil {
				out.Notes = append(out.Notes, name+": "+err.Error())
				return
			}
			*into = got
		}()
	}
	run("cluster_nodes", &nodes)
	run("cluster_nodes_ready", &ready)
	run("cluster_pods", &pods)
	run("cluster_allocatable_cpu", &out.AllocatableCPU)
	run("cluster_allocatable_memory", &out.AllocatableMemory)
	run("cluster_requests_cpu", &out.RequestedCPU)
	run("cluster_requests_memory", &out.RequestedMemory)
	run("cluster_used_cpu", &out.UsedCPU)
	run("cluster_used_memory", &out.UsedMemory)
	wg.Wait()
	out.Nodes, out.NodesReady, out.Pods = int(math.Round(nodes)), int(math.Round(ready)), int(math.Round(pods))
	sort.Slice(out.Queries, func(i, j int) bool { return queryOrder(out.Queries[i].Name) < queryOrder(out.Queries[j].Name) })
	sort.Strings(out.Notes)
	return out, nil
}

func queryOrder(name string) int {
	for i, q := range resourcequery.Queries {
		if q.Name == name {
			return i
		}
	}
	return len(resourcequery.Queries)
}

// NodeUsage is what each node uses and has been asked for right now, as the data source answers: every Pod on the node, not only the Pods of the modules.
type NodeUsage struct {
	Datasource CostSource `json:"datasource"`
	At         time.Time  `json:"at"`
	// Nodes is by node name.
	Nodes   map[string]NodeFigures `json:"nodes"`
	Queries []ClusterQuery         `json:"queries"`
	Notes   []string               `json:"notes"`
}

// NodeFigures are the figures of one node: CPU in cores and memory in bytes.
type NodeFigures struct {
	CPUUsed           *float64 `json:"cpu_used"`
	MemoryUsed        *float64 `json:"memory_used"`
	CPURequested      *float64 `json:"cpu_requested"`
	MemoryRequested   *float64 `json:"memory_requested"`
	CPUAllocatable    *float64 `json:"cpu_allocatable"`
	MemoryAllocatable *float64 `json:"memory_allocatable"`
	Ready             *bool    `json:"ready"`
}

// NodeUsageOf asks the data source for the figures of every node: each query gives a series for each node, with the label node.
func (s *Service) NodeUsageOf(ctx context.Context, datasourceID uint) (*NodeUsage, error) {
	if datasourceID == 0 {
		id, err := s.ClusterDatasourceID(ctx, 0)
		if err != nil {
			return nil, err
		}
		datasourceID = id
	}
	ds, err := s.clusterQuerier(ctx, datasourceID)
	if err != nil {
		return nil, err
	}
	q, err := s.vectorQuerierOf(ctx, ds)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCostCluster, err)
	}
	set := s.Queries(ctx)
	at := s.now()
	out := &NodeUsage{Datasource: CostSource{ID: ds.ID, Name: ds.Name}, At: at, Nodes: map[string]NodeFigures{}, Queries: []ClusterQuery{}, Notes: []string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	type spec struct {
		name string
		put  func(f *NodeFigures, v float64)
	}
	specs := []spec{
		{"node_cpu_usage", func(f *NodeFigures, v float64) { f.CPUUsed = &v }},
		{"node_memory_usage", func(f *NodeFigures, v float64) { f.MemoryUsed = &v }},
		{"node_cpu_requests", func(f *NodeFigures, v float64) { f.CPURequested = &v }},
		{"node_memory_requests", func(f *NodeFigures, v float64) { f.MemoryRequested = &v }},
		{"node_cpu_allocatable", func(f *NodeFigures, v float64) { f.CPUAllocatable = &v }},
		{"node_memory_allocatable", func(f *NodeFigures, v float64) { f.MemoryAllocatable = &v }},
		{"node_ready", func(f *NodeFigures, v float64) { ok := v >= 1; f.Ready = &ok }},
	}
	for _, sp := range specs {
		wg.Add(1)
		go func(sp spec) {
			defer wg.Done()
			text := resourcequery.Expand(set[sp.name], nil)
			samples, err := q.QueryVector(ctx, text, at)
			mu.Lock()
			defer mu.Unlock()
			out.Queries = append(out.Queries, ClusterQuery{Name: sp.name, Query: text})
			if err != nil {
				out.Notes = append(out.Notes, sp.name+": "+err.Error())
				return
			}
			if len(samples) == 0 {
				out.Notes = append(out.Notes, sp.name+": the data source has no series for it")
				return
			}
			for _, sm := range samples {
				node := sm.Labels["node"]
				if node == "" {
					out.Notes = append(out.Notes, sp.name+": a series has no node label: the query has to give one series for each node, with the label node")
					return
				}
				if math.IsNaN(sm.Value) || math.IsInf(sm.Value, 0) {
					continue
				}
				f := out.Nodes[node]
				sp.put(&f, sm.Value)
				out.Nodes[node] = f
			}
		}(sp)
	}
	wg.Wait()
	sort.Slice(out.Queries, func(i, j int) bool { return queryOrder(out.Queries[i].Name) < queryOrder(out.Queries[j].Name) })
	sort.Strings(out.Notes)
	return out, nil
}
