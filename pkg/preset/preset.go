// Package preset is the built-in, read-only library of metric mapping templates. A preset is a ready PromQL
// template for a well-known exporter together with where its data comes from and what limits it has, so an
// administrator never writes the common queries by hand. Presets ship with the program; applying one creates an
// ordinary, editable metric mapping that remembers its preset id.
package preset

import (
	"fmt"
	"sort"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Text is a bilingual string.
type Text struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}

// Source says where the data comes from, so the administrator knows what must be running.
type Source struct {
	Component string   `json:"component"` // e.g. cAdvisor (kubelet)
	Metrics   []string `json:"metrics"`   // the series the template reads
	Labels    []string `json:"labels"`    // labels the series must carry
}

// Preset is one template.
type Preset struct {
	ID          string `json:"id"`
	Name        Text   `json:"name"`
	Scenario    Text   `json:"scenario"`
	Metric      string `json:"metric"`
	Level       string `json:"level"`
	Unit        string `json:"unit"`
	StepSeconds int    `json:"step_seconds"`
	Query       string `json:"query"`
	Source      Source `json:"source"`
	Usage       Text   `json:"usage"`
	Limitations Text   `json:"limitations"`
	// Verified is false until the preset has been run against a real Prometheus. Every preset ships false:
	// the tests prove the templates render and are recognised by the demo generator, not that a cluster answers.
	Verified bool `json:"verified"`
}

// Mapping builds the metric mapping a preset describes for a datasource. name defaults to the preset id and
// stepSeconds, when positive, overrides the default step.
func (p Preset) Mapping(datasourceID uint, name string, stepSeconds int) *model.MetricMapping {
	if name == "" {
		name = p.ID
	}
	if stepSeconds <= 0 {
		stepSeconds = p.StepSeconds
	}
	return &model.MetricMapping{
		DatasourceID: datasourceID, Name: name, Metric: p.Metric, Level: p.Level, Query: p.Query, Unit: p.Unit,
		StepSeconds: stepSeconds, Enabled: true, PresetID: p.ID, Remark: p.Name.EN,
	}
}

const (
	workloadPods = `namespace="{{namespace}}",pod=~"{{workload}}-.*"`
	cadvisorBase = `container!="",container!="POD"`
)

var presets = []Preset{
	{
		ID: "cadvisor.cpu.workload", Metric: "cpu", Level: "workload", Unit: "cores", StepSeconds: 60,
		Name:     Text{"CPU 使用量（工作负载合计）", "CPU usage (workload total)"},
		Scenario: Text{"工作负载所有 Pod 的 CPU 核数合计，用于副本数的 CPU 约束与 CPU 预测。", "Cores used by all pods of the workload; feeds the CPU replica constraint and CPU forecasts."},
		Query:    `sum(rate(container_cpu_usage_seconds_total{` + workloadPods + `,` + cadvisorBase + `}[{{range}}]))`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_cpu_usage_seconds_total"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:    Text{"Pod 名称按 Deployment 命名（工作负载名-哈希-随机）匹配。", "Pods are matched by the Deployment naming scheme (workload-hash-random)."},
		Limitations: Text{
			"StatefulSet、DaemonSet 或自定义 Pod 名需改写 pod 匹配；多容器 Pod 含 sidecar。",
			"StatefulSets, DaemonSets and custom pod names need a different pod matcher; sidecars are included.",
		},
	},
	{
		ID: "cadvisor.cpu.container", Metric: "cpu", Level: "container", Unit: "cores", StepSeconds: 60,
		Name:     Text{"CPU 使用量（单容器合计）", "CPU usage (one container)"},
		Scenario: Text{"只看业务容器、排除 sidecar 的 CPU 核数合计。", "Cores used by the application container only, sidecars excluded."},
		Query:    `sum(rate(container_cpu_usage_seconds_total{namespace="{{namespace}}",pod=~"{{workload}}-.*",container="{{container}}"}[{{range}}]))`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_cpu_usage_seconds_total"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:    Text{"需要在模块中填写容器名。", "The module must name the container."},
		Limitations: Text{
			"这是容器所有副本的合计，不是单个 Pod；用于副本数 CPU 约束时请选工作负载级预设。",
			"This is the total over all replicas, not one pod; use the workload preset for the replica CPU constraint.",
		},
	},
	{
		ID: "cadvisor.memory.workload", Metric: "memory", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:     Text{"内存工作集（工作负载合计）", "Memory working set (workload total)"},
		Scenario: Text{"工作负载所有 Pod 的内存工作集合计，OOM 判断以工作集为准。", "Working set of all pods of the workload; the working set is what the OOM killer acts on."},
		Query:    `sum(container_memory_working_set_bytes{` + workloadPods + `,` + cadvisorBase + `})`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_memory_working_set_bytes"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:    Text{"瞬时值，无需 range。", "An instant value; no range is needed."},
		Limitations: Text{
			"Pod 名匹配规则同 CPU 预设。",
			"The pod matcher has the same limits as the CPU preset.",
		},
	},
	{
		ID: "cadvisor.memory.container", Metric: "memory", Level: "container", Unit: "bytes", StepSeconds: 60,
		Name:     Text{"内存工作集（单容器合计）", "Memory working set (one container)"},
		Scenario: Text{"只看业务容器的内存工作集合计。", "Working set of the application container only."},
		Query:    `sum(container_memory_working_set_bytes{namespace="{{namespace}}",pod=~"{{workload}}-.*",container="{{container}}"})`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_memory_working_set_bytes"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:    Text{"需要在模块中填写容器名。", "The module must name the container."},
		Limitations: Text{
			"所有副本的合计，不是单个 Pod。", "The total over all replicas, not one pod.",
		},
	},
	{
		ID: "kube-state-metrics.replicas.ready", Metric: "replicas", Level: "workload", Unit: "count", StepSeconds: 60,
		Name:     Text{"Ready 副本数（Deployment）", "Ready replicas (Deployment)"},
		Scenario: Text{"Deployment 的可用副本数，用于从历史估计单 Pod 容量。", "Available replicas of a Deployment; used to estimate one pod's capacity from history."},
		Query:    `sum(kube_deployment_status_replicas_available{namespace="{{namespace}}",deployment="{{workload}}"})`,
		Source:   Source{Component: "kube-state-metrics", Metrics: []string{"kube_deployment_status_replicas_available"}, Labels: []string{"namespace", "deployment"}},
		Usage:    Text{"工作负载名即 Deployment 名。", "The workload name is the Deployment name."},
		Limitations: Text{
			"仅 Deployment；StatefulSet 请把指标换成 kube_statefulset_status_replicas_ready。",
			"Deployments only; for a StatefulSet use kube_statefulset_status_replicas_ready.",
		},
	},
	{
		ID: "kube-state-metrics.pods.ready", Metric: "replicas", Level: "workload", Unit: "count", StepSeconds: 60,
		Name:     Text{"Ready Pod 数（按名称匹配）", "Ready pods (matched by name)"},
		Scenario: Text{"按 Pod 名称匹配统计 Ready 的 Pod，适用于非 Deployment 工作负载。", "Counts ready pods by name; works for workloads that are not Deployments."},
		Query:    `sum(kube_pod_status_ready{` + workloadPods + `,condition="true"})`,
		Source:   Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_status_ready"}, Labels: []string{"namespace", "pod", "condition"}},
		Usage:    Text{"与 cAdvisor 预设使用同一 Pod 名称匹配规则。", "Uses the same pod name matcher as the cAdvisor presets."},
		Limitations: Text{
			"滚动发布时旧新 Pod 同时 Ready，计数会短暂偏大。", "During a rollout old and new pods are ready together, so the count is briefly high.",
		},
	},
	{
		ID: "ingress-nginx.rps.service", Metric: "rps", Level: "service", Unit: "rps", StepSeconds: 60,
		Name:     Text{"请求速率（ingress-nginx，按 Service）", "Request rate (ingress-nginx, per Service)"},
		Scenario: Text{"经 ingress-nginx 进入某 Service 的请求速率。", "Requests per second entering a Service through ingress-nginx."},
		Query:    `sum(rate(nginx_ingress_controller_requests{exported_namespace="{{namespace}}",exported_service="{{service}}"}[{{range}}]))`,
		Source:   Source{Component: "ingress-nginx controller", Metrics: []string{"nginx_ingress_controller_requests"}, Labels: []string{"exported_namespace", "exported_service"}},
		Usage:    Text{"需要在模块中填写 Service 名。", "The module must name the Service."},
		Limitations: Text{
			"只统计经过 Ingress 的流量，集群内部调用不在其中；需启用 controller 指标。",
			"Only traffic through the Ingress is counted, not in-cluster calls; controller metrics must be enabled.",
		},
	},
	{
		ID: "istio.rps.service", Metric: "rps", Level: "service", Unit: "rps", StepSeconds: 60,
		Name:     Text{"请求速率（Istio，按 Service）", "Request rate (Istio, per Service)"},
		Scenario: Text{"服务网格内某 Service 收到的全部请求速率（含集群内部）。", "All requests a Service receives inside the mesh, in-cluster calls included."},
		Query:    `sum(rate(istio_requests_total{reporter="destination",destination_service_namespace="{{namespace}}",destination_service_name="{{service}}"}[{{range}}]))`,
		Source:   Source{Component: "Istio (Envoy sidecar)", Metrics: []string{"istio_requests_total"}, Labels: []string{"reporter", "destination_service_namespace", "destination_service_name"}},
		Usage:    Text{"需要在模块中填写 Service 名。", "The module must name the Service."},
		Limitations: Text{
			"需要 sidecar 注入且 Prometheus 抓取 Envoy 指标；高基数标签需自行控制。",
			"Needs sidecar injection and Prometheus scraping of Envoy metrics; keep label cardinality in check.",
		},
	},
	{
		ID: "ingress-nginx.latency.p95", Metric: "latency", Level: "service", Unit: "seconds", StepSeconds: 60,
		Name:     Text{"P95 延迟（ingress-nginx）", "P95 latency (ingress-nginx)"},
		Scenario: Text{"经 Ingress 的请求 P95 延迟，用于判断容量是否充足。", "P95 latency of requests through the Ingress; shows whether capacity is sufficient."},
		Query:    `histogram_quantile(0.95, sum by (le) (rate(nginx_ingress_controller_request_duration_seconds_bucket{exported_namespace="{{namespace}}",exported_service="{{service}}"}[{{range}}])))`,
		Source:   Source{Component: "ingress-nginx controller", Metrics: []string{"nginx_ingress_controller_request_duration_seconds_bucket"}, Labels: []string{"exported_namespace", "exported_service", "le"}},
		Usage:    Text{"当前仅用于观察，不参与副本数计算。", "Observation only; it does not enter the replica calculation."},
		Limitations: Text{
			"分位数由直方图桶估计，精度取决于桶边界。", "Quantiles are estimated from histogram buckets; accuracy depends on the bucket bounds.",
		},
	},
	{
		ID: "istio.latency.p95", Metric: "latency", Level: "service", Unit: "seconds", StepSeconds: 60,
		Name:     Text{"P95 延迟（Istio）", "P95 latency (Istio)"},
		Scenario: Text{"服务网格内某 Service 的 P95 延迟（秒）。", "P95 latency of a Service in the mesh, in seconds."},
		Query:    `histogram_quantile(0.95, sum by (le) (rate(istio_request_duration_milliseconds_bucket{reporter="destination",destination_service_namespace="{{namespace}}",destination_service_name="{{service}}"}[{{range}}]))) / 1000`,
		Source:   Source{Component: "Istio (Envoy sidecar)", Metrics: []string{"istio_request_duration_milliseconds_bucket"}, Labels: []string{"reporter", "destination_service_namespace", "destination_service_name", "le"}},
		Usage:    Text{"Istio 以毫秒记录，模板已除以 1000；仅用于观察。", "Istio records milliseconds, so the template divides by 1000; observation only."},
		Limitations: Text{
			"分位数精度取决于桶边界。", "Quantile accuracy depends on the bucket bounds.",
		},
	},
	{
		ID: "cadvisor.oom.events", Metric: "oom", Level: "workload", Unit: "count", StepSeconds: 60,
		Name:     Text{"OOM 次数（cAdvisor）", "OOM events (cAdvisor)"},
		Scenario: Text{"窗口内容器被 OOM Killer 终止的次数，判断内存是否被低估。", "Containers killed by the OOM killer within the window; shows whether memory was under-provisioned."},
		Query:    `sum(increase(container_oom_events_total{` + workloadPods + `}[{{range}}]))`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_oom_events_total"}, Labels: []string{"namespace", "pod"}},
		Usage:    Text{"仅用于观察与回溯，不参与副本数计算。", "Observation and review only; it does not enter the replica calculation."},
		Limitations: Text{
			"container_oom_events_total 需要较新的 cAdvisor（kubelet）；旧版本没有该指标。",
			"container_oom_events_total needs a recent cAdvisor (kubelet); older versions do not export it.",
		},
	},
	{
		ID: "kube-state-metrics.oom.last-terminated", Metric: "oom", Level: "workload", Unit: "count", StepSeconds: 60,
		Name:     Text{"最近一次终止原因为 OOM 的容器数", "Containers whose last termination was OOM"},
		Scenario: Text{"cAdvisor 没有 OOM 事件指标时的替代：统计上次终止原因为 OOMKilled 的容器。", "Fallback when cAdvisor has no OOM counter: containers whose last termination was OOMKilled."},
		Query:    `sum(kube_pod_container_status_last_terminated_reason{` + workloadPods + `,reason="OOMKilled"})`,
		Source:   Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_container_status_last_terminated_reason"}, Labels: []string{"namespace", "pod", "reason"}},
		Usage:    Text{"这是瞬时状态而非事件计数。", "This is a state, not an event count."},
		Limitations: Text{
			"容器重启后原因会被覆盖，可能漏计或重复计。", "The reason is overwritten when the container restarts, so events can be missed or repeated.",
		},
	},
	{
		ID: "cadvisor.oom.working-set", Metric: "oom_working_set", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:     Text{"内存工作集峰值（OOM 地板依据）", "Memory working-set peak (OOM floor basis)"},
		Scenario: Text{"每个步长内工作负载内存工作集的最大值；发生 OOM 时，取 OOM 前的该值作为内存推荐的下限依据。", "The largest working set of the workload in each step; after an OOM the value before the kill sizes the memory floor."},
		Query:    `sum(max_over_time(container_memory_working_set_bytes{` + workloadPods + `,` + cadvisorBase + `}[{{range}}]))`,
		Source:   Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_memory_working_set_bytes"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:    Text{"与 OOM 次数映射一起绑定到模块；也可作为协变量。", "Bind it next to the OOM events mapping; it can also be a covariate."},
		Limitations: Text{
			"工作集在被杀进程消失后下降，抓取间隔内的尖峰可能被漏掉。", "The working set drops once the killed process is gone, and a spike between two scrapes can be missed.",
		},
	},
	{
		ID: "micrometer.jvm.heap-used", Metric: "jvm_heap_used", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:     Text{"JVM 堆已用（Micrometer）", "JVM heap used (Micrometer)"},
		Scenario: Text{"工作负载所有 Pod 的 JVM 堆已用字节合计，配合堆上限计算堆压力。", "JVM heap bytes in use across the pods of the workload; with the heap maximum it gives the heap pressure."},
		Query:    `sum(jvm_memory_used_bytes{area="heap",` + workloadPods + `})`,
		Source:   Source{Component: "Micrometer / Spring Boot Actuator", Metrics: []string{"jvm_memory_used_bytes"}, Labels: []string{"area", "namespace", "pod"}},
		Usage:    Text{"与「JVM 堆上限」一起绑定，副本推荐才会应用堆压力约束。", "Bind it together with the JVM heap maximum, or the replica recommendation applies no heap pressure rule."},
		Limitations: Text{
			"非 Java 工作负载没有该指标；GC 后堆会骤降，瞬时值可能偏低。", "Non-Java workloads do not export it; the heap drops after a GC so a single sample can be low.",
		},
	},
	{
		ID: "micrometer.jvm.heap-max", Metric: "jvm_heap_max", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:     Text{"JVM 堆上限（Micrometer）", "JVM heap maximum (Micrometer)"},
		Scenario: Text{"工作负载所有 Pod 的 JVM 堆上限合计。", "JVM heap maximum summed over the pods of the workload."},
		Query:    `sum(jvm_memory_max_bytes{area="heap",` + workloadPods + `})`,
		Source:   Source{Component: "Micrometer / Spring Boot Actuator", Metrics: []string{"jvm_memory_max_bytes"}, Labels: []string{"area", "namespace", "pod"}},
		Usage:    Text{"堆压力 = 堆已用 / 堆上限。", "Heap pressure = heap used / heap maximum."},
		Limitations: Text{
			"某些内存池未设上限时为 -1，合计会失真；请确认设置了 -Xmx。", "A pool without a limit reports -1 and distorts the sum; make sure -Xmx is set.",
		},
	},
	{
		ID: "micrometer.jvm.nonheap-used", Metric: "jvm_nonheap_used", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:        Text{"JVM 非堆内存已用（Micrometer）", "JVM non-heap used (Micrometer)"},
		Scenario:    Text{"元空间、代码缓存等非堆内存，容器内存推荐的协变量。", "Metaspace, code cache and other non-heap memory; a covariate for the container memory forecast."},
		Query:       `sum(jvm_memory_used_bytes{area="nonheap",` + workloadPods + `})`,
		Source:      Source{Component: "Micrometer / Spring Boot Actuator", Metrics: []string{"jvm_memory_used_bytes"}, Labels: []string{"area", "namespace", "pod"}},
		Usage:       Text{"仅作协变量，不直接改变推荐。", "A covariate only; it does not change the recommendation directly."},
		Limitations: Text{"不含堆外直接内存（NIO）与线程栈。", "Direct (NIO) buffers and thread stacks are not included."},
	},
	{
		ID: "micrometer.jvm.gc-pause", Metric: "jvm_gc_pause", Level: "workload", Unit: "seconds", StepSeconds: 60,
		Name:        Text{"JVM GC 暂停总时长（Micrometer）", "JVM GC pause time (Micrometer)"},
		Scenario:    Text{"窗口内所有 Pod 的 GC 暂停总秒数，反映堆是否吃紧。", "Seconds spent in GC pauses across the pods within the window; shows whether the heap is tight."},
		Query:       `sum(increase(jvm_gc_pause_seconds_sum{` + workloadPods + `}[{{range}}]))`,
		Source:      Source{Component: "Micrometer / Spring Boot Actuator", Metrics: []string{"jvm_gc_pause_seconds_sum"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"作协变量，帮助内存与延迟预测。", "A covariate for the memory and latency forecasts."},
		Limitations: Text{"Pod 重启会使计数器重置，increase 会据此处理但窗口内可能略偏小。", "A pod restart resets the counter; increase() handles it but the window can read slightly low."},
	},
	{
		ID: "micrometer.jvm.threads", Metric: "jvm_threads", Level: "workload", Unit: "count", StepSeconds: 60,
		Name:        Text{"JVM 存活线程数（Micrometer）", "JVM live threads (Micrometer)"},
		Scenario:    Text{"工作负载所有 Pod 的存活线程数合计。", "Live threads summed over the pods of the workload."},
		Query:       `sum(jvm_threads_live_threads{` + workloadPods + `})`,
		Source:      Source{Component: "Micrometer / Spring Boot Actuator", Metrics: []string{"jvm_threads_live_threads"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"作协变量，线程暴涨常早于 CPU 与内存上升。", "A covariate: a thread surge often comes before CPU and memory."},
		Limitations: Text{"旧版 Micrometer 的指标名为 jvm_threads_live。", "Older Micrometer versions name it jvm_threads_live."},
	},
	{
		ID: "kube-state-metrics.cpu.requests", Metric: "k8s_cpu_requests", Level: "workload", Unit: "cores", StepSeconds: 60,
		Name:        Text{"CPU Requests（kube-state-metrics）", "CPU requests (kube-state-metrics)"},
		Scenario:    Text{"工作负载所有 Pod 的 CPU requests 合计，用来对照推荐值。", "CPU requests summed over the pods of the workload, to compare with the recommendation."},
		Query:       `sum(kube_pod_container_resource_requests{resource="cpu",` + workloadPods + `})`,
		Source:      Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_container_resource_requests"}, Labels: []string{"resource", "namespace", "pod"}},
		Usage:       Text{"配置类指标，随副本数变化。", "A configuration metric; it moves with the replica count."},
		Limitations: Text{"需要 kube-state-metrics v2；v1 的指标名不同。", "Needs kube-state-metrics v2; v1 names the series differently."},
	},
	{
		ID: "kube-state-metrics.cpu.limits", Metric: "k8s_cpu_limits", Level: "workload", Unit: "cores", StepSeconds: 60,
		Name:        Text{"CPU Limits（kube-state-metrics）", "CPU limits (kube-state-metrics)"},
		Scenario:    Text{"工作负载所有 Pod 的 CPU limits 合计；推荐 CPU 超过它时会被限流。", "CPU limits summed over the pods; a recommended CPU above it will be throttled."},
		Query:       `sum(kube_pod_container_resource_limits{resource="cpu",` + workloadPods + `})`,
		Source:      Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_container_resource_limits"}, Labels: []string{"resource", "namespace", "pod"}},
		Usage:       Text{"配置类指标。", "A configuration metric."},
		Limitations: Text{"未设 limits 的容器不出现在结果中。", "Containers without limits are absent from the result."},
	},
	{
		ID: "kube-state-metrics.memory.requests", Metric: "k8s_mem_requests", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:        Text{"内存 Requests（kube-state-metrics）", "Memory requests (kube-state-metrics)"},
		Scenario:    Text{"工作负载所有 Pod 的内存 requests 合计。", "Memory requests summed over the pods of the workload."},
		Query:       `sum(kube_pod_container_resource_requests{resource="memory",` + workloadPods + `})`,
		Source:      Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_container_resource_requests"}, Labels: []string{"resource", "namespace", "pod"}},
		Usage:       Text{"配置类指标。", "A configuration metric."},
		Limitations: Text{"需要 kube-state-metrics v2。", "Needs kube-state-metrics v2."},
	},
	{
		ID: "kube-state-metrics.memory.limits", Metric: "k8s_mem_limits", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:        Text{"内存 Limits（kube-state-metrics）", "Memory limits (kube-state-metrics)"},
		Scenario:    Text{"工作负载所有 Pod 的内存 limits 合计；OOM 地板以它与 OOM 前工作集的较大者为基准。", "Memory limits summed over the pods; the OOM floor starts from the larger of it and the working set before the kill."},
		Query:       `sum(kube_pod_container_resource_limits{resource="memory",` + workloadPods + `})`,
		Source:      Source{Component: "kube-state-metrics", Metrics: []string{"kube_pod_container_resource_limits"}, Labels: []string{"resource", "namespace", "pod"}},
		Usage:       Text{"OOM 保护读取该指标；未映射时只用 OOM 前工作集。", "The OOM protection reads it; unmapped, only the working set before the kill is used."},
		Limitations: Text{"合计值，单个容器的 limit 要乘副本数还原。", "A sum over pods; divide by the replica count for one container."},
	},
	{
		ID: "cadvisor.cpu.throttled", Metric: "k8s_cpu_throttle", Level: "workload", Unit: "seconds", StepSeconds: 60,
		Name:        Text{"CPU 被限流时长（cAdvisor）", "CPU throttled time (cAdvisor)"},
		Scenario:    Text{"窗口内容器因 CPU limit 被限流的总秒数，说明 limits 是否偏紧。", "Seconds containers were throttled by their CPU limit within the window; shows whether limits are too tight."},
		Query:       `sum(increase(container_cpu_cfs_throttled_seconds_total{` + workloadPods + `,` + cadvisorBase + `}[{{range}}]))`,
		Source:      Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_cpu_cfs_throttled_seconds_total"}, Labels: []string{"namespace", "pod", "container"}},
		Usage:       Text{"作协变量，限流常使 CPU 使用量被低估。", "A covariate: throttling often makes the observed CPU usage understate the demand."},
		Limitations: Text{"没有设置 CPU limit 的容器不会被限流，值恒为 0。", "Containers without a CPU limit are never throttled, so the value stays 0."},
	},
	{
		ID: "dcgm.gpu.utilization", Metric: "gpu", Level: "workload", Unit: "gpus", StepSeconds: 60,
		Name:        Text{"GPU 使用卡数（DCGM）", "GPU cards in use (DCGM)"},
		Scenario:    Text{"工作负载各 Pod 的 GPU 利用率之和除以 100，即平均占用的整卡数。", "The GPU utilization of the workload's pods summed and divided by 100: the whole cards kept busy."},
		Query:       `sum(avg_over_time(DCGM_FI_DEV_GPU_UTIL{` + workloadPods + `}[{{range}}])) / 100`,
		Source:      Source{Component: "NVIDIA DCGM exporter", Metrics: []string{"DCGM_FI_DEV_GPU_UTIL"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"GPU 作为独立资源预测，与 CPU、内存互不影响；建议值按整卡取整。", "GPU is forecast as a resource of its own, independent of CPU and memory; the recommendation is rounded up to whole cards."},
		Limitations: Text{"需要 DCGM exporter 给指标打上 namespace 和 pod 标签；GPU 只对新 Pod 通过准入生效。", "The DCGM exporter must label the series with namespace and pod; GPU applies to new Pods through admission only."},
	},
	{
		ID: "dcgm.gpu.memory-used", Metric: "gpu_memory", Level: "workload", Unit: "bytes", StepSeconds: 60,
		Name:        Text{"GPU 显存使用量（DCGM）", "GPU memory used (DCGM)"},
		Scenario:    Text{"工作负载各 Pod 占用的显存总量。", "The GPU memory used by the pods of the workload."},
		Query:       `sum(max_over_time(DCGM_FI_DEV_FB_USED{` + workloadPods + `}[{{range}}])) * 1048576`,
		Source:      Source{Component: "NVIDIA DCGM exporter", Metrics: []string{"DCGM_FI_DEV_FB_USED"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"作为 GPU 资源的一个指标来源，或只作压力信号。", "A source of the GPU resource, or a pressure signal only."},
		Limitations: Text{"DCGM 以 MiB 计，模板已换算为字节。", "DCGM reports MiB; the template converts to bytes."},
	},
	{
		ID: "cadvisor.network.receive", Metric: "network_rx", Level: "workload", Unit: "bytes_per_second", StepSeconds: 60,
		Name:        Text{"网络接收速率（cAdvisor）", "Network receive rate (cAdvisor)"},
		Scenario:    Text{"工作负载各 Pod 每秒接收的字节数。", "Bytes per second received by the pods of the workload."},
		Query:       `sum(rate(container_network_receive_bytes_total{` + workloadPods + `}[{{range}}]))`,
		Source:      Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_network_receive_bytes_total"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"作为压力信号参与副本计算，需给来源设置阈值。", "A pressure signal for the replica calculation; give its source a threshold."},
		Limitations: Text{"hostNetwork 的 Pod 没有独立的网络指标。", "Pods on the host network have no network series of their own."},
	},
	{
		ID: "cadvisor.network.transmit", Metric: "network_tx", Level: "workload", Unit: "bytes_per_second", StepSeconds: 60,
		Name:        Text{"网络发送速率（cAdvisor）", "Network transmit rate (cAdvisor)"},
		Scenario:    Text{"工作负载各 Pod 每秒发送的字节数。", "Bytes per second sent by the pods of the workload."},
		Query:       `sum(rate(container_network_transmit_bytes_total{` + workloadPods + `}[{{range}}]))`,
		Source:      Source{Component: "cAdvisor (kubelet)", Metrics: []string{"container_network_transmit_bytes_total"}, Labels: []string{"namespace", "pod"}},
		Usage:       Text{"作为压力信号参与副本计算，需给来源设置阈值。", "A pressure signal for the replica calculation; give its source a threshold."},
		Limitations: Text{"hostNetwork 的 Pod 没有独立的网络指标。", "Pods on the host network have no network series of their own."},
	},
}

func init() {
	sort.SliceStable(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
}

// All returns the presets ordered by id.
func All() []Preset { return append([]Preset(nil), presets...) }

// Get returns the preset with the id.
func Get(id string) (Preset, error) {
	for _, p := range presets {
		if p.ID == id {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("unknown preset %q", id)
}
