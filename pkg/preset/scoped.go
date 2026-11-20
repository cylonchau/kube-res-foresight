package preset

// The presets of the cluster and of its nodes: the figures the resource dashboard asks the data source for. They are ordinary mappings of the data source (level
// cluster or node), so a system whose metrics have other names edits the query of the mapping, in the same place as every other metric. What is written here is
// a default for the commonly exported series and is not claimed to fit any system.

const (
	clusterBase = `container!="",container!="POD"`
	byNode      = `sum by (node) `
	component   = "Prometheus"
)

var (
	needsNode   = Text{"序列要带 node 标签，页面按它对应到节点。", "The series must carry the label node: the page matches the nodes by it."}
	noRequests  = Text{"没有设 requests 的容器不出现。", "Containers without requests are absent."}
	scrapedOnly = Text{"只含被抓取的容器。", "Only the containers that are scraped."}
)

func scoped(id, metric, level, unit string, name, scenario Text, query string, series, labels []string, usage, limits Text) Preset {
	return Preset{ID: id, Metric: metric, Level: level, Unit: unit, StepSeconds: 60, Name: name, Scenario: scenario, Query: query,
		Source: Source{Component: component, Metrics: series, Labels: labels}, Usage: usage, Limitations: limits}
}

var scopedPresets = []Preset{
	scoped("cluster.nodes", "cluster_nodes", "cluster", "count",
		Text{"集群节点数", "Cluster nodes"}, Text{"集群里的节点总数。", "The number of nodes of the cluster."},
		`count(kube_node_info)`, []string{"kube_node_info"}, []string{"node"},
		Text{"资源大盘的集群卡片。", "The cluster card of the resource dashboard."},
		Text{"多个集群共用一个数据源时要加集群标签过滤。", "With several clusters in one data source, filter on the cluster label."}),
	scoped("cluster.nodes.ready", "cluster_nodes_ready", "cluster", "count",
		Text{"Ready 节点数", "Ready nodes"}, Text{"状态为 Ready 的节点数。", "The nodes whose Ready condition is true."},
		`sum(kube_node_status_condition{condition="Ready",status="true"})`, []string{"kube_node_status_condition"}, []string{"condition", "status"},
		Text{"与节点数对比，看有没有节点不可用。", "Compared with the node count, it shows nodes that are down."},
		Text{"节点从数据源消失时不计入。", "A node that has left the data source is not counted."}),
	scoped("cluster.pods", "cluster_pods", "cluster", "count",
		Text{"运行中的 Pod 数", "Running Pods"}, Text{"集群里处于 Running 的 Pod 总数。", "The Pods of the cluster that are Running."},
		`sum(kube_pod_status_phase{phase="Running"})`, []string{"kube_pod_status_phase"}, []string{"phase"},
		Text{"资源大盘的集群卡片。", "The cluster card of the resource dashboard."},
		Text{"不含 Pending 和已完成的 Pod。", "Pending and finished Pods are not counted."}),
	scoped("cluster.cpu.allocatable", "cluster_cpu_allocatable", "cluster", "cores",
		Text{"集群可分配 CPU", "Cluster allocatable CPU"}, Text{"所有节点可分配给 Pod 的 CPU 核数。", "The cores all the nodes have to give to Pods."},
		`sum(kube_node_status_allocatable{resource="cpu"})`, []string{"kube_node_status_allocatable"}, []string{"resource"},
		Text{"请求值、使用量都和它比。", "The requests and the use are measured against it."}, Text{"单位是核。", "In cores."}),
	scoped("cluster.memory.allocatable", "cluster_memory_allocatable", "cluster", "bytes",
		Text{"集群可分配内存", "Cluster allocatable memory"}, Text{"所有节点可分配给 Pod 的内存。", "The memory all the nodes have to give to Pods."},
		`sum(kube_node_status_allocatable{resource="memory"})`, []string{"kube_node_status_allocatable"}, []string{"resource"},
		Text{"请求值、使用量都和它比。", "The requests and the use are measured against it."}, Text{"单位是字节。", "In bytes."}),
	scoped("cluster.cpu.requests", "cluster_cpu_requests", "cluster", "cores",
		Text{"集群 CPU 请求合计", "Cluster CPU requests"}, Text{"所有容器的 CPU 请求合计。", "The CPU requests of all the containers."},
		`sum(kube_pod_container_resource_requests{resource="cpu"})`, []string{"kube_pod_container_resource_requests"}, []string{"resource"},
		Text{"已承诺出去的资源。", "What has been promised out."}, noRequests),
	scoped("cluster.memory.requests", "cluster_memory_requests", "cluster", "bytes",
		Text{"集群内存请求合计", "Cluster memory requests"}, Text{"所有容器的内存请求合计。", "The memory requests of all the containers."},
		`sum(kube_pod_container_resource_requests{resource="memory"})`, []string{"kube_pod_container_resource_requests"}, []string{"resource"},
		Text{"已承诺出去的资源。", "What has been promised out."}, noRequests),
	scoped("cluster.cpu.used", "cluster_cpu_used", "cluster", "cores",
		Text{"集群 CPU 使用量", "Cluster CPU used"}, Text{"所有容器此刻实际用的 CPU 核数。", "The cores all the containers use now."},
		`sum(rate(container_cpu_usage_seconds_total{`+clusterBase+`}[{{range}}]))`, []string{"container_cpu_usage_seconds_total"}, []string{"container"},
		Text{"{{range}} 由平台填 5m。", "{{range}} is filled with 5m by the platform."}, scrapedOnly),
	scoped("cluster.memory.used", "cluster_memory_used", "cluster", "bytes",
		Text{"集群内存使用量（工作集）", "Cluster memory used (working set)"}, Text{"所有容器的内存工作集合计。", "The working set of all the containers."},
		`sum(container_memory_working_set_bytes{`+clusterBase+`})`, []string{"container_memory_working_set_bytes"}, []string{"container"},
		Text{"OOM 判断以工作集为准。", "The working set is what the OOM killer acts on."}, scrapedOnly),

	scoped("node.cpu.used", "node_cpu_used", "node", "cores",
		Text{"节点 CPU 使用量", "Node CPU used"}, Text{"每个节点上所有容器用的 CPU 核数，一个节点一条序列。", "The cores the containers of each node use; one series for each node."},
		byNode+`(rate(container_cpu_usage_seconds_total{`+clusterBase+`}[{{range}}]))`, []string{"container_cpu_usage_seconds_total"}, []string{"node"},
		needsNode, Text{"容器指标没有 node 标签时，先用 join 把节点补上。", "When the container series have no node label, add it with a join first."}),
	scoped("node.memory.used", "node_memory_used", "node", "bytes",
		Text{"节点内存使用量（工作集）", "Node memory used (working set)"}, Text{"每个节点上所有容器的内存工作集，一个节点一条序列。", "The working set of the containers of each node; one series for each node."},
		byNode+`(container_memory_working_set_bytes{`+clusterBase+`})`, []string{"container_memory_working_set_bytes"}, []string{"node"},
		needsNode, Text{"同上。", "As above."}),
	scoped("node.cpu.allocatable", "node_cpu_allocatable", "node", "cores",
		Text{"节点可分配 CPU", "Node allocatable CPU"}, Text{"每个节点可分配的 CPU 核数。", "The cores each node has to give."},
		byNode+`(kube_node_status_allocatable{resource="cpu"})`, []string{"kube_node_status_allocatable"}, []string{"resource", "node"},
		needsNode, Text{"单位是核。", "In cores."}),
	scoped("node.memory.allocatable", "node_memory_allocatable", "node", "bytes",
		Text{"节点可分配内存", "Node allocatable memory"}, Text{"每个节点可分配的内存。", "The memory each node has to give."},
		byNode+`(kube_node_status_allocatable{resource="memory"})`, []string{"kube_node_status_allocatable"}, []string{"resource", "node"},
		needsNode, Text{"单位是字节。", "In bytes."}),
	scoped("node.cpu.requests", "node_cpu_requests", "node", "cores",
		Text{"节点 CPU 请求合计", "Node CPU requests"}, Text{"每个节点上所有容器的 CPU 请求合计。", "The CPU requests of the containers of each node."},
		byNode+`(kube_pod_container_resource_requests{resource="cpu"})`, []string{"kube_pod_container_resource_requests"}, []string{"resource", "node"},
		needsNode, noRequests),
	scoped("node.memory.requests", "node_memory_requests", "node", "bytes",
		Text{"节点内存请求合计", "Node memory requests"}, Text{"每个节点上所有容器的内存请求合计。", "The memory requests of the containers of each node."},
		byNode+`(kube_pod_container_resource_requests{resource="memory"})`, []string{"kube_pod_container_resource_requests"}, []string{"resource", "node"},
		needsNode, noRequests),
	scoped("node.ready", "node_ready", "node", "count",
		Text{"节点 Ready 状态", "Node Ready"}, Text{"每个节点的 Ready 状态，1 为 Ready。", "The Ready condition of each node; 1 is Ready."},
		byNode+`(kube_node_status_condition{condition="Ready",status="true"})`, []string{"kube_node_status_condition"}, []string{"condition", "status", "node"},
		needsNode, Text{"节点不 Ready 时序列可能是 0 或不存在。", "A node that is not Ready answers 0 or no series."}),
}
