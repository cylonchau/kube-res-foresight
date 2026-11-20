package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The operator reads the cluster, read-only, for the platform's resource pages (P10 A1): the nodes, and the Deployment, StatefulSet or DaemonSet of every
// module of the platform with its Pods, the requests and limits of its containers, and the HPA or KEDA object that scales it. It reads no other Pod and no
// other workload: what is read is what the platform's modules name. It reads through the API reader (not the cache of the manager) so that no informer is
// started over every Pod of the cluster for a read that happens every few minutes.

// InventoryTargets reads the modules the platform wants read (GET /operator/inventory-targets): every enabled module, whatever its mode.
func (d *PlatformDecider) InventoryTargets(ctx context.Context) ([]inventory.Target, error) {
	data, err := d.do(ctx, http.MethodGet, "/api/v1/operator/inventory-targets", url.Values{}, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Targets []inventory.Target `json:"targets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, unreachable(fmt.Errorf("unreadable platform response: %w", err))
	}
	if env.Code != 10000 {
		return nil, unreachable(fmt.Errorf("platform error %d: %s", env.Code, env.Msg))
	}
	return env.Data.Targets, nil
}

// ReportInventory implements InventoryReporterSink: one report to the platform.
func (d *PlatformDecider) ReportInventory(ctx context.Context, s inventory.Snapshot) error {
	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	data, err := d.do(ctx, http.MethodPost, "/api/v1/operator/inventory", url.Values{}, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(data, &env) == nil && env.Code != 0 && env.Code != 10000 {
		return fmt.Errorf("platform error %d: %s", env.Code, env.Msg)
	}
	return nil
}

// InventoryReader reads the cluster.
type InventoryReader struct {
	// Reader is the API reader of the manager: it reads from the API server, not from a cache.
	Reader client.Reader
}

// Node labels that say where a node is and what it is.
var (
	regionLabels   = []string{"topology.kubernetes.io/region", "failure-domain.beta.kubernetes.io/region"}
	zoneLabels     = []string{"topology.kubernetes.io/zone", "failure-domain.beta.kubernetes.io/zone"}
	instanceLabels = []string{"node.kubernetes.io/instance-type", "beta.kubernetes.io/instance-type"}
)

func firstLabel(labels map[string]string, keys []string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

func capacityOf(list corev1.ResourceList) inventory.Capacity {
	return inventory.Capacity{CPUMilli: list.Cpu().MilliValue(), MemoryBytes: list.Memory().Value(), Pods: list.Pods().Value()}
}

func resourcesOf(list corev1.ResourceList) inventory.Resources {
	return inventory.Resources{CPUMilli: list.Cpu().MilliValue(), MemoryBytes: list.Memory().Value()}
}

// Nodes reads every node.
func (r *InventoryReader) Nodes(ctx context.Context) ([]inventory.Node, error) {
	var list corev1.NodeList
	if err := r.Reader.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]inventory.Node, 0, len(list.Items))
	for _, n := range list.Items {
		node := inventory.Node{Name: n.Name, Unschedulable: n.Spec.Unschedulable, Provider: inventory.ProviderOf(n.Spec.ProviderID), Region: firstLabel(n.Labels, regionLabels),
			Zone: firstLabel(n.Labels, zoneLabels), InstanceType: firstLabel(n.Labels, instanceLabels), KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			Capacity: capacityOf(n.Status.Capacity), Allocatable: capacityOf(n.Status.Allocatable)}
		for _, c := range n.Status.Conditions {
			switch c.Type {
			case corev1.NodeReady:
				node.Ready = c.Status == corev1.ConditionTrue
			case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
				if c.Status == corev1.ConditionTrue {
					node.Pressure = append(node.Pressure, string(c.Type))
				}
			}
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// templateOf is what the three kinds of workload have in common: the replicas, the selector, the Pod template and what the status says.
type templateOf struct {
	selector   *metav1.LabelSelector
	containers []corev1.Container
	desired    int32
	ready      int32
	available  int32
	updated    int32
	generation int64
}

func (r *InventoryReader) workload(ctx context.Context, t inventory.Target) (templateOf, error) {
	key := client.ObjectKey{Namespace: t.Namespace, Name: t.Workload}
	switch t.Kind {
	case "StatefulSet":
		var o appsv1.StatefulSet
		if err := r.Reader.Get(ctx, key, &o); err != nil {
			return templateOf{}, err
		}
		desired := int32(1)
		if o.Spec.Replicas != nil {
			desired = *o.Spec.Replicas
		}
		return templateOf{o.Spec.Selector, o.Spec.Template.Spec.Containers, desired, o.Status.ReadyReplicas, o.Status.AvailableReplicas, o.Status.UpdatedReplicas, o.Generation}, nil
	case "DaemonSet":
		var o appsv1.DaemonSet
		if err := r.Reader.Get(ctx, key, &o); err != nil {
			return templateOf{}, err
		}
		return templateOf{o.Spec.Selector, o.Spec.Template.Spec.Containers, o.Status.DesiredNumberScheduled, o.Status.NumberReady, o.Status.NumberAvailable, o.Status.UpdatedNumberScheduled, o.Generation}, nil
	default:
		var o appsv1.Deployment
		if err := r.Reader.Get(ctx, key, &o); err != nil {
			return templateOf{}, err
		}
		desired := int32(1)
		if o.Spec.Replicas != nil {
			desired = *o.Spec.Replicas
		}
		return templateOf{o.Spec.Selector, o.Spec.Template.Spec.Containers, desired, o.Status.ReadyReplicas, o.Status.AvailableReplicas, o.Status.UpdatedReplicas, o.Generation}, nil
	}
}

// Workload reads the workload of one module. A workload that is not there is Missing; one that cannot be read has the reason in Error; neither stops the
// others.
func (r *InventoryReader) Workload(ctx context.Context, t inventory.Target) (inventory.Workload, []corev1.Pod) {
	kind := t.Kind
	if kind == "" {
		kind = "Deployment"
	}
	out := inventory.Workload{Module: t.Name, Namespace: t.Namespace, Kind: kind, Name: t.Workload, Containers: []inventory.Container{}, Pods: []inventory.Pod{}}
	t.Kind = kind
	tpl, err := r.workload(ctx, t)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			out.Missing = true
		} else {
			out.Error = truncate(err.Error(), 280)
		}
		return out, nil
	}
	out.Desired, out.Ready, out.Available, out.Updated, out.Generation = tpl.desired, tpl.ready, tpl.available, tpl.updated, tpl.generation
	for _, c := range tpl.containers {
		out.Containers = append(out.Containers, inventory.Container{Name: c.Name, Requests: resourcesOf(c.Resources.Requests), Limits: resourcesOf(c.Resources.Limits)})
	}
	var pods []corev1.Pod
	if tpl.selector != nil {
		sel, err := metav1.LabelSelectorAsSelector(tpl.selector)
		if err == nil && !sel.Empty() {
			var list corev1.PodList
			if err := r.Reader.List(ctx, &list, client.InNamespace(t.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
				out.Error = truncate("pods: "+err.Error(), 280)
			} else {
				pods = list.Items
			}
		}
	}
	for _, p := range pods {
		if p.DeletionTimestamp != nil {
			continue
		}
		pod := inventory.Pod{Name: p.Name, Node: p.Spec.NodeName, Phase: string(p.Status.Phase)}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady {
				pod.Ready = c.Status == corev1.ConditionTrue
			}
		}
		for _, cs := range p.Status.ContainerStatuses {
			pod.Restarts += int(cs.RestartCount)
		}
		if p.Status.StartTime != nil {
			at := p.Status.StartTime.Time.UTC()
			pod.StartedAt = &at
		}
		out.Pods = append(out.Pods, pod)
	}
	sort.Slice(out.Pods, func(i, j int) bool { return out.Pods[i].Name < out.Pods[j].Name })
	out.HPA = r.autoscaler(ctx, t)
	return out, pods
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// behaviorOf writes the scaling behavior of an HPA as a line.
func behaviorOf(b *autoscalingv2.HorizontalPodAutoscalerBehavior) string {
	if b == nil {
		return ""
	}
	var parts []string
	window := func(name string, r *autoscalingv2.HPAScalingRules) {
		if r != nil && r.StabilizationWindowSeconds != nil {
			parts = append(parts, fmt.Sprintf("%s stabilization %ds", name, *r.StabilizationWindowSeconds))
		}
		if r != nil && len(r.Policies) > 0 {
			parts = append(parts, fmt.Sprintf("%s %d policies", name, len(r.Policies)))
		}
	}
	window("scaleUp", b.ScaleUp)
	window("scaleDown", b.ScaleDown)
	return strings.Join(parts, ", ")
}

// autoscaler finds the HPA or the KEDA ScaledObject that scales the workload, if there is one. A cluster without KEDA is no error.
func (r *InventoryReader) autoscaler(ctx context.Context, t inventory.Target) *inventory.HPA {
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := r.Reader.List(ctx, &hpas, client.InNamespace(t.Namespace)); err == nil {
		for _, h := range hpas.Items {
			ref := h.Spec.ScaleTargetRef
			if ref.Name == t.Workload && (ref.Kind == "" || ref.Kind == t.Kind) {
				min := int32(1)
				if h.Spec.MinReplicas != nil {
					min = *h.Spec.MinReplicas
				}
				return &inventory.HPA{Kind: "hpa", Name: h.Name, Min: min, Max: h.Spec.MaxReplicas, Current: h.Status.CurrentReplicas, Desired: h.Status.DesiredReplicas,
					Behavior: behaviorOf(h.Spec.Behavior)}
			}
		}
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(scaledObjectList)
	if err := r.Reader.List(ctx, list, client.InNamespace(t.Namespace)); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return nil
	}
	for _, o := range list.Items {
		name, _, _ := unstructured.NestedString(o.Object, "spec", "scaleTargetRef", "name")
		kind, _, _ := unstructured.NestedString(o.Object, "spec", "scaleTargetRef", "kind")
		if name != t.Workload || (kind != "" && kind != t.Kind) {
			continue
		}
		min, _, _ := unstructured.NestedInt64(o.Object, "spec", "minReplicaCount")
		max, found, _ := unstructured.NestedInt64(o.Object, "spec", "maxReplicaCount")
		if !found {
			max = 100 // the default of KEDA
		}
		current, _, _ := unstructured.NestedInt64(o.Object, "status", "originalReplicaCount")
		return &inventory.HPA{Kind: "keda", Name: o.GetName(), Min: int32(min), Max: int32(max), Current: int32(current), Desired: int32(current)}
	}
	return nil
}

// Read reads the nodes and the workload of every target. A node list that cannot be read is told in NodesError, and the workloads are still reported.
func (r *InventoryReader) Read(ctx context.Context, targets []inventory.Target, at time.Time) inventory.Snapshot {
	snap := inventory.Snapshot{ObservedAt: at.UTC(), Nodes: []inventory.Node{}, Workloads: []inventory.Workload{}}
	nodes, err := r.Nodes(ctx)
	if err != nil {
		snap.NodesError = truncate(err.Error(), 280)
	}
	index := map[string]int{}
	for i := range nodes {
		index[nodes[i].Name] = i
	}
	for _, t := range targets {
		w, pods := r.Workload(ctx, t)
		snap.Workloads = append(snap.Workloads, w)
		for _, p := range pods {
			i, ok := index[p.Spec.NodeName]
			if !ok || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			n := &nodes[i]
			n.Pods++
			req, lim := effectiveResources(p)
			n.Requests.CPUMilli += req.CPUMilli
			n.Requests.MemoryBytes += req.MemoryBytes
			n.Limits.CPUMilli += lim.CPUMilli
			n.Limits.MemoryBytes += lim.MemoryBytes
		}
	}
	snap.Nodes = nodes
	if snap.Nodes == nil {
		snap.Nodes = []inventory.Node{}
	}
	return snap
}

// effectiveResources is what a Pod asks for as the scheduler reads it: the sum over its containers, and for each resource the larger of that and the biggest
// init container.
func effectiveResources(p corev1.Pod) (req, lim inventory.Resources) {
	for _, c := range p.Spec.Containers {
		r, l := resourcesOf(c.Resources.Requests), resourcesOf(c.Resources.Limits)
		req.CPUMilli += r.CPUMilli
		req.MemoryBytes += r.MemoryBytes
		lim.CPUMilli += l.CPUMilli
		lim.MemoryBytes += l.MemoryBytes
	}
	for _, c := range p.Spec.InitContainers {
		r, l := resourcesOf(c.Resources.Requests), resourcesOf(c.Resources.Limits)
		req.CPUMilli = max(req.CPUMilli, r.CPUMilli)
		req.MemoryBytes = max(req.MemoryBytes, r.MemoryBytes)
		lim.CPUMilli = max(lim.CPUMilli, l.CPUMilli)
		lim.MemoryBytes = max(lim.MemoryBytes, l.MemoryBytes)
	}
	return req, lim
}

// DefaultInventoryInterval is how often the cluster is read; DefaultInventoryForce how long it may go without a report even when nothing changed (the
// history of the replicas and of the nodes is made of these).
const (
	DefaultInventoryInterval = 5 * time.Minute
	DefaultInventoryForce    = time.Hour
)

// InventoryTargets lists the modules to read.
type InventoryTargetSource interface {
	InventoryTargets(ctx context.Context) ([]inventory.Target, error)
}

// InventoryQueue takes a report of the inventory to deliver.
type InventoryQueue interface {
	ReportInventory(ctx context.Context, s inventory.Snapshot) error
}

// InventoryRunner reads the cluster every Interval and queues a report when something that is configuration changed, or when Force has passed since the last
// report. It reads on the elected replica only: one report, not one for each replica.
type InventoryRunner struct {
	Reader   *InventoryReader
	Targets  InventoryTargetSource
	Queue    InventoryQueue
	Interval time.Duration
	Force    time.Duration
	Now      func() time.Time

	mu       sync.Mutex
	last     string
	lastSent time.Time
	known    []inventory.Target
}

// NeedLeaderElection keeps the reading on the elected replica.
func (r *InventoryRunner) NeedLeaderElection() bool { return true }

func (r *InventoryRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Start runs until ctx ends.
func (r *InventoryRunner) Start(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInventoryInterval
	}
	logf.FromContext(ctx).WithName("inventory").Info("reading the nodes and the workloads of the modules, read-only", "interval", interval.String())
	for {
		r.Once(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// Once reads once and queues a report if there is one to make. It returns whether it did.
func (r *InventoryRunner) Once(ctx context.Context) bool {
	log := logf.FromContext(ctx).WithName("inventory")
	targets, err := r.Targets.InventoryTargets(ctx)
	r.mu.Lock()
	if err != nil {
		// the platform cannot be reached: the cluster is read for the modules it last named, and the report waits in the outbox
		targets = r.known
		if len(targets) == 0 {
			r.mu.Unlock()
			log.Info("the modules to read are not known yet: the platform has not answered", "error", err.Error())
			return false
		}
	} else {
		r.known = targets
	}
	r.mu.Unlock()
	snap := r.Reader.Read(ctx, targets, r.now())
	if snap.NodesError != "" {
		log.Info("the nodes could not be read: the reports have no nodes", "error", snap.NodesError)
	}
	fp := inventory.Fingerprint(snap)
	force := r.Force
	if force <= 0 {
		force = DefaultInventoryForce
	}
	r.mu.Lock()
	due := fp != r.last || r.now().Sub(r.lastSent) >= force
	r.mu.Unlock()
	if !due {
		return false
	}
	if err := r.Queue.ReportInventory(ctx, snap); err != nil {
		log.Info("could not queue the inventory report", "error", err.Error())
		return false
	}
	r.mu.Lock()
	r.last, r.lastSent = fp, r.now()
	r.mu.Unlock()
	return true
}
