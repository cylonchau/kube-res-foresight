// Package cost is the cost side of the platform: what a node costs, what the requests of each workload cost of it, what is idle, what the
// requests of a workload could be lowered to, and how many nodes could be emptied by it. Every answer is a pure function of a snapshot of
// the cluster and of the price rules, so it can be reproduced and tested; the snapshot is read from Prometheus (kube-state-metrics) by
// Collect and nothing here writes to the cluster.
//
// Money here is a figure of the price rules an administrator typed in. It is an estimate of what the nodes cost, not what a bill says, and
// a saving is only "estimated" until the node count has really gone down (see requirements/references/cost-closure-p9.md).
package cost

import "time"

// HoursPerMonth turns an hourly price into a monthly one (24 x 365 / 12). It is an assumption of the page, said there.
const HoursPerMonth = 730.0

// Taint is a node taint; only NoSchedule and NoExecute keep a Pod away.
type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// Node is one node of the snapshot. The allocatable figures are what the scheduler can hand out; Pods 0 means no limit is known.
type Node struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Taints      []Taint           `json:"taints,omitempty"`
	CPUMilli    int64             `json:"cpu_milli"`
	MemoryBytes int64             `json:"memory_bytes"`
	Pods        int64             `json:"pods"`
}

// Pod is one running Pod: where it runs, what it requests (the sum over its containers) and which workload owns it.
type Pod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Kind and Workload name the controller workload; both are empty for a Pod nobody controls (it cannot be moved).
	Kind        string `json:"kind,omitempty"`
	Workload    string `json:"workload,omitempty"`
	Node        string `json:"node"`
	CPUMilli    int64  `json:"cpu_milli"`
	MemoryBytes int64  `json:"memory_bytes"`
	DaemonSet   bool   `json:"daemon_set,omitempty"`
}

// Snapshot is the cluster at one moment.
type Snapshot struct {
	At    time.Time `json:"at"`
	Nodes []Node    `json:"nodes"`
	Pods  []Pod     `json:"pods"`
	// Notes say what the snapshot could not see (for example that node labels were not exported).
	Notes []string `json:"notes,omitempty"`
}

// WorkloadKey names the workload of a Pod, the way a module names its workload.
func WorkloadKey(namespace, kind, workload string) string {
	return namespace + "/" + kind + "/" + workload
}

// Key is the workload key of the Pod; a Pod with no controller is its own workload.
func (p Pod) Key() string {
	if p.Workload == "" {
		return WorkloadKey(p.Namespace, "Pod", p.Name)
	}
	return WorkloadKey(p.Namespace, p.Kind, p.Workload)
}
