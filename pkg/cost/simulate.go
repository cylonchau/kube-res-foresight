package cost

import (
	"math"
	"sort"
)

// What the simulation does not model (codes, the page words them). Each of them can only make a real consolidation harder, so a node the
// simulation empties may still be kept by the cluster; and the simulation packs greedily, so it may also leave a node that a better
// packing would empty. The result is an estimate in neither direction, not a bound.
var Unmodeled = []string{
	"node_selector_and_affinity", // nodeSelector, node affinity and Pod (anti-)affinity
	"topology_spread",            // topology spread constraints and zones
	"disruption_budgets",         // PodDisruptionBudgets and the time Pods need to move
	"tolerations",                // a Pod moves only to a node whose taints are among those of its own node: its real tolerations are not known
	"storage_binding",            // volumes bound to a node or a zone
	"pod_overhead_and_init",      // init containers and Pod overhead in the requests
	"provisioner_replacement",    // Karpenter or the cluster autoscaler may replace a node by a cheaper one instead of removing it
}

// Change is the request a workload would have for each of its Pods instead of the one it has. A figure of zero or less keeps the request
// a Pod has, and a figure above the request of a Pod is ignored: a change only lowers a request, so what is simulated is a saving.
type Change struct {
	Namespace   string
	Kind        string
	Workload    string
	CPUMilli    int64
	MemoryBytes int64
}

// Why a node stays (codes).
const (
	BlockedUncontrolled = "pod_without_controller" // a Pod nobody controls would be lost, not moved
	BlockedNoRoom       = "no_room"                // a Pod fits on none of the other nodes
)

// Blocked is a node that could not be emptied and why.
type Blocked struct {
	Node   string `json:"node"`
	Reason string `json:"reason"`
	// Pod is the Pod that blocked it, as namespace/name.
	Pod string `json:"pod,omitempty"`
}

// Saving is what the emptied nodes would cost, in one currency.
type Saving struct {
	Currency string  `json:"currency"`
	Nodes    int     `json:"nodes"`
	Hourly   float64 `json:"hourly"`
	Monthly  float64 `json:"monthly"`
}

// SimResult is what a what-if run found.
type SimResult struct {
	NodesBefore int `json:"nodes_before"`
	NodesAfter  int `json:"nodes_after"`
	// PodsChanged is how many Pods had their request changed.
	PodsChanged int       `json:"pods_changed"`
	Released    []string  `json:"released"`
	Blocked     []Blocked `json:"blocked"`
	Savings     []Saving  `json:"savings"`
	// UnpricedReleased is how many emptied nodes no rule prices: they are in Released but in no Saving.
	UnpricedReleased int      `json:"unpriced_released"`
	Unmodeled        []string `json:"unmodeled"`
}

type simNode struct {
	Node
	freeCPU  int64
	freeMem  int64
	freePods int64 // -1 when no limit is known
	pods     []int
	taints   map[string]bool
	gone     bool
}

func (n *simNode) fits(p Pod) bool {
	return n.freeCPU >= p.CPUMilli && n.freeMem >= p.MemoryBytes && (n.freePods < 0 || n.freePods >= 1)
}

func (n *simNode) take(p Pod) {
	n.freeCPU -= p.CPUMilli
	n.freeMem -= p.MemoryBytes
	if n.freePods >= 0 {
		n.freePods--
	}
}

func (n *simNode) give(p Pod) {
	n.freeCPU += p.CPUMilli
	n.freeMem += p.MemoryBytes
	if n.freePods >= 0 {
		n.freePods++
	}
}

func taintSet(ts []Taint) map[string]bool {
	out := map[string]bool{}
	for _, t := range ts {
		if t.Effect == "NoSchedule" || t.Effect == "NoExecute" {
			out[t.Key+"="+t.Value+":"+t.Effect] = true
		}
	}
	return out
}

// canMove says whether a Pod of the origin node may go to the target: only when every taint that keeps Pods off the target is one the
// origin node has too, because then the Pod already lives with it. The Pod's tolerations are not known, so this is the only safe reading.
func canMove(origin, target *simNode) bool {
	for t := range target.taints {
		if !origin.taints[t] {
			return false
		}
	}
	return true
}

// Simulate asks, for the requests the changes give, how many nodes could be emptied: it takes the least used node first and tries to
// place every Pod of it (largest first, on the node it fits tightest) on the nodes that stay; a node whose Pods all find a place is
// removed, the places they took are kept, and the next node is tried. DaemonSet Pods go with their node. It reads the snapshot and
// writes nothing; the snapshot passed in is not changed.
func Simulate(s Snapshot, changes []Change, rules []Rule) SimResult {
	res := SimResult{Released: []string{}, Blocked: []Blocked{}, Savings: []Saving{}, Unmodeled: Unmodeled, NodesBefore: len(s.Nodes)}

	wanted := map[string]Change{}
	for _, c := range changes {
		wanted[WorkloadKey(c.Namespace, c.Kind, c.Workload)] = c
	}
	pods := make([]Pod, len(s.Pods))
	copy(pods, s.Pods)
	for i := range pods {
		if c, ok := wanted[pods[i].Key()]; ok && pods[i].Workload != "" {
			changed := false
			if c.CPUMilli > 0 && c.CPUMilli < pods[i].CPUMilli {
				pods[i].CPUMilli, changed = c.CPUMilli, true
			}
			if c.MemoryBytes > 0 && c.MemoryBytes < pods[i].MemoryBytes {
				pods[i].MemoryBytes, changed = c.MemoryBytes, true
			}
			if changed {
				res.PodsChanged++
			}
		}
	}

	nodes := make([]*simNode, 0, len(s.Nodes))
	index := map[string]*simNode{}
	for _, n := range s.Nodes {
		sn := &simNode{Node: n, freeCPU: n.CPUMilli, freeMem: n.MemoryBytes, freePods: -1, taints: taintSet(n.Taints)}
		if n.Pods > 0 {
			sn.freePods = n.Pods
		}
		nodes = append(nodes, sn)
		index[n.Name] = sn
	}
	for i, p := range pods {
		if n := index[p.Node]; n != nil {
			n.pods = append(n.pods, i)
			n.take(p)
		}
	}

	// the least used first: it is the one that is easiest to empty
	order := make([]*simNode, len(nodes))
	copy(order, nodes)
	used := func(n *simNode) float64 {
		if n.CPUMilli <= 0 || n.MemoryBytes <= 0 {
			return 1
		}
		return math.Max(float64(n.CPUMilli-n.freeCPU)/float64(n.CPUMilli), float64(n.MemoryBytes-n.freeMem)/float64(n.MemoryBytes))
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := used(order[i]), used(order[j])
		if a != b {
			return a < b
		}
		return order[i].Name < order[j].Name
	})

	active := len(nodes)
	for _, x := range order {
		if active <= 1 {
			break
		}
		blocked, pod := tryEmpty(x, nodes, pods)
		if blocked != "" {
			res.Blocked = append(res.Blocked, Blocked{Node: x.Name, Reason: blocked, Pod: pod})
			continue
		}
		x.gone = true
		active--
		res.Released = append(res.Released, x.Name)
	}
	res.NodesAfter = active

	// a node that stayed and was never tried is simply not listed as blocked: it is the one the loop stopped before
	byCurrency := map[string]*Saving{}
	for _, name := range res.Released {
		n := index[name]
		rule, ok := PriceOf(n.Node, rules)
		if !ok {
			res.UnpricedReleased++
			continue
		}
		sv := byCurrency[rule.Currency]
		if sv == nil {
			sv = &Saving{Currency: rule.Currency}
			byCurrency[rule.Currency] = sv
		}
		sv.Nodes++
		sv.Hourly += rule.HourlyPrice
	}
	codes := make([]string, 0, len(byCurrency))
	for code := range byCurrency {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		sv := byCurrency[code]
		sv.Monthly = sv.Hourly * HoursPerMonth
		res.Savings = append(res.Savings, *sv)
	}
	return res
}

// tryEmpty moves the Pods of x onto the other nodes that are still there. It either places them all and leaves the places taken, or
// places none (it undoes what it took) and says why. A DaemonSet Pod is not moved: it ends with its node.
func tryEmpty(x *simNode, nodes []*simNode, pods []Pod) (reason, pod string) {
	var movable []int
	for _, i := range x.pods {
		p := pods[i]
		if p.DaemonSet {
			continue
		}
		if p.Workload == "" {
			return BlockedUncontrolled, p.Namespace + "/" + p.Name
		}
		movable = append(movable, i)
	}
	sort.SliceStable(movable, func(a, b int) bool {
		pa, pb := pods[movable[a]], pods[movable[b]]
		if pa.CPUMilli != pb.CPUMilli {
			return pa.CPUMilli > pb.CPUMilli
		}
		return pa.MemoryBytes > pb.MemoryBytes
	})

	type placed struct {
		pod    Pod
		target *simNode
	}
	var done []placed
	undo := func() {
		for k := len(done) - 1; k >= 0; k-- {
			done[k].target.give(done[k].pod)
		}
	}
	for _, i := range movable {
		p := pods[i]
		var best *simNode
		bestLeft := math.MaxFloat64
		for _, t := range nodes {
			if t == x || t.gone || !t.fits(p) || !canMove(x, t) {
				continue
			}
			left := 0.0
			if t.CPUMilli > 0 {
				left += float64(t.freeCPU-p.CPUMilli) / float64(t.CPUMilli)
			}
			if t.MemoryBytes > 0 {
				left += float64(t.freeMem-p.MemoryBytes) / float64(t.MemoryBytes)
			}
			if left < bestLeft {
				best, bestLeft = t, left
			}
		}
		if best == nil {
			undo()
			return BlockedNoRoom, p.Namespace + "/" + p.Name
		}
		best.take(p)
		done = append(done, placed{p, best})
	}
	return "", ""
}
