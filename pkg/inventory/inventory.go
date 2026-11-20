// Package inventory is the wire format of what the operator reads from the cluster, read-only, and reports to the platform (P10 A1), and the pure parts that
// both sides use: the cloud provider of a node, the one-line text of a quantity, the difference between two readings of a workload, and the hash that tells
// the operator whether anything that matters has changed. It holds no database and no Kubernetes client.
package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Limits of one report: a report that is bigger is refused, not cut, so the operator finds out.
const (
	MaxNodes      = 5000
	MaxWorkloads  = 2000
	MaxPods       = 500
	MaxContainers = 64
)

// Resources are CPU in millicores and memory in bytes; 0 is "not set".
type Resources struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// Capacity is what a node has (or what it lets Pods have, for the allocatable).
type Capacity struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
	Pods        int64 `json:"pods"`
}

// Node is one node of the cluster. Pods, Requests and Limits are the sums over the Pods of the configured modules that run on it.
type Node struct {
	Name           string    `json:"name"`
	Ready          bool      `json:"ready"`
	Unschedulable  bool      `json:"unschedulable"`
	Pressure       []string  `json:"pressure,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Region         string    `json:"region,omitempty"`
	Zone           string    `json:"zone,omitempty"`
	InstanceType   string    `json:"instance_type,omitempty"`
	KubeletVersion string    `json:"kubelet_version,omitempty"`
	Capacity       Capacity  `json:"capacity"`
	Allocatable    Capacity  `json:"allocatable"`
	Pods           int       `json:"pods"`
	Requests       Resources `json:"requests"`
	Limits         Resources `json:"limits"`
}

// Container is one container of the Pod template of a workload.
type Container struct {
	Name     string    `json:"name"`
	Requests Resources `json:"requests"`
	Limits   Resources `json:"limits"`
}

// Pod is one Pod of a workload.
type Pod struct {
	Name      string     `json:"name"`
	Node      string     `json:"node,omitempty"`
	Phase     string     `json:"phase"`
	Ready     bool       `json:"ready"`
	Restarts  int        `json:"restarts"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// HPA is the object that scales the workload from outside, as the operator reads it (the platform's operator is the only controller of the replicas of a
// module in auto mode, so what is here is what it must not fight with): a HorizontalPodAutoscaler or a KEDA ScaledObject.
type HPA struct {
	Kind     string `json:"kind"` // hpa or keda
	Name     string `json:"name"`
	Min      int32  `json:"min"`
	Max      int32  `json:"max"`
	Current  int32  `json:"current"`
	Desired  int32  `json:"desired"`
	Behavior string `json:"behavior,omitempty"`
}

// Workload is the workload of one module.
type Workload struct {
	Module     string      `json:"module"`
	Namespace  string      `json:"namespace"`
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Desired    int32       `json:"desired"`
	Ready      int32       `json:"ready"`
	Available  int32       `json:"available"`
	Updated    int32       `json:"updated"`
	Generation int64       `json:"generation"`
	Containers []Container `json:"containers"`
	Pods       []Pod       `json:"pods"`
	HPA        *HPA        `json:"hpa,omitempty"`
	// Missing is true when the workload is not in the cluster; Error says why it could not be read (no permission, a failed call).
	Missing bool   `json:"missing,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Snapshot is one report: the nodes and the workloads of the configured modules at one moment. RecordID is made by the operator when it queues the report
// and the platform keeps one report for each.
type Snapshot struct {
	RecordID   string     `json:"record_id"`
	Operator   string     `json:"operator,omitempty"`
	ObservedAt time.Time  `json:"observed_at"`
	Nodes      []Node     `json:"nodes"`
	NodesError string     `json:"nodes_error,omitempty"`
	Workloads  []Workload `json:"workloads"`
}

// Target is a module the operator is asked to read: every module of the platform that is enabled, whatever its mode.
type Target struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Kind      string `json:"workload_kind"`
	Workload  string `json:"workload"`
}

// Providers by the scheme of the providerID of a node.
var providers = map[string]string{
	"aws": "aws", "gce": "gcp", "azure": "azure", "aliyun": "alibaba", "alicloud": "alibaba", "tencentcloud": "tencent", "qcloud": "tencent",
	"huaweicloud": "huawei", "volcengine": "volcengine", "openstack": "openstack", "vsphere": "vsphere", "digitalocean": "digitalocean", "kind": "kind", "k3s": "k3s",
	"baremetal": "baremetal", "ibmcloud": "ibm", "ocid": "oracle", "linode": "linode",
}

// ProviderOf is the cloud provider a node's providerID names ("aws:///us-east-1a/i-0abc" is aws), the scheme as it is when it is not known, and empty when
// the node has no providerID.
func ProviderOf(providerID string) string {
	id := strings.TrimSpace(providerID)
	if id == "" {
		return ""
	}
	scheme, _, found := strings.Cut(id, "://")
	if !found {
		return ""
	}
	scheme = strings.ToLower(scheme)
	if p, ok := providers[scheme]; ok {
		return p
	}
	return scheme
}

// CPU writes millicores the way a manifest does: 500m, or 2 for whole cores; 0 is "-".
func CPU(milli int64) string {
	switch {
	case milli <= 0:
		return "-"
	case milli%1000 == 0:
		return fmt.Sprintf("%d", milli/1000)
	}
	return fmt.Sprintf("%dm", milli)
}

// Memory writes bytes with the binary unit a manifest uses: 512Mi, 1.5Gi; 0 is "-".
func Memory(bytes int64) string {
	const (
		ki = int64(1) << 10
		mi = int64(1) << 20
		gi = int64(1) << 30
	)
	switch {
	case bytes <= 0:
		return "-"
	case bytes >= gi:
		return trim(float64(bytes)/float64(gi)) + "Gi"
	case bytes >= mi:
		return trim(float64(bytes)/float64(mi)) + "Mi"
	case bytes >= ki:
		return trim(float64(bytes)/float64(ki)) + "Ki"
	}
	return fmt.Sprintf("%d", bytes)
}

func trim(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// Change is one difference between two readings of a workload.
type Change struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Diff lists what is different between two readings of the same workload: the replicas, the requests and limits of every container (a container that came or
// went too), and the bounds of the HPA. Which Pods ran, how many were ready and how often they restarted are not configuration, and are not in it.
func Diff(before, after Workload) []Change {
	var out []Change
	add := func(path, from, to string) {
		if from != to {
			out = append(out, Change{path, from, to})
		}
	}
	add("replicas", fmt.Sprint(before.Desired), fmt.Sprint(after.Desired))
	old := map[string]Container{}
	for _, c := range before.Containers {
		old[c.Name] = c
	}
	seen := map[string]bool{}
	for _, c := range after.Containers {
		seen[c.Name] = true
		prev, existed := old[c.Name]
		if !existed {
			out = append(out, Change{"containers/" + c.Name, "-", "added"})
		}
		add("containers/"+c.Name+"/requests.cpu", CPU(prev.Requests.CPUMilli), CPU(c.Requests.CPUMilli))
		add("containers/"+c.Name+"/requests.memory", Memory(prev.Requests.MemoryBytes), Memory(c.Requests.MemoryBytes))
		add("containers/"+c.Name+"/limits.cpu", CPU(prev.Limits.CPUMilli), CPU(c.Limits.CPUMilli))
		add("containers/"+c.Name+"/limits.memory", Memory(prev.Limits.MemoryBytes), Memory(c.Limits.MemoryBytes))
	}
	names := make([]string, 0, len(old))
	for name := range old {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, Change{"containers/" + name, "present", "removed"})
	}
	bounds := func(h *HPA) (string, string, string) {
		if h == nil {
			return "none", "-", "-"
		}
		return h.Kind + "/" + h.Name, fmt.Sprint(h.Min), fmt.Sprint(h.Max)
	}
	bk, bmin, bmax := bounds(before.HPA)
	ak, amin, amax := bounds(after.HPA)
	add("hpa", bk, ak)
	add("hpa.min", bmin, amin)
	add("hpa.max", bmax, amax)
	return out
}

// Summary is a list of changes as one line.
func Summary(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s %s → %s", c.Path, c.From, c.To))
	}
	s := strings.Join(parts, "; ")
	if len(s) > 480 {
		s = s[:480] + "…"
	}
	return s
}

// Fingerprint is the hash of the part of a snapshot that is configuration: the nodes (ready, schedulable, pressure, capacity, allocatable) and the replicas,
// containers and HPA of the workloads. It does not change when a Pod restarts or the ready count moves, so the operator reports at once when something
// that matters changed and otherwise once an hour.
func Fingerprint(s Snapshot) string {
	type node struct {
		Name                 string
		Ready, Unsch         bool
		Pressure             []string
		Cap, Alloc           Capacity
		Provider, Zone, Type string
	}
	type workload struct {
		Module     string
		Desired    int32
		Containers []Container
		HPA        *HPA
		Missing    bool
	}
	nodes := make([]node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		nodes = append(nodes, node{n.Name, n.Ready, n.Unschedulable, n.Pressure, n.Capacity, n.Allocatable, n.Provider, n.Zone, n.InstanceType})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	workloads := make([]workload, 0, len(s.Workloads))
	for _, w := range s.Workloads {
		var h *HPA
		if w.HPA != nil {
			cp := *w.HPA
			cp.Current, cp.Desired = 0, 0 // what the HPA does is not its configuration
			h = &cp
		}
		workloads = append(workloads, workload{w.Module, w.Desired, w.Containers, h, w.Missing})
	}
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].Module < workloads[j].Module })
	raw, _ := json.Marshal(struct {
		Nodes     []node
		Workloads []workload
	}{nodes, workloads})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}

// ErrInvalid is what the answer of Validate wraps: a report that is too big or names nothing, which sending it again cannot mend.
var ErrInvalid = errors.New("the report is not valid")

// Validate refuses a report that is too big or that names nothing.
func (s Snapshot) Validate() error {
	switch {
	case strings.TrimSpace(s.RecordID) == "":
		return fmt.Errorf("%w: record_id is required: a report is stored once for each id", ErrInvalid)
	case len(s.Nodes) > MaxNodes:
		return fmt.Errorf("%w: a report holds at most %d nodes", ErrInvalid, MaxNodes)
	case len(s.Workloads) > MaxWorkloads:
		return fmt.Errorf("%w: a report holds at most %d workloads", ErrInvalid, MaxWorkloads)
	}
	for _, w := range s.Workloads {
		if strings.TrimSpace(w.Module) == "" {
			return fmt.Errorf("%w: a workload has no module", ErrInvalid)
		}
		if len(w.Pods) > MaxPods {
			return fmt.Errorf("%w: the workload of module %s has %d Pods: at most %d are reported", ErrInvalid, w.Module, len(w.Pods), MaxPods)
		}
		if len(w.Containers) > MaxContainers {
			return fmt.Errorf("%w: the workload of module %s has %d containers: at most %d are reported", ErrInvalid, w.Module, len(w.Containers), MaxContainers)
		}
	}
	return nil
}
