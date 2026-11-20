// Package resourcequery holds the one set of PromQL that the resource pages and the Grafana dashboard share (P10 A3, A4): the numbers of the cluster, the nodes,
// a workload, its Pods and Containers and its JVM. Each query is a system parameter (query.<name>), so a system whose metrics have other names edits it in
// one place and the page and the dashboard change together; what is written here is only the default, and it is not claimed to fit any system. A query
// is written the way a Grafana panel is, with the variables $cluster $namespace $workload $pod $node $container: Grafana fills them from its
// variables, and the platform fills them with ".*" (every one) when it asks for the figure itself.
package resourcequery

import (
	"context"
	"sort"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Query is one query of the set.
type Query struct {
	// Name is the key under query.: the parameter is query.<Name>.
	Name string
	// Group is the row of the dashboard it belongs to.
	Group string
	// Title is what the panel and the parameter are called.
	Title string
	// Unit is the Grafana unit of the figure (percentunit, bytes, short, cores, s, reqps...).
	Unit string
	// Default is the query when the parameter is empty.
	Default string
	// Legend is the legend of a series of a graph ({{node}}, {{pod}}); empty for a single figure.
	Legend string
}

// The groups, in the order of the dashboard.
const (
	GroupCluster   = "cluster"
	GroupNode      = "node"
	GroupWorkload  = "workload"
	GroupPod       = "pod"
	GroupContainer = "container"
	GroupJVM       = "jvm"
	// GroupVariable is not a row of the dashboard: these are the queries that fill its variables (the namespaces, the workloads, the Pods, the nodes).
	GroupVariable = "variable"
)

// Groups lists the groups of the rows of the dashboard in order (the variables are not a row).
var Groups = []string{GroupCluster, GroupNode, GroupWorkload, GroupPod, GroupContainer, GroupJVM}

const (
	podOfWorkload = `namespace=~"$namespace",pod=~"$workload-.*"`
	workloadScope = `namespace=~"$namespace",pod=~"$workload-.*",container!="",container!="POD"`
)

// Queries is the set, in the order the dashboard shows it. The metric names in the defaults are the common ones of cAdvisor, the object-state metrics of
// Kubernetes and Micrometer; a system that exports others edits the parameter.
var Queries = []Query{
	// the cluster
	{"cluster_nodes", GroupCluster, "Nodes", "short", `count(kube_node_info)`, ""},
	{"cluster_nodes_ready", GroupCluster, "Nodes ready", "short", `sum(kube_node_status_condition{condition="Ready",status="true"})`, ""},
	{"cluster_pods", GroupCluster, "Pods running", "short", `sum(kube_pod_status_phase{phase="Running"})`, ""},
	{"cluster_allocatable_cpu", GroupCluster, "CPU allocatable (cores)", "short", `sum(kube_node_status_allocatable{resource="cpu"})`, ""},
	{"cluster_allocatable_memory", GroupCluster, "Memory allocatable", "bytes", `sum(kube_node_status_allocatable{resource="memory"})`, ""},
	{"cluster_requests_cpu", GroupCluster, "CPU requested (cores)", "short", `sum(kube_pod_container_resource_requests{resource="cpu"})`, ""},
	{"cluster_requests_memory", GroupCluster, "Memory requested", "bytes", `sum(kube_pod_container_resource_requests{resource="memory"})`, ""},
	{"cluster_used_cpu", GroupCluster, "CPU used (cores)", "short", `sum(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]))`, ""},
	{"cluster_used_memory", GroupCluster, "Memory used (working set)", "bytes", `sum(container_memory_working_set_bytes{container!="",container!="POD"})`, ""},

	// the nodes: each answers one series for each node, with the label node
	{"node_cpu_usage", GroupNode, "Node CPU used (cores)", "short", `sum by (node) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]))`, "{{node}}"},
	{"node_memory_usage", GroupNode, "Node memory used (working set)", "bytes", `sum by (node) (container_memory_working_set_bytes{container!="",container!="POD"})`, "{{node}}"},
	{"node_cpu_allocatable", GroupNode, "Node CPU allocatable (cores)", "short", `sum by (node) (kube_node_status_allocatable{resource="cpu"})`, "{{node}}"},
	{"node_memory_allocatable", GroupNode, "Node memory allocatable", "bytes", `sum by (node) (kube_node_status_allocatable{resource="memory"})`, "{{node}}"},
	{"node_cpu_requests", GroupNode, "Node CPU requested (cores)", "short", `sum by (node) (kube_pod_container_resource_requests{resource="cpu",node=~"$node"})`, "{{node}}"},
	{"node_memory_requests", GroupNode, "Node memory requested", "bytes", `sum by (node) (kube_pod_container_resource_requests{resource="memory",node=~"$node"})`, "{{node}}"},
	{"node_ready", GroupNode, "Node ready", "short", `kube_node_status_condition{condition="Ready",status="true",node=~"$node"}`, "{{node}}"},

	// a workload, by namespace and workload name
	{"workload_cpu_usage", GroupWorkload, "Workload CPU used (cores)", "short", `sum(rate(container_cpu_usage_seconds_total{` + workloadScope + `}[5m]))`, ""},
	{"workload_memory_usage", GroupWorkload, "Workload memory used (working set)", "bytes", `sum(container_memory_working_set_bytes{` + workloadScope + `})`, ""},
	{"workload_cpu_requests", GroupWorkload, "Workload CPU requests (cores)", "short", `sum(kube_pod_container_resource_requests{resource="cpu",` + podOfWorkload + `})`, ""},
	{"workload_cpu_limits", GroupWorkload, "Workload CPU limits (cores)", "short", `sum(kube_pod_container_resource_limits{resource="cpu",` + podOfWorkload + `})`, ""},
	{"workload_memory_requests", GroupWorkload, "Workload memory requests", "bytes", `sum(kube_pod_container_resource_requests{resource="memory",` + podOfWorkload + `})`, ""},
	{"workload_memory_limits", GroupWorkload, "Workload memory limits", "bytes", `sum(kube_pod_container_resource_limits{resource="memory",` + podOfWorkload + `})`, ""},
	{"workload_replicas", GroupWorkload, "Workload replicas ready", "short", `count(kube_pod_status_ready{condition="true",` + podOfWorkload + `} == 1)`, ""},
	{"workload_cpu_throttle", GroupWorkload, "Workload CPU throttled (s/s)", "s", `sum(rate(container_cpu_cfs_throttled_seconds_total{` + workloadScope + `}[5m]))`, ""},
	{"workload_oom", GroupWorkload, "Workload OOM kills", "short", `sum(kube_pod_container_status_last_terminated_reason{reason="OOMKilled",` + podOfWorkload + `})`, ""},

	// the Pods of a workload: a series for each Pod
	{"pod_cpu_usage", GroupPod, "Pod CPU used (cores)", "short", `sum by (pod) (rate(container_cpu_usage_seconds_total{` + workloadScope + `}[5m]))`, "{{pod}}"},
	{"pod_memory_usage", GroupPod, "Pod memory used (working set)", "bytes", `sum by (pod) (container_memory_working_set_bytes{` + workloadScope + `})`, "{{pod}}"},
	{"pod_restarts", GroupPod, "Pod restarts", "short", `sum by (pod) (kube_pod_container_status_restarts_total{` + podOfWorkload + `})`, "{{pod}}"},

	// the containers of a workload: a series for each container
	{"container_cpu_usage", GroupContainer, "Container CPU used (cores)", "short", `sum by (container) (rate(container_cpu_usage_seconds_total{` + workloadScope + `}[5m]))`, "{{container}}"},
	{"container_memory_usage", GroupContainer, "Container memory used (working set)", "bytes", `sum by (container) (container_memory_working_set_bytes{` + workloadScope + `})`, "{{container}}"},
	{"container_memory_limit", GroupContainer, "Container memory limit", "bytes", `sum by (container) (kube_pod_container_resource_limits{resource="memory",` + podOfWorkload + `})`, "{{container}}"},

	// the lists the variables of the dashboard offer
	{"var_namespace", GroupVariable, "Variable: namespaces", "", `label_values(kube_pod_info, namespace)`, ""},
	{"var_workload", GroupVariable, "Variable: workloads", "", `label_values(kube_deployment_labels{namespace=~"$namespace"}, deployment)`, ""},
	{"var_pod", GroupVariable, "Variable: Pods", "", `label_values(kube_pod_info{namespace=~"$namespace",pod=~"$workload-.*"}, pod)`, ""},
	{"var_node", GroupVariable, "Variable: nodes", "", `label_values(kube_node_info, node)`, ""},
	{"var_container", GroupVariable, "Variable: containers", "", `label_values(container_memory_working_set_bytes{namespace=~"$namespace",pod=~"$workload-.*",container!=""}, container)`, ""},

	// the JVM of a workload
	{"jvm_heap_used", GroupJVM, "JVM heap used", "bytes", `sum(jvm_memory_used_bytes{area="heap",` + podOfWorkload + `})`, ""},
	{"jvm_heap_committed", GroupJVM, "JVM heap committed", "bytes", `sum(jvm_memory_committed_bytes{area="heap",` + podOfWorkload + `})`, ""},
	{"jvm_heap_max", GroupJVM, "JVM heap max", "bytes", `sum(jvm_memory_max_bytes{area="heap",` + podOfWorkload + `})`, ""},
	{"jvm_nonheap_used", GroupJVM, "JVM non-heap used", "bytes", `sum(jvm_memory_used_bytes{area="nonheap",` + podOfWorkload + `})`, ""},
	{"jvm_nonheap_committed", GroupJVM, "JVM non-heap committed", "bytes", `sum(jvm_memory_committed_bytes{area="nonheap",` + podOfWorkload + `})`, ""},
	{"jvm_nonheap_max", GroupJVM, "JVM non-heap max", "bytes", `sum(jvm_memory_max_bytes{area="nonheap",` + podOfWorkload + `})`, ""},
	{"jvm_gc_count", GroupJVM, "JVM GC collections (per s)", "short", `sum(rate(jvm_gc_pause_seconds_count{` + podOfWorkload + `}[5m]))`, ""},
	{"jvm_gc_pause", GroupJVM, "JVM GC pause time (s/s)", "s", `sum(rate(jvm_gc_pause_seconds_sum{` + podOfWorkload + `}[5m]))`, ""},
	{"jvm_threads", GroupJVM, "JVM live threads", "short", `sum(jvm_threads_live_threads{` + podOfWorkload + `})`, ""},
}

// Key is the system parameter of a query.
func Key(name string) string { return "query." + name }

// Find is the query of a name.
func Find(name string) (Query, bool) {
	for _, q := range Queries {
		if q.Name == name {
			return q, true
		}
	}
	return Query{}, false
}

// Reader reads a system parameter; ok is false when there is none.
type Reader interface {
	GetValueOrDefault(ctx context.Context, key, fallback string) (string, error)
}

// Set is the queries in force: the default of each, replaced by the parameter when the parameter has a text.
type Set map[string]string

// Load reads every query from the system parameters.
func Load(ctx context.Context, r Reader) Set {
	out := Set{}
	for _, q := range Queries {
		text := q.Default
		if r != nil {
			if v, err := r.GetValueOrDefault(ctx, Key(q.Name), ""); err == nil && strings.TrimSpace(v) != "" {
				text = strings.TrimSpace(v)
			}
		}
		out[q.Name] = text
	}
	return out
}

// Defaults is the set with every query at its default.
func Defaults() Set { return Load(context.Background(), nil) }

// Variables are the Grafana variables of a query.
var Variables = []string{"cluster", "namespace", "workload", "pod", "node", "container"}

// Expand fills the variables of a query: the value given, or ".*" for every one that is not given (a regular expression that takes all, which is what a
// Grafana variable with "include all" is made to). A variable is written $name or ${name}.
func Expand(query string, values map[string]string) string {
	out := query
	// the longest names first, so that $pod is not taken for the start of $podname
	names := append([]string(nil), Variables...)
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, name := range names {
		v, ok := values[name]
		if !ok || v == "" {
			v = ".*"
		}
		out = strings.ReplaceAll(out, "${"+name+"}", v)
		out = strings.ReplaceAll(out, "$"+name, v)
	}
	return out
}

// Used lists the variables a query uses.
func Used(query string) []string {
	var out []string
	for _, name := range Variables {
		if strings.Contains(query, "$"+name) || strings.Contains(query, "${"+name+"}") {
			out = append(out, name)
		}
	}
	return out
}

// Configs are the parameters of the queries for the system parameter seed.
func Configs() []model.SysConfig {
	out := make([]model.SysConfig, 0, len(Queries))
	for _, q := range Queries {
		out = append(out, model.SysConfig{ConfigKey: Key(q.Name), ConfigValue: q.Default, ConfigName: "PromQL: " + q.Title,
			Remark: "The query for \"" + q.Title + "\" on the resource pages and in the Grafana dashboard (both read this one). The default uses the common metric names; write your own when yours differ. " +
				"The variables $cluster $namespace $workload $pod $node $container are filled by Grafana from its variables and by the platform with .* (all). " +
				legendNote(q)})
	}
	return out
}

func legendNote(q Query) string {
	if q.Group == GroupVariable {
		return "It fills a variable of the Grafana dashboard: write it as a label_values(...) of Grafana."
	}
	if q.Legend == "" {
		return "It must give one series (sum it)."
	}
	return "It must give one series for each " + strings.Trim(strings.SplitN(q.Legend, "{{", 2)[1], "}}") + ", with that label."
}

// MappingKind says which queries are the figures of a metric mapping (the metric templates of the kinds cluster_* and node_*): the query of the set by
// the kind of the mapping that replaces it. Every other query of the set is not asked of the data source by the platform and stays a parameter.
var MappingKind = map[string]string{
	"cluster_nodes": "cluster_nodes", "cluster_nodes_ready": "cluster_nodes_ready", "cluster_pods": "cluster_pods",
	"cluster_allocatable_cpu": "cluster_cpu_allocatable", "cluster_allocatable_memory": "cluster_memory_allocatable",
	"cluster_requests_cpu": "cluster_cpu_requests", "cluster_requests_memory": "cluster_memory_requests",
	"cluster_used_cpu": "cluster_cpu_used", "cluster_used_memory": "cluster_memory_used",
	"node_cpu_usage": "node_cpu_used", "node_memory_usage": "node_memory_used", "node_cpu_allocatable": "node_cpu_allocatable",
	"node_memory_allocatable": "node_memory_allocatable", "node_cpu_requests": "node_cpu_requests", "node_memory_requests": "node_memory_requests",
	"node_ready": "node_ready",
}

// MappedNames lists the queries that a metric mapping can replace, in the order of the set.
func MappedNames() []string {
	out := make([]string, 0, len(MappingKind))
	for _, q := range Queries {
		if _, ok := MappingKind[q.Name]; ok {
			out = append(out, q.Name)
		}
	}
	return out
}
