package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What the operator makes of a watch of the nodes, the Pods, the workloads and the HPAs: the changes of state (P12 E7, E8). Everything here is a pure function of the
// objects the operator was given by its cache; it reads nothing more and changes nothing. A change carries the reasons the object itself gives and the configuration it
// had at that moment (the cache still has the last one of an object that was deleted); the usage is the platform's to ask of Prometheus.

// PodLister gives the Pods that are on a node (the operator's cache, indexed by node).
type PodLister interface {
	PodsOnNode(node string) []*corev1.Pod
}

// detector makes changes out of the old and the new state of an object.
type detector struct {
	now  func() time.Time
	pods PodLister
}

func (d *detector) at() time.Time {
	if d.now != nil {
		return d.now().UTC()
	}
	return time.Now().UTC()
}

// idOf is the id of a change: the same for the same change however often it is made (the object, what changed, to what, and when).
func idOf(kind string, uid string, typ, to string, at time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{kind, uid, typ, to, strconv.FormatInt(at.UnixNano(), 10)}, "|")))
	return hex.EncodeToString(sum[:])[:40]
}

func resOf(list corev1.ResourceList) clusteragent.Res {
	return clusteragent.Res{CPUMilli: list.Cpu().MilliValue(), MemoryBytes: list.Memory().Value()}
}

// podResources is what the containers of a Pod ask for and are limited to (the init containers are not counted).
func podResources(spec *corev1.PodSpec) (req, lim clusteragent.Res) {
	for _, c := range spec.Containers {
		r, l := resOf(c.Resources.Requests), resOf(c.Resources.Limits)
		req.CPUMilli += r.CPUMilli
		req.MemoryBytes += r.MemoryBytes
		lim.CPUMilli += l.CPUMilli
		lim.MemoryBytes += l.MemoryBytes
	}
	return
}

// workloadOf is the workload a Pod belongs to, by what owns it: a ReplicaSet is named after its Deployment and a hash, so the hash goes; a StatefulSet, a DaemonSet or a
// Job is its own name; a Pod no controller owns is its own workload. The name is also the way the platform's queries find the Pods of it, so a guess that is wrong gives no
// series and a figure that is not found, never a wrong one.
func workloadOf(pod *corev1.Pod) (owner, workload string) {
	for _, o := range pod.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		owner = o.Kind + "/" + o.Name
		if o.Kind == "ReplicaSet" {
			if i := strings.LastIndex(o.Name, "-"); i > 0 {
				return owner, o.Name[:i]
			}
		}
		return owner, o.Name
	}
	return "", pod.Name
}

func i32(v int32) *int32 { return &v }

func condOf(conds []corev1.NodeCondition, t corev1.NodeConditionType) (corev1.NodeCondition, bool) {
	for _, c := range conds {
		if c.Type == t {
			return c, true
		}
	}
	return corev1.NodeCondition{}, false
}

func podCond(conds []corev1.PodCondition, t corev1.PodConditionType) (corev1.PodCondition, bool) {
	for _, c := range conds {
		if c.Type == t {
			return c, true
		}
	}
	return corev1.PodCondition{}, false
}

// timeOr is the time an object gives for a change, or else the time the operator saw it.
func timeOr(t metav1.Time, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t.UTC()
}

func taintText(t corev1.Taint) string {
	v := t.Key
	if t.Value != "" {
		v += "=" + t.Value
	}
	return v + ":" + string(t.Effect)
}

// ---- nodes

func (d *detector) nodeSnapshot(n *corev1.Node) *clusteragent.Snapshot {
	capacity, alloc := resOf(n.Status.Capacity), resOf(n.Status.Allocatable)
	s := &clusteragent.Snapshot{Capacity: &capacity, Allocatable: &alloc, CapacityPods: n.Status.Capacity.Pods().Value()}
	if len(n.Labels) > 0 {
		s.Labels = map[string]string{}
		keys := make([]string, 0, len(n.Labels))
		for k := range n.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(s.Labels) >= 50 {
				break
			}
			s.Labels[k] = clusteragent.Clip(n.Labels[k], 128)
		}
	}
	for _, t := range n.Spec.Taints {
		s.Taints = append(s.Taints, taintText(t))
	}
	if d.pods != nil {
		pods := d.pods.PodsOnNode(n.Name)
		sort.Slice(pods, func(i, j int) bool { return pods[i].Namespace+"/"+pods[i].Name < pods[j].Namespace+"/"+pods[j].Name })
		s.PodCount = len(pods)
		for i, p := range pods {
			if i >= clusteragent.MaxSnapshotPods {
				break
			}
			req, lim := podResources(&p.Spec)
			_, wl := workloadOf(p)
			s.Pods = append(s.Pods, clusteragent.PodRef{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Workload: wl, Requests: req, Limits: lim})
		}
	}
	return s
}

func (d *detector) nodeChange(n *corev1.Node, typ, from, to string, at time.Time, reasons []clusteragent.Reason) clusteragent.Change {
	now := d.at()
	return clusteragent.Change{RecordID: idOf(clusteragent.KindNode, string(n.UID), typ, to+"|"+from, at), Kind: clusteragent.KindNode, Name: n.Name, ObjectUID: string(n.UID),
		Type: typ, From: from, To: to, OccurredAt: at, ObservedAt: now, Reasons: reasons, Snapshot: d.nodeSnapshot(n)}
}

func nodeConditionReason(c corev1.NodeCondition) []clusteragent.Reason {
	if c.Reason == "" && c.Message == "" {
		return nil
	}
	return []clusteragent.Reason{{Source: clusteragent.SourceNodeCondition, Reason: c.Reason, Message: c.Message, Component: string(c.Type)}}
}

// nodeChanges are the changes between two states of a node: Ready, the pressures, whether it takes new Pods, and its taints.
func (d *detector) nodeChanges(old, n *corev1.Node) []clusteragent.Change {
	var out []clusteragent.Change
	now := d.at()
	if oc, ok := condOf(old.Status.Conditions, corev1.NodeReady); ok {
		if nc, ok := condOf(n.Status.Conditions, corev1.NodeReady); ok && nc.Status != oc.Status {
			out = append(out, d.nodeChange(n, "ready", string(oc.Status), string(nc.Status), timeOr(nc.LastTransitionTime, now), nodeConditionReason(nc)))
		}
	}
	for _, t := range []corev1.NodeConditionType{corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable} {
		oc, ok1 := condOf(old.Status.Conditions, t)
		nc, ok2 := condOf(n.Status.Conditions, t)
		if ok1 && ok2 && nc.Status != oc.Status {
			out = append(out, d.nodeChange(n, "pressure", string(t)+":"+string(oc.Status), string(t)+":"+string(nc.Status), timeOr(nc.LastTransitionTime, now), nodeConditionReason(nc)))
		}
	}
	if old.Spec.Unschedulable != n.Spec.Unschedulable {
		out = append(out, d.nodeChange(n, "unschedulable", strconv.FormatBool(old.Spec.Unschedulable), strconv.FormatBool(n.Spec.Unschedulable), now, nil))
	}
	oldT, newT := map[string]corev1.Taint{}, map[string]corev1.Taint{}
	for _, t := range old.Spec.Taints {
		oldT[taintText(t)] = t
	}
	for _, t := range n.Spec.Taints {
		newT[taintText(t)] = t
	}
	var added, removed []string
	for k := range newT {
		if _, ok := oldT[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range oldT {
		if _, ok := newT[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	for _, k := range added {
		t := newT[k]
		out = append(out, d.nodeChange(n, "taint", "", "+"+k, timeOr(taintTime(t), now), []clusteragent.Reason{{Source: clusteragent.SourceTaint, Reason: t.Key, Message: string(t.Effect) + valueNote(t.Value)}}))
	}
	for _, k := range removed {
		t := oldT[k]
		out = append(out, d.nodeChange(n, "taint", "-"+k, "", now, []clusteragent.Reason{{Source: clusteragent.SourceTaint, Reason: t.Key, Message: string(t.Effect) + valueNote(t.Value)}}))
	}
	return out
}

func valueNote(v string) string {
	if v == "" {
		return ""
	}
	return " " + v
}

func taintTime(t corev1.Taint) metav1.Time {
	if t.TimeAdded != nil {
		return *t.TimeAdded
	}
	return metav1.Time{}
}

// nodeCreated is a node that came into the cluster (not one that was already there when the operator started).
func (d *detector) nodeCreated(n *corev1.Node) clusteragent.Change {
	return d.nodeChange(n, "created", "", "node", timeOr(n.CreationTimestamp, d.at()), nil)
}

// nodeDeleted is a node that is gone, with the state it was last seen in: its conditions that are not as they should be and its taints are the reasons that were there.
func (d *detector) nodeDeleted(n *corev1.Node) clusteragent.Change {
	var reasons []clusteragent.Reason
	for _, c := range n.Status.Conditions {
		bad := (c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue) || (c.Type != corev1.NodeReady && c.Status == corev1.ConditionTrue)
		if bad {
			reasons = append(reasons, nodeConditionReason(c)...)
		}
	}
	for _, t := range n.Spec.Taints {
		reasons = append(reasons, clusteragent.Reason{Source: clusteragent.SourceTaint, Reason: t.Key, Message: string(t.Effect) + valueNote(t.Value)})
	}
	return d.nodeChange(n, "deleted", "node", "", d.at(), reasons)
}

// ---- Pods

func podContainerStates(p *corev1.Pod) []clusteragent.ContainerState {
	out := make([]clusteragent.ContainerState, 0, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		s := clusteragent.ContainerState{Name: cs.Name, Restarts: cs.RestartCount}
		switch {
		case cs.State.Waiting != nil:
			s.State, s.Reason, s.Message = "waiting", cs.State.Waiting.Reason, clusteragent.Clip(cs.State.Waiting.Message, 512)
		case cs.State.Terminated != nil:
			s.State, s.Reason, s.Message, s.ExitCode = "terminated", cs.State.Terminated.Reason, clusteragent.Clip(cs.State.Terminated.Message, 512), cs.State.Terminated.ExitCode
		case cs.State.Running != nil:
			s.State = "running"
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			s.LastReason, s.LastExitCode = t.Reason, t.ExitCode
		}
		out = append(out, s)
	}
	return out
}

func (d *detector) podSnapshot(p *corev1.Pod) *clusteragent.Snapshot {
	req, lim := podResources(&p.Spec)
	owner, _ := workloadOf(p)
	return &clusteragent.Snapshot{Requests: &req, Limits: &lim, Owner: owner, Containers: podContainerStates(p)}
}

func (d *detector) podChange(p *corev1.Pod, typ, from, to string, at time.Time, reasons []clusteragent.Reason) clusteragent.Change {
	_, wl := workloadOf(p)
	return clusteragent.Change{RecordID: idOf(clusteragent.KindPod, string(p.UID), typ, to+"|"+from, at), Kind: clusteragent.KindPod, Namespace: p.Namespace, Name: p.Name,
		ObjectUID: string(p.UID), Type: typ, From: from, To: to, OccurredAt: at, ObservedAt: d.at(), Node: p.Spec.NodeName, Workload: wl, Reasons: reasons, Snapshot: d.podSnapshot(p)}
}

func podConditionReason(c corev1.PodCondition) []clusteragent.Reason {
	if c.Reason == "" && c.Message == "" {
		return nil
	}
	return []clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Reason: c.Reason, Message: c.Message, Component: string(c.Type)}}
}

func podStatusReason(p *corev1.Pod) []clusteragent.Reason {
	if p.Status.Reason == "" && p.Status.Message == "" {
		return nil
	}
	return []clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Reason: p.Status.Reason, Message: clusteragent.Clip(p.Status.Message, 512), Component: "pod"}}
}

func containerOf(p *corev1.Pod, name string) (corev1.ContainerStatus, bool) {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == name {
			return cs, true
		}
	}
	return corev1.ContainerStatus{}, false
}

// podChanges are the changes between two states of a Pod: where it is scheduled, its phase and readiness, how its containers end and wait, why it has a status, and that it is
// being deleted.
func (d *detector) podChanges(old, p *corev1.Pod) []clusteragent.Change {
	var out []clusteragent.Change
	now := d.at()
	if old.Spec.NodeName != p.Spec.NodeName {
		out = append(out, d.podChange(p, "node", old.Spec.NodeName, p.Spec.NodeName, now, nil))
	}
	if old.Status.Phase != p.Status.Phase {
		out = append(out, d.podChange(p, "phase", string(old.Status.Phase), string(p.Status.Phase), now, podStatusReason(p)))
	}
	if oc, ok := podCond(old.Status.Conditions, corev1.PodScheduled); ok {
		if nc, ok := podCond(p.Status.Conditions, corev1.PodScheduled); ok && nc.Status != oc.Status {
			out = append(out, d.podChange(p, "scheduled", string(oc.Status), string(nc.Status), timeOr(nc.LastTransitionTime, now), podConditionReason(nc)))
		}
	} else if nc, ok := podCond(p.Status.Conditions, corev1.PodScheduled); ok && nc.Status == corev1.ConditionFalse {
		out = append(out, d.podChange(p, "scheduled", "", string(nc.Status), timeOr(nc.LastTransitionTime, now), podConditionReason(nc)))
	}
	if oc, ok := podCond(old.Status.Conditions, corev1.PodReady); ok {
		if nc, ok := podCond(p.Status.Conditions, corev1.PodReady); ok && nc.Status != oc.Status {
			out = append(out, d.podChange(p, "ready", string(oc.Status), string(nc.Status), timeOr(nc.LastTransitionTime, now), podConditionReason(nc)))
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		oc, had := containerOf(old, cs.Name)
		if had && cs.RestartCount > oc.RestartCount {
			at, reasons := now, []clusteragent.Reason(nil)
			if t := cs.LastTerminationState.Terminated; t != nil {
				at = timeOr(t.FinishedAt, now)
				reasons = []clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Reason: t.Reason, Message: clusteragent.Clip(fmt.Sprintf("exit code %d %s", t.ExitCode, t.Message), 512), Component: cs.Name}}
			}
			out = append(out, d.podChange(p, "restart", strconv.Itoa(int(oc.RestartCount)), strconv.Itoa(int(cs.RestartCount)), at, reasons))
		}
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			prev := ""
			if had && oc.State.Waiting != nil {
				prev = oc.State.Waiting.Reason
			}
			if prev != w.Reason {
				out = append(out, d.podChange(p, "waiting", prev, w.Reason, now,
					[]clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Reason: w.Reason, Message: clusteragent.Clip(w.Message, 512), Component: cs.Name}}))
			}
		}
	}
	if p.Status.Reason != "" && p.Status.Reason != old.Status.Reason {
		out = append(out, d.podChange(p, "status", old.Status.Reason, p.Status.Reason, now, podStatusReason(p)))
	}
	if old.DeletionTimestamp == nil && p.DeletionTimestamp != nil {
		msg := "the Pod is being deleted"
		if p.DeletionGracePeriodSeconds != nil {
			msg = fmt.Sprintf("the Pod is being deleted, grace period %ds", *p.DeletionGracePeriodSeconds)
		}
		out = append(out, d.podChange(p, "terminating", "", "terminating", timeOr(*p.DeletionTimestamp, now), []clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Message: msg}}))
	}
	return out
}

func (d *detector) podCreated(p *corev1.Pod) clusteragent.Change {
	return d.podChange(p, "created", "", "pod", timeOr(p.CreationTimestamp, d.at()), nil)
}

// podDeleted is a Pod that is gone, with the state it was last in: how its containers last ended is the reason that was there.
func (d *detector) podDeleted(p *corev1.Pod) clusteragent.Change {
	reasons := podStatusReason(p)
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason != "" {
			reasons = append(reasons, clusteragent.Reason{Source: clusteragent.SourcePodStatus, Reason: t.Reason, Message: clusteragent.Clip(fmt.Sprintf("exit code %d", t.ExitCode), 64), Component: cs.Name})
		}
	}
	return d.podChange(p, "deleted", "pod", "", d.at(), reasons)
}

// ---- workloads (the replicas of a Deployment or a StatefulSet)

// replicaWriter is who last wrote spec.replicas, read from the managed fields of the object: the manager and the operation of the newest entry that owns the field. It is
// what Kubernetes records, and not a guess: a manager name says who (kube-controller-manager for the HPA controller, kubectl, a GitOps tool, this operator) and an object
// whose entries do not own the field says nothing.
func replicaWriter(fields []metav1.ManagedFieldsEntry) []clusteragent.Reason {
	var best *metav1.ManagedFieldsEntry
	for i := range fields {
		f := &fields[i]
		if f.FieldsV1 == nil || !ownsReplicas(f.FieldsV1.Raw) {
			continue
		}
		if best == nil || (f.Time != nil && (best.Time == nil || f.Time.After(best.Time.Time))) {
			best = f
		}
	}
	if best == nil {
		return nil
	}
	note := "the last writer of spec.replicas"
	if best.Subresource != "" {
		note += " (through " + best.Subresource + ")"
	}
	return []clusteragent.Reason{{Source: clusteragent.SourceManagedFields, Component: best.Manager, Reason: string(best.Operation), Message: note}}
}

func ownsReplicas(raw []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	spec, ok := root["f:spec"]
	if !ok {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(spec, &fields) != nil {
		return false
	}
	_, ok = fields["f:replicas"]
	return ok
}

// workloadView is the part of a Deployment or a StatefulSet that matters here.
type workloadView struct {
	kind, namespace, name string
	uid                   string
	desired               int32
	ready, available      int32
	fields                []metav1.ManagedFieldsEntry
	created               metav1.Time
	requests, limits      clusteragent.Res
}

func viewOf(obj any) (workloadView, bool) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		v := workloadView{kind: "Deployment", namespace: o.Namespace, name: o.Name, uid: string(o.UID), ready: o.Status.ReadyReplicas, available: o.Status.AvailableReplicas, fields: o.ManagedFields, created: o.CreationTimestamp}
		if o.Spec.Replicas != nil {
			v.desired = *o.Spec.Replicas
		} else {
			v.desired = 1
		}
		v.requests, v.limits = podResources(&o.Spec.Template.Spec)
		return v, true
	case *appsv1.StatefulSet:
		v := workloadView{kind: "StatefulSet", namespace: o.Namespace, name: o.Name, uid: string(o.UID), ready: o.Status.ReadyReplicas, available: o.Status.AvailableReplicas, fields: o.ManagedFields, created: o.CreationTimestamp}
		if o.Spec.Replicas != nil {
			v.desired = *o.Spec.Replicas
		} else {
			v.desired = 1
		}
		v.requests, v.limits = podResources(&o.Spec.Template.Spec)
		return v, true
	}
	return workloadView{}, false
}

func (d *detector) workloadChange(v workloadView, typ, from, to string, at time.Time, reasons []clusteragent.Reason) clusteragent.Change {
	req, lim := v.requests, v.limits
	return clusteragent.Change{RecordID: idOf(clusteragent.KindWorkload, v.uid, typ, to+"|"+from, at), Kind: clusteragent.KindWorkload, Namespace: v.namespace, Name: v.name, ObjectUID: v.uid,
		Type: typ, From: from, To: to, OccurredAt: at, ObservedAt: d.at(), Workload: v.name, Reasons: reasons,
		Snapshot: &clusteragent.Snapshot{Owner: v.kind, Desired: i32(v.desired), Ready: i32(v.ready), Available: i32(v.available), Requests: &req, Limits: &lim}}
}

// workloadChanges are the changes of the replicas a workload asks for.
func (d *detector) workloadChanges(old, n workloadView) []clusteragent.Change {
	if old.desired == n.desired {
		return nil
	}
	return []clusteragent.Change{d.workloadChange(n, "replicas", strconv.Itoa(int(old.desired)), strconv.Itoa(int(n.desired)), d.at(), replicaWriter(n.fields))}
}

func (d *detector) workloadCreated(v workloadView) clusteragent.Change {
	return d.workloadChange(v, "created", "", strconv.Itoa(int(v.desired)), timeOr(v.created, d.at()), nil)
}

func (d *detector) workloadDeleted(v workloadView) clusteragent.Change {
	return d.workloadChange(v, "deleted", strconv.Itoa(int(v.desired)), "", d.at(), nil)
}

// ---- HPAs (the HorizontalPodAutoscaler of the Kubernetes API)

func hpaSnapshot(h *autoscalingv2.HorizontalPodAutoscaler) *clusteragent.Snapshot {
	s := &clusteragent.Snapshot{Max: i32(h.Spec.MaxReplicas), Current: i32(h.Status.CurrentReplicas), Desired: i32(h.Status.DesiredReplicas),
		Target: h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name}
	min := int32(1)
	if h.Spec.MinReplicas != nil {
		min = *h.Spec.MinReplicas
	}
	s.Min = i32(min)
	return s
}

func (d *detector) hpaChange(h *autoscalingv2.HorizontalPodAutoscaler, typ, from, to string, at time.Time, reasons []clusteragent.Reason) clusteragent.Change {
	return clusteragent.Change{RecordID: idOf(clusteragent.KindHPA, string(h.UID), typ, to+"|"+from, at), Kind: clusteragent.KindHPA, Namespace: h.Namespace, Name: h.Name, ObjectUID: string(h.UID),
		Type: typ, From: from, To: to, OccurredAt: at, ObservedAt: d.at(), Workload: h.Spec.ScaleTargetRef.Name, Reasons: reasons, Snapshot: hpaSnapshot(h)}
}

func hpaCond(h *autoscalingv2.HorizontalPodAutoscaler, t autoscalingv2.HorizontalPodAutoscalerConditionType) (autoscalingv2.HorizontalPodAutoscalerCondition, bool) {
	for _, c := range h.Status.Conditions {
		if c.Type == t {
			return c, true
		}
	}
	return autoscalingv2.HorizontalPodAutoscalerCondition{}, false
}

func hpaMin(h *autoscalingv2.HorizontalPodAutoscaler) int32 {
	if h.Spec.MinReplicas != nil {
		return *h.Spec.MinReplicas
	}
	return 1
}

// hpaChanges are the changes of an HPA: its bounds, the replicas it decided on, and its conditions (can it scale, is it working, is it held at a limit).
func (d *detector) hpaChanges(old, h *autoscalingv2.HorizontalPodAutoscaler) []clusteragent.Change {
	var out []clusteragent.Change
	now := d.at()
	if hpaMin(old) != hpaMin(h) || old.Spec.MaxReplicas != h.Spec.MaxReplicas {
		out = append(out, d.hpaChange(h, "bounds", fmt.Sprintf("%d-%d", hpaMin(old), old.Spec.MaxReplicas), fmt.Sprintf("%d-%d", hpaMin(h), h.Spec.MaxReplicas), now, nil))
	}
	if old.Status.DesiredReplicas != h.Status.DesiredReplicas {
		var reasons []clusteragent.Reason
		if c, ok := hpaCond(h, autoscalingv2.ScalingActive); ok && (c.Reason != "" || c.Message != "") {
			reasons = append(reasons, clusteragent.Reason{Source: clusteragent.SourceHPACondition, Reason: c.Reason, Message: clusteragent.Clip(c.Message, 512), Component: string(c.Type)})
		}
		at := now
		if h.Status.LastScaleTime != nil {
			at = h.Status.LastScaleTime.UTC()
		}
		out = append(out, d.hpaChange(h, "desired", strconv.Itoa(int(old.Status.DesiredReplicas)), strconv.Itoa(int(h.Status.DesiredReplicas)), at, reasons))
	}
	for _, t := range []autoscalingv2.HorizontalPodAutoscalerConditionType{autoscalingv2.AbleToScale, autoscalingv2.ScalingActive, autoscalingv2.ScalingLimited} {
		oc, ok1 := hpaCond(old, t)
		nc, ok2 := hpaCond(h, t)
		if ok1 && ok2 && nc.Status != oc.Status {
			out = append(out, d.hpaChange(h, "condition", string(t)+":"+string(oc.Status), string(t)+":"+string(nc.Status), timeOr(nc.LastTransitionTime, now),
				[]clusteragent.Reason{{Source: clusteragent.SourceHPACondition, Reason: nc.Reason, Message: clusteragent.Clip(nc.Message, 512), Component: string(t)}}))
		}
	}
	return out
}

func (d *detector) hpaCreated(h *autoscalingv2.HorizontalPodAutoscaler) clusteragent.Change {
	return d.hpaChange(h, "created", "", "hpa", timeOr(h.CreationTimestamp, d.at()), nil)
}

func (d *detector) hpaDeleted(h *autoscalingv2.HorizontalPodAutoscaler) clusteragent.Change {
	return d.hpaChange(h, "deleted", "hpa", "", d.at(), nil)
}
