package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// The cluster and the nodes, as the Prometheus data source of the modules sees them now. Every figure is the answer of the data source to a query, and the
// query comes from, in this order: the metric mapping of the data source for that figure (the page "metric templates", kinds cluster_* and node_*), else
// the system parameter query.<name> when it was edited, else the default of the set the pages and the Grafana dashboard share (package resourcequery). The
// queries are shown with the figures, with where each came from, so they can be checked in Grafana, and a system whose metrics have other names writes its
// own once, in the mapping. None of it is stored; what is stored is what the operator reported (package inventorysvc).

// Where a query of the cluster set comes from.
const (
	QueryFromMapping   = "mapping"
	QueryFromParameter = "parameter"
	QueryFromDefault   = "default"
)

// The reasons the cluster could not be read at all (a figure that fails is told with the figure instead).
const (
	ClusterNoDatasource = "no_datasource"
	ClusterUnreachable  = "unreachable"
)

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
	// Queries are the PromQL that was sent, by what it asks for, with the value each answered and where its text came from.
	Queries []ClusterQuery `json:"queries"`
	Notes   []string       `json:"notes"`
	// Configured is true when at least one figure has a metric mapping of the data source.
	Configured bool `json:"configured"`
	// Missing are the metric kinds with no mapping in the data source: the figures that use the parameter or the default instead.
	Missing []string `json:"missing"`
	// Reason is set (and the figures are empty) when the cluster could not be read at all: no data source (ClusterNoDatasource) or one that cannot be
	// asked (ClusterUnreachable); Message says what to do.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// ClusterQuery is a query sent for the cluster.
type ClusterQuery struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Query string `json:"query"`
	// Source is QueryFromMapping, QueryFromParameter or QueryFromDefault; Mapping is the name of the metric mapping when it is the first.
	Source    string `json:"source"`
	Mapping   string `json:"mapping,omitempty"`
	MappingID uint   `json:"mapping_id,omitempty"`
	// Value is the answer of a figure of the cluster; the figures of the nodes have one for each node, see NodeUsage.
	Value *float64 `json:"value,omitempty"`
	Error string   `json:"error,omitempty"`
}

// Queries is the set of PromQL in force: the default of each, replaced by the parameter query.<name> when the parameter has a text.
func (s *Service) Queries(ctx context.Context) resourcequery.Set {
	if s.Config == nil {
		return resourcequery.Defaults()
	}
	return resourcequery.Load(ctx, s.Config)
}

// queryOrigin is where the text of one query comes from.
type queryOrigin struct {
	source    string
	mapping   string
	mappingID uint
}

// rangeText is what {{range}} of a mapping is filled with when the platform asks for a figure of the cluster (an instant query).
const rangeText = "5m"

// QueriesFor is the set with the queries of the figures of the cluster and of the nodes taken from the metric mappings of the data source (the data source of
// the cluster when datasourceID is 0): what the pages ask and what the Grafana dashboard is generated from. With no data source or no mapping it is the set of
// the parameters. The second result says where each query came from, the third which metric kinds have no mapping.
func (s *Service) QueriesFor(ctx context.Context, datasourceID uint) (resourcequery.Set, map[string]queryOrigin, []string) {
	set := s.Queries(ctx)
	origin := map[string]queryOrigin{}
	defaults := resourcequery.Defaults()
	for name, text := range set {
		if text == defaults[name] {
			origin[name] = queryOrigin{source: QueryFromDefault}
		} else {
			origin[name] = queryOrigin{source: QueryFromParameter}
		}
	}
	missing := []string{}
	byKind := map[string]*model.MetricMapping{}
	if s.Mappings != nil {
		if datasourceID == 0 {
			datasourceID, _ = s.ClusterDatasourceID(ctx, 0)
		}
		if datasourceID != 0 {
			if list, err := s.Mappings.Scoped(ctx, datasourceID); err == nil {
				for _, m := range list {
					byKind[m.Metric] = m // the newest of a kind wins
				}
			}
		}
	}
	for _, name := range resourcequery.MappedNames() {
		kind := resourcequery.MappingKind[name]
		m := byKind[kind]
		if m == nil {
			missing = append(missing, kind)
			continue
		}
		set[name] = strings.TrimSpace(strings.ReplaceAll(m.Query, "{{range}}", rangeText))
		origin[name] = queryOrigin{source: QueryFromMapping, mapping: m.Name, mappingID: m.ID}
	}
	return set, origin, missing
}

// ClusterDatasourceID is the data source the cluster and node figures are asked of: the system parameter cluster.datasource_id, else the data source that
// has the most metric mappings of the cluster and the nodes, else the data source of the metric mapping of the first enabled module that has a metric
// source (preferring the module asked for).
func (s *Service) ClusterDatasourceID(ctx context.Context, moduleID uint) (uint, error) {
	if id := uint(s.Count(ctx, timing.ClusterDatasource)); id != 0 {
		return id, nil
	}
	if s.Mappings != nil {
		if list, err := s.Mappings.Scoped(ctx, 0); err == nil && len(list) > 0 {
			counts := map[uint]int{}
			best := list[0].DatasourceID
			for _, m := range list {
				counts[m.DatasourceID]++
				if counts[m.DatasourceID] > counts[best] {
					best = m.DatasourceID
				}
			}
			return best, nil
		}
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

// The messages of a cluster that cannot be read; the page shows them with the way to set it up.
const (
	msgNoDatasource = "No data source is chosen for the cluster. Create the cluster and node metrics in \"Metric Templates\" (the cluster.* and node.* presets) on a data source, or set the system parameter cluster.datasource_id."
)

// ClusterCapacityOf asks the Prometheus data source about the cluster: the data source of the module when module_id is given and datasourceID is 0 (else the
// one the parameter or the first module names). A cluster that cannot be read at all is an answer with a reason, not an error: the page tells what to set up.
func (s *Service) ClusterCapacityOf(ctx context.Context, moduleID, datasourceID uint) (*ClusterCapacity, error) {
	if moduleID != 0 {
		if _, err := s.Modules.Get(ctx, moduleID); err != nil {
			return nil, err
		}
	}
	out := &ClusterCapacity{At: s.now(), Notes: []string{}, Queries: []ClusterQuery{}, Missing: []string{}}
	if datasourceID == 0 {
		id, err := s.ClusterDatasourceID(ctx, moduleID)
		if err != nil {
			if errors.Is(err, ErrCostNoDatasource) {
				out.Reason, out.Message = ClusterNoDatasource, msgNoDatasource
				return out, nil
			}
			return nil, err
		}
		datasourceID = id
	}
	ds, err := s.clusterQuerier(ctx, datasourceID)
	if err != nil {
		out.Reason, out.Message = ClusterUnreachable, err.Error()
		return out, nil
	}
	out.Datasource = CostSource{ID: ds.ID, Name: ds.Name}
	q, err := s.vectorQuerierOf(ctx, ds)
	if err != nil {
		out.Reason, out.Message = ClusterUnreachable, fmt.Sprintf("the data source %s cannot be asked: %v", ds.Name, err)
		return out, nil
	}
	set, origin, missing := s.QueriesFor(ctx, ds.ID)
	out.Missing = missing
	at := out.At
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
			cq := clusterQueryOf(name, text, origin[name])
			if err != nil {
				cq.Error = err.Error()
				out.Notes = append(out.Notes, name+": "+err.Error())
			} else {
				*into = got
				cq.Value = &got
			}
			out.Queries = append(out.Queries, cq)
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
	for _, cq := range out.Queries {
		if cq.Source == QueryFromMapping {
			out.Configured = true
		}
	}
	return out, nil
}

func clusterQueryOf(name, text string, from queryOrigin) ClusterQuery {
	cq := ClusterQuery{Name: name, Query: text, Kind: resourcequery.MappingKind[name], Source: from.source, Mapping: from.mapping, MappingID: from.mappingID}
	if q, ok := resourcequery.Find(name); ok {
		cq.Title = q.Title
	}
	if cq.Source == "" {
		cq.Source = QueryFromDefault
	}
	return cq
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
	// Reason and Message: see ClusterCapacity.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
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
	out := &NodeUsage{At: s.now(), Nodes: map[string]NodeFigures{}, Queries: []ClusterQuery{}, Notes: []string{}}
	if datasourceID == 0 {
		id, err := s.ClusterDatasourceID(ctx, 0)
		if err != nil {
			if errors.Is(err, ErrCostNoDatasource) {
				out.Reason, out.Message = ClusterNoDatasource, msgNoDatasource
				return out, nil
			}
			return nil, err
		}
		datasourceID = id
	}
	ds, err := s.clusterQuerier(ctx, datasourceID)
	if err != nil {
		out.Reason, out.Message = ClusterUnreachable, err.Error()
		return out, nil
	}
	out.Datasource = CostSource{ID: ds.ID, Name: ds.Name}
	q, err := s.vectorQuerierOf(ctx, ds)
	if err != nil {
		out.Reason, out.Message = ClusterUnreachable, fmt.Sprintf("the data source %s cannot be asked: %v", ds.Name, err)
		return out, nil
	}
	set, origin, _ := s.QueriesFor(ctx, ds.ID)
	at := out.At
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
			cq := clusterQueryOf(sp.name, text, origin[sp.name])
			defer func() { out.Queries = append(out.Queries, cq) }()
			if err != nil {
				cq.Error = err.Error()
				out.Notes = append(out.Notes, sp.name+": "+err.Error())
				return
			}
			if len(samples) == 0 {
				cq.Error = "the data source has no series for it"
				out.Notes = append(out.Notes, sp.name+": "+cq.Error)
				return
			}
			for _, sm := range samples {
				node := sm.Labels["node"]
				if node == "" {
					cq.Error = "a series has no node label: the query has to give one series for each node, with the label node"
					out.Notes = append(out.Notes, sp.name+": "+cq.Error)
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
