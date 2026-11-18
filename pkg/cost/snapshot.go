package cost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
)

// Querier answers an instant vector query; the Prometheus client does, and a test replaces it.
type Querier interface {
	QueryVector(ctx context.Context, query string, at time.Time) ([]promclient.VectorSample, error)
}

// The queries of a snapshot, all of kube-state-metrics. The names follow kube-state-metrics v2; they are NOT verified against a running
// installation (none was available), and an installation that exports fewer labels (node labels are only exported when allow-listed)
// gives a snapshot with notes instead of a failure.
const (
	QueryAllocatable = `kube_node_status_allocatable{resource=~"cpu|memory|pods"}`
	QueryTaints      = `kube_node_spec_taint`
	QueryNodeLabels  = `kube_node_labels`
	QueryPods        = `kube_pod_info * on(namespace, pod) group_left() (kube_pod_status_phase{phase="Running"} == 1)`
	QueryRequests    = `sum by (namespace, pod, resource) (kube_pod_container_resource_requests{resource=~"cpu|memory"})`
	QueryOwners      = `kube_pod_owner{owner_is_controller="true"}`
)

// Notes of a snapshot (codes).
const (
	SnapshotNoNodeLabels = "node_labels_not_exported" // a price rule by label cannot match: allow-list the labels in kube-state-metrics, or match by name
)

var unsafeLabel = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeLabelKey writes a node label key the way kube-state-metrics exports it (every character that is not a letter, a digit or an
// underscore becomes an underscore), so that a rule written with the real key matches the exported one.
func SanitizeLabelKey(key string) string { return unsafeLabel.ReplaceAllString(key, "_") }

// Collect reads the cluster from kube-state-metrics as it is at the given moment.
func Collect(ctx context.Context, q Querier, at time.Time) (Snapshot, error) {
	snap := Snapshot{At: at.UTC()}
	read := func(query string) ([]promclient.VectorSample, error) {
		v, err := q.QueryVector(ctx, query, at)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		return v, nil
	}

	alloc, err := read(QueryAllocatable)
	if err != nil {
		return snap, err
	}
	nodes := map[string]*Node{}
	for _, s := range alloc {
		name := s.Labels["node"]
		if name == "" || math.IsNaN(s.Value) || s.Value < 0 {
			continue
		}
		n := nodes[name]
		if n == nil {
			n = &Node{Name: name}
			nodes[name] = n
		}
		switch s.Labels["resource"] {
		case "cpu":
			n.CPUMilli = int64(math.Round(s.Value * 1000))
		case "memory":
			n.MemoryBytes = int64(math.Round(s.Value))
		case "pods":
			n.Pods = int64(math.Round(s.Value))
		}
	}
	if len(nodes) == 0 {
		return snap, errors.New("kube-state-metrics returned no node (kube_node_status_allocatable): is it scraped by this data source?")
	}

	taints, err := read(QueryTaints)
	if err != nil {
		return snap, err
	}
	for _, s := range taints {
		if n := nodes[s.Labels["node"]]; n != nil {
			n.Taints = append(n.Taints, Taint{Key: s.Labels["key"], Value: s.Labels["value"], Effect: s.Labels["effect"]})
		}
	}

	labels, err := read(QueryNodeLabels)
	if err != nil {
		return snap, err
	}
	exported := false
	for _, s := range labels {
		n := nodes[s.Labels["node"]]
		if n == nil {
			continue
		}
		for k, v := range s.Labels {
			if strings.HasPrefix(k, "label_") {
				if n.Labels == nil {
					n.Labels = map[string]string{}
				}
				n.Labels[strings.TrimPrefix(k, "label_")] = v
				exported = true
			}
		}
	}
	if !exported {
		snap.Notes = append(snap.Notes, SnapshotNoNodeLabels)
	}

	podInfo, err := read(QueryPods)
	if err != nil {
		return snap, err
	}
	reqs, err := read(QueryRequests)
	if err != nil {
		return snap, err
	}
	owners, err := read(QueryOwners)
	if err != nil {
		return snap, err
	}
	type pk struct{ ns, name string }
	cpu, mem := map[pk]int64{}, map[pk]int64{}
	for _, s := range reqs {
		if math.IsNaN(s.Value) || s.Value < 0 {
			continue
		}
		k := pk{s.Labels["namespace"], s.Labels["pod"]}
		switch s.Labels["resource"] {
		case "cpu":
			cpu[k] = int64(math.Round(s.Value * 1000))
		case "memory":
			mem[k] = int64(math.Round(s.Value))
		}
	}
	type owner struct{ kind, name string }
	ownerOf := map[pk]owner{}
	for _, s := range owners {
		ownerOf[pk{s.Labels["namespace"], s.Labels["pod"]}] = owner{s.Labels["owner_kind"], s.Labels["owner_name"]}
	}
	seen := map[pk]bool{}
	for _, s := range podInfo {
		k := pk{s.Labels["namespace"], s.Labels["pod"]}
		node := s.Labels["node"]
		if node == "" || k.name == "" || seen[k] {
			continue
		}
		seen[k] = true
		p := Pod{Namespace: k.ns, Name: k.name, Node: node, CPUMilli: cpu[k], MemoryBytes: mem[k]}
		if o, ok := ownerOf[k]; ok {
			p.Kind, p.Workload = workloadOf(o.kind, o.name)
			p.DaemonSet = o.kind == "DaemonSet"
		}
		snap.Pods = append(snap.Pods, p)
	}

	snap.Nodes = make([]Node, 0, len(nodes))
	for _, n := range nodes {
		snap.Nodes = append(snap.Nodes, *n)
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	sort.Slice(snap.Pods, func(i, j int) bool {
		a, b := snap.Pods[i], snap.Pods[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return snap, nil
}

// workloadOf names the workload a controller owner stands for. A ReplicaSet is taken for the Deployment that made it, by dropping the
// hash its name ends with (a ReplicaSet nobody owns is then read as a Deployment of that name, which is as close as the metric lets us
// get); every other owner is the workload itself.
func workloadOf(kind, name string) (string, string) {
	if kind == "ReplicaSet" {
		if i := strings.LastIndex(name, "-"); i > 0 {
			return "Deployment", name[:i]
		}
	}
	return kind, name
}
