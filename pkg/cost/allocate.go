package cost

import (
	"math"
	"sort"
)

// DefaultCPUShare is the part of the price of a node that is put on its CPU, the rest on its memory. It is an assumption, not a
// measurement: nothing in the cluster says how a node price splits between the two, and an administrator is expected to set it to the
// ratio of the instance prices of the provider. The platform parameter cost.cpu_share_percent carries it.
const DefaultCPUShare = 0.5

// Notes a node can carry (codes, the page words them).
const (
	NoteNoAllocatable = "no_allocatable"        // the node has no CPU or memory allocatable figure, so nothing can be shared out
	NoteOvercommitted = "requests_exceed_node"  // the Pods request more than the node can give: the shares are scaled down to the price
	NoteUnknownNode   = "pods_on_unknown_nodes" // Pods run on nodes the snapshot has no figure for
	NoteUnpricedNodes = "nodes_without_a_price" // no price rule matches some nodes
)

// NodeCost is what one node costs an hour and how much of it the requests of its Pods take.
type NodeCost struct {
	Node     string  `json:"node"`
	Priced   bool    `json:"priced"`
	Rule     string  `json:"rule,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Hourly   float64 `json:"hourly"`
	// Allocated is the part of the price the requests of the Pods take, Idle the rest. They add up to Hourly.
	Allocated          float64 `json:"allocated"`
	Idle               float64 `json:"idle"`
	Pods               int     `json:"pods"`
	CPURequestMilli    int64   `json:"cpu_request_milli"`
	MemoryRequestBytes int64   `json:"memory_request_bytes"`
	CPUMilli           int64   `json:"cpu_milli"`
	MemoryBytes        int64   `json:"memory_bytes"`
	Note               string  `json:"note,omitempty"`
}

// WorkloadCost is what the requests of one workload cost an hour.
type WorkloadCost struct {
	Namespace   string  `json:"namespace"`
	Kind        string  `json:"kind"`
	Workload    string  `json:"workload"`
	Pods        int     `json:"pods"`
	CPUMilli    int64   `json:"cpu_milli"`
	MemoryBytes int64   `json:"memory_bytes"`
	Hourly      float64 `json:"hourly"`
}

// CurrencyCost is the cost in one currency: amounts of two currencies are never added, so each has its own block.
type CurrencyCost struct {
	Currency string  `json:"currency"`
	Hourly   float64 `json:"hourly"`
	// Allocated is what the workloads take of Hourly and Idle what no request takes; Allocated + Idle = Hourly.
	Allocated float64 `json:"allocated"`
	Idle      float64 `json:"idle"`
	IdleShare float64 `json:"idle_share"`
	Monthly   float64 `json:"monthly"`
	// CPUCoreHour and MemoryGiBHour are the average price of a core-hour and of a GiB-hour over the priced nodes of this currency: what
	// a request costs, used to put a price on a surplus.
	CPUCoreHour   float64        `json:"cpu_core_hour"`
	MemoryGiBHour float64        `json:"memory_gib_hour"`
	Nodes         []NodeCost     `json:"nodes"`
	Workloads     []WorkloadCost `json:"workloads"`
}

// Allocation is the cost of a snapshot.
type Allocation struct {
	Currencies []*CurrencyCost `json:"currencies"`
	// Unpriced are the nodes no rule prices. They are listed, never counted as free.
	Unpriced []NodeCost `json:"unpriced"`
	Notes    []string   `json:"notes,omitempty"`
}

// Allocate shares the price of every priced node among the Pods on it by what they request: the CPU part of the price by CPU requests
// over the CPU the node can give, the memory part by memory. What no request takes is the idle cost of the node. For every currency the
// workload costs and the idle cost add up to the node prices (a test holds it), because a shared-out price that does not add up cannot
// be trusted.
func Allocate(s Snapshot, rules []Rule, cpuShare float64) Allocation {
	if math.IsNaN(cpuShare) || cpuShare < 0 || cpuShare > 1 {
		cpuShare = DefaultCPUShare
	}
	nodeOf := make(map[string]Node, len(s.Nodes))
	for _, n := range s.Nodes {
		nodeOf[n.Name] = n
	}
	podsOf := make(map[string][]Pod, len(s.Nodes))
	unknown := 0
	for _, p := range s.Pods {
		if _, ok := nodeOf[p.Node]; !ok {
			unknown++
			continue
		}
		podsOf[p.Node] = append(podsOf[p.Node], p)
	}

	out := Allocation{Currencies: []*CurrencyCost{}, Unpriced: []NodeCost{}}
	byCurrency := map[string]*CurrencyCost{}
	workloads := map[string]map[string]*WorkloadCost{} // currency -> workload key -> cost
	// the sums that make the unit prices
	cpuPriced, cpuCores := map[string]float64{}, map[string]float64{}
	memPriced, memGiB := map[string]float64{}, map[string]float64{}

	for _, n := range s.Nodes {
		pods := podsOf[n.Name]
		nc := NodeCost{Node: n.Name, Pods: len(pods), CPUMilli: n.CPUMilli, MemoryBytes: n.MemoryBytes}
		for _, p := range pods {
			nc.CPURequestMilli += p.CPUMilli
			nc.MemoryRequestBytes += p.MemoryBytes
		}
		rule, ok := PriceOf(n, rules)
		if !ok {
			out.Unpriced = append(out.Unpriced, nc)
			continue
		}
		nc.Priced, nc.Rule, nc.Currency, nc.Hourly = true, rule.Name, rule.Currency, rule.HourlyPrice
		cc := byCurrency[rule.Currency]
		if cc == nil {
			cc = &CurrencyCost{Currency: rule.Currency, Nodes: []NodeCost{}, Workloads: []WorkloadCost{}}
			byCurrency[rule.Currency] = cc
			workloads[rule.Currency] = map[string]*WorkloadCost{}
		}
		cc.Hourly += nc.Hourly
		if n.CPUMilli <= 0 || n.MemoryBytes <= 0 {
			nc.Note, nc.Idle = NoteNoAllocatable, nc.Hourly
			cc.Idle += nc.Idle
			cc.Nodes = append(cc.Nodes, nc)
			continue
		}
		cpuPrice := nc.Hourly * cpuShare / float64(n.CPUMilli)
		memPrice := nc.Hourly * (1 - cpuShare) / float64(n.MemoryBytes)
		cpuPriced[rule.Currency] += nc.Hourly * cpuShare
		cpuCores[rule.Currency] += float64(n.CPUMilli) / 1000
		memPriced[rule.Currency] += nc.Hourly * (1 - cpuShare)
		memGiB[rule.Currency] += float64(n.MemoryBytes) / (1 << 30)

		taken := cpuPrice*float64(nc.CPURequestMilli) + memPrice*float64(nc.MemoryRequestBytes)
		scale := 1.0
		if taken > nc.Hourly {
			scale = nc.Hourly / taken
			nc.Note = NoteOvercommitted
		}
		for _, p := range pods {
			w := workloads[rule.Currency][p.Key()]
			if w == nil {
				w = &WorkloadCost{Namespace: p.Namespace, Kind: kindOf(p), Workload: nameOf(p)}
				workloads[rule.Currency][p.Key()] = w
			}
			w.Pods++
			w.CPUMilli += p.CPUMilli
			w.MemoryBytes += p.MemoryBytes
			w.Hourly += (cpuPrice*float64(p.CPUMilli) + memPrice*float64(p.MemoryBytes)) * scale
		}
		nc.Allocated = taken * scale
		nc.Idle = nc.Hourly - nc.Allocated
		cc.Allocated += nc.Allocated
		cc.Idle += nc.Idle
		cc.Nodes = append(cc.Nodes, nc)
	}

	codes := make([]string, 0, len(byCurrency))
	for code := range byCurrency {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		cc := byCurrency[code]
		cc.Monthly = cc.Hourly * HoursPerMonth
		if cc.Hourly > 0 {
			cc.IdleShare = cc.Idle / cc.Hourly
		}
		if cpuCores[code] > 0 {
			cc.CPUCoreHour = cpuPriced[code] / cpuCores[code]
		}
		if memGiB[code] > 0 {
			cc.MemoryGiBHour = memPriced[code] / memGiB[code]
		}
		sort.Slice(cc.Nodes, func(i, j int) bool { return cc.Nodes[i].Node < cc.Nodes[j].Node })
		for _, w := range workloads[code] {
			cc.Workloads = append(cc.Workloads, *w)
		}
		sort.Slice(cc.Workloads, func(i, j int) bool {
			a, b := cc.Workloads[i], cc.Workloads[j]
			if a.Hourly != b.Hourly {
				return a.Hourly > b.Hourly
			}
			return WorkloadKey(a.Namespace, a.Kind, a.Workload) < WorkloadKey(b.Namespace, b.Kind, b.Workload)
		})
		out.Currencies = append(out.Currencies, cc)
	}
	sort.Slice(out.Unpriced, func(i, j int) bool { return out.Unpriced[i].Node < out.Unpriced[j].Node })
	if len(out.Unpriced) > 0 {
		out.Notes = append(out.Notes, NoteUnpricedNodes)
	}
	if unknown > 0 {
		out.Notes = append(out.Notes, NoteUnknownNode)
	}
	return out
}

func kindOf(p Pod) string {
	if p.Workload == "" {
		return "Pod"
	}
	return p.Kind
}

func nameOf(p Pod) string {
	if p.Workload == "" {
		return p.Name
	}
	return p.Workload
}
