// Package grafana builds the resource dashboard of Grafana (P10 A4): the cluster, the nodes, a workload, its Pods and Containers and its JVM, from the same set of
// PromQL as the resource pages of the platform (package resourcequery), so the figure on a page and the figure in a panel are the answer to one query.
// The dashboard is generated, not written by hand: a system whose metrics have other names edits the query in the system parameter and downloads the dashboard
// again, and the page and the dashboard agree. The platform cannot load the JSON into a real Grafana; it checks its structure (see the tests), not that a given
// version of Grafana takes it.
package grafana

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
)

// ShippedName is the file the default dashboard is shipped as in deploy/grafana.
const ShippedName = "kube-res-foresight-resources.json"

// DefaultUID is the uid of the dashboard; the system parameter grafana.dashboard_uid names the one the pages link to.
const DefaultUID = "kube-res-foresight-resources"

// PlatformPage is the page of the platform a panel of a workload links back to.
const PlatformPage = "/forecast-results/overprovision"

// Options shape the dashboard.
type Options struct {
	UID   string
	Title string
	// Queries is the set of PromQL, from the system parameters (the defaults when nil).
	Queries resourcequery.Set
	// Cluster is the name of the cluster: the default value of the cluster variable.
	Cluster string
	// PlatformURL is the address people reach the platform at; empty leaves out the links back to it.
	PlatformURL string
}

type m = map[string]any

// panel specs: what a panel shows.
type spec struct {
	title  string
	kind   string // stat or timeseries
	unit   string
	width  int
	height int
	// queries are names in the set; a stat has one, a graph any number of them drawn together.
	queries []string
	// legends overrides the legend of a query in this panel (a graph of totals names its lines)
	legends map[string]string
	// link makes the panel link back to the platform page of the workload
	link bool
}

type row struct {
	title  string
	group  string
	panels []spec
}

var rows = []row{
	{"Cluster", resourcequery.GroupCluster, []spec{
		{title: "Nodes", kind: "stat", width: 4, height: 4, queries: []string{"cluster_nodes"}},
		{title: "Nodes ready", kind: "stat", width: 4, height: 4, queries: []string{"cluster_nodes_ready"}},
		{title: "Pods running", kind: "stat", width: 4, height: 4, queries: []string{"cluster_pods"}},
		{title: "CPU used / allocatable (cores)", kind: "stat", width: 6, height: 4, queries: []string{"cluster_used_cpu", "cluster_allocatable_cpu"}},
		{title: "Memory used / allocatable", kind: "stat", unit: "bytes", width: 6, height: 4, queries: []string{"cluster_used_memory", "cluster_allocatable_memory"}},
		{title: "CPU: allocatable, requested, used (cores)", kind: "timeseries", unit: "short", width: 12, height: 8, queries: []string{"cluster_allocatable_cpu", "cluster_requests_cpu", "cluster_used_cpu"},
			legends: map[string]string{"cluster_allocatable_cpu": "allocatable", "cluster_requests_cpu": "requested", "cluster_used_cpu": "used"}},
		{title: "Memory: allocatable, requested, used", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"cluster_allocatable_memory", "cluster_requests_memory", "cluster_used_memory"},
			legends: map[string]string{"cluster_allocatable_memory": "allocatable", "cluster_requests_memory": "requested", "cluster_used_memory": "used"}},
		{title: "Nodes and nodes ready", kind: "timeseries", unit: "short", width: 12, height: 7, queries: []string{"cluster_nodes", "cluster_nodes_ready"},
			legends: map[string]string{"cluster_nodes": "nodes", "cluster_nodes_ready": "ready"}},
	}},
	{"Nodes", resourcequery.GroupNode, []spec{
		{title: "Node CPU used (cores)", kind: "timeseries", unit: "short", width: 12, height: 8, queries: []string{"node_cpu_usage"}},
		{title: "Node CPU requested (cores)", kind: "timeseries", unit: "short", width: 12, height: 8, queries: []string{"node_cpu_requests"}},
		{title: "Node memory used", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"node_memory_usage"}},
		{title: "Node memory requested", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"node_memory_requests"}},
		{title: "Node CPU allocatable (cores)", kind: "timeseries", unit: "short", width: 8, height: 7, queries: []string{"node_cpu_allocatable"}},
		{title: "Node memory allocatable", kind: "timeseries", unit: "bytes", width: 8, height: 7, queries: []string{"node_memory_allocatable"}},
		{title: "Node ready", kind: "timeseries", unit: "short", width: 8, height: 7, queries: []string{"node_ready"}},
	}},
	{"Workload", resourcequery.GroupWorkload, []spec{
		{title: "CPU: requests, limits, used (cores)", kind: "timeseries", unit: "short", width: 12, height: 8, link: true, queries: []string{"workload_cpu_requests", "workload_cpu_limits", "workload_cpu_usage"},
			legends: map[string]string{"workload_cpu_requests": "requests", "workload_cpu_limits": "limits", "workload_cpu_usage": "used"}},
		{title: "Memory: requests, limits, used", kind: "timeseries", unit: "bytes", width: 12, height: 8, link: true, queries: []string{"workload_memory_requests", "workload_memory_limits", "workload_memory_usage"},
			legends: map[string]string{"workload_memory_requests": "requests", "workload_memory_limits": "limits", "workload_memory_usage": "used"}},
		{title: "Replicas ready", kind: "timeseries", unit: "short", width: 8, height: 7, link: true, queries: []string{"workload_replicas"}},
		{title: "CPU throttled (s/s)", kind: "timeseries", unit: "s", width: 8, height: 7, queries: []string{"workload_cpu_throttle"}},
		{title: "OOM kills", kind: "timeseries", unit: "short", width: 8, height: 7, queries: []string{"workload_oom"}},
	}},
	{"Pods", resourcequery.GroupPod, []spec{
		{title: "Pod CPU used (cores)", kind: "timeseries", unit: "short", width: 12, height: 8, queries: []string{"pod_cpu_usage"}},
		{title: "Pod memory used", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"pod_memory_usage"}},
		{title: "Pod restarts", kind: "timeseries", unit: "short", width: 24, height: 7, queries: []string{"pod_restarts"}},
	}},
	{"Containers", resourcequery.GroupContainer, []spec{
		{title: "Container CPU used (cores)", kind: "timeseries", unit: "short", width: 8, height: 8, queries: []string{"container_cpu_usage"}},
		{title: "Container memory used", kind: "timeseries", unit: "bytes", width: 8, height: 8, queries: []string{"container_memory_usage"}},
		{title: "Container memory limit", kind: "timeseries", unit: "bytes", width: 8, height: 8, queries: []string{"container_memory_limit"}},
	}},
	{"JVM", resourcequery.GroupJVM, []spec{
		{title: "Heap: used, committed, max", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"jvm_heap_used", "jvm_heap_committed", "jvm_heap_max"},
			legends: map[string]string{"jvm_heap_used": "used", "jvm_heap_committed": "committed", "jvm_heap_max": "max"}},
		{title: "Non-heap: used, committed, max", kind: "timeseries", unit: "bytes", width: 12, height: 8, queries: []string{"jvm_nonheap_used", "jvm_nonheap_committed", "jvm_nonheap_max"},
			legends: map[string]string{"jvm_nonheap_used": "used", "jvm_nonheap_committed": "committed", "jvm_nonheap_max": "max"}},
		{title: "GC collections (per s)", kind: "timeseries", unit: "short", width: 8, height: 7, queries: []string{"jvm_gc_count"}},
		{title: "GC pause time (s/s)", kind: "timeseries", unit: "s", width: 8, height: 7, queries: []string{"jvm_gc_pause"}},
		{title: "Live threads", kind: "timeseries", unit: "short", width: 8, height: 7, queries: []string{"jvm_threads"}},
	}},
}

func datasource() m { return m{"type": "prometheus", "uid": "${datasource}"} }

func variable(name, label, query string, multi bool) m {
	v := m{"name": name, "label": label, "type": "query", "datasource": datasource(), "query": m{"query": query, "refId": name}, "definition": query, "refresh": 2, "sort": 1,
		"hide": 0, "includeAll": true, "multi": multi, "allValue": ".*", "current": m{"selected": true, "text": "All", "value": "$__all"}}
	return v
}

func (o Options) queries() resourcequery.Set {
	if o.Queries != nil {
		return o.Queries
	}
	return resourcequery.Defaults()
}

// Build is the dashboard as a JSON object.
func Build(o Options) map[string]any {
	if o.UID == "" {
		o.UID = DefaultUID
	}
	if o.Title == "" {
		o.Title = "kube-res-foresight resources"
	}
	if o.Cluster == "" {
		o.Cluster = "default"
	}
	set := o.queries()
	platform := strings.TrimRight(strings.TrimSpace(o.PlatformURL), "/")

	templating := []any{
		m{"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus", "current": m{}, "hide": 0, "refresh": 1, "options": []any{}},
		// the platform manages one cluster: the variable carries its name, for the queries that are written with a cluster label and for the links
		m{"name": "cluster", "label": "Cluster", "type": "custom", "query": o.Cluster, "hide": 0, "includeAll": false, "multi": false,
			"current": m{"selected": true, "text": o.Cluster, "value": o.Cluster}, "options": []any{m{"selected": true, "text": o.Cluster, "value": o.Cluster}}},
		variable("namespace", "Namespace", set["var_namespace"], true),
		variable("workload", "Workload", set["var_workload"], true),
		variable("pod", "Pod", set["var_pod"], true),
		variable("node", "Node", set["var_node"], true),
		variable("container", "Container", set["var_container"], true),
	}

	panels := []any{}
	id := 1
	y := 0
	for _, r := range rows {
		panels = append(panels, m{"id": id, "type": "row", "title": r.title, "collapsed": false, "gridPos": m{"h": 1, "w": 24, "x": 0, "y": y}, "panels": []any{}})
		id++
		y++
		x, rowHeight := 0, 0
		for _, sp := range r.panels {
			if x+sp.width > 24 {
				y += rowHeight
				x, rowHeight = 0, 0
			}
			panels = append(panels, buildPanel(id, sp, set, x, y, platform))
			id++
			x += sp.width
			if sp.height > rowHeight {
				rowHeight = sp.height
			}
		}
		y += rowHeight
	}

	links := []any{}
	if platform != "" {
		links = append(links, m{"title": "Resource dashboard of the platform", "type": "link", "icon": "external link", "targetBlank": true, "keepTime": false, "includeVars": false,
			"url": platform + PlatformPage})
	}
	return m{
		"uid": o.UID, "title": o.Title, "description": "The cluster, the nodes, a workload with its Pods and Containers, and its JVM. Every query is a system parameter of the platform (query.*), the same the resource pages of the platform ask.",
		"tags": []string{"kube-res-foresight", "resources", "kubernetes"}, "timezone": "browser", "editable": true, "schemaVersion": 39, "version": 1, "refresh": "1m",
		"time": m{"from": "now-6h", "to": "now"}, "graphTooltip": 1, "templating": m{"list": templating}, "panels": panels, "links": links,
		"annotations": m{"list": []any{}},
	}
}

func buildPanel(id int, sp spec, set resourcequery.Set, x, y int, platform string) m {
	targets := []any{}
	for i, name := range sp.queries {
		q, _ := resourcequery.Find(name)
		legend := q.Legend
		if l, ok := sp.legends[name]; ok {
			legend = l
		}
		t := m{"refId": string(rune('A' + i)), "expr": set[name], "datasource": datasource()}
		if sp.kind == "timeseries" {
			t["legendFormat"] = legend
		}
		if sp.kind == "stat" {
			t["instant"] = true
		}
		targets = append(targets, t)
	}
	unit := sp.unit
	if unit == "" {
		unit = "short"
	}
	p := m{"id": id, "type": sp.kind, "title": sp.title, "datasource": datasource(), "gridPos": m{"h": sp.height, "w": sp.width, "x": x, "y": y}, "targets": targets,
		"fieldConfig": m{"defaults": m{"unit": unit}, "overrides": []any{}}}
	switch sp.kind {
	case "stat":
		p["options"] = m{"reduceOptions": m{"calcs": []string{"lastNotNull"}, "fields": "", "values": false}, "colorMode": "none", "graphMode": "none", "textMode": "auto", "orientation": "auto"}
		if len(sp.queries) > 1 {
			p["options"].(m)["textMode"] = "value_and_name"
			p["fieldConfig"].(m)["defaults"].(m)["displayName"] = ""
		}
	default:
		p["options"] = m{"legend": m{"displayMode": "table", "placement": "bottom", "calcs": []string{"lastNotNull", "max"}}, "tooltip": m{"mode": "multi", "sort": "desc"}}
		p["fieldConfig"].(m)["defaults"].(m)["custom"] = m{"lineWidth": 1, "fillOpacity": 8, "showPoints": "never", "spanNulls": true}
	}
	if sp.link && platform != "" {
		p["links"] = []any{m{"title": "Open this workload in the platform", "targetBlank": true,
			"url": platform + PlatformPage + "?namespace=${namespace:raw}&workload=${workload:raw}&" + (url.Values{"source": {"grafana"}}).Encode()}}
	}
	return p
}

// JSON is the dashboard as indented JSON, with a newline at the end.
func JSON(o Options) ([]byte, error) {
	raw, err := json.MarshalIndent(Build(o), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
