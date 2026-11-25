package controller

import (
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var chAt = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type fakePods map[string][]*corev1.Pod

func (f fakePods) PodsOnNode(node string) []*corev1.Pod { return f[node] }

func newDetector(pods PodLister) *detector {
	return &detector{now: func() time.Time { return chAt }, pods: pods}
}

func quantity(s string) resource.Quantity { return resource.MustParse(s) }

func readyNode(name string, status corev1.ConditionStatus, reason, msg string, transition time.Time) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: map[string]string{"topology.kubernetes.io/zone": "z1"}},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{corev1.ResourceCPU: quantity("4"), corev1.ResourceMemory: quantity("16Gi"), corev1.ResourcePods: quantity("110")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: quantity("3900m"), corev1.ResourceMemory: quantity("15Gi")},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status, Reason: reason, Message: msg, LastTransitionTime: metav1.NewTime(transition)},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}}}}
}

func podOn(name, node string, controller string, ownerKind string) *corev1.Pod {
	yes := true
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name)},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: quantity("500m"), corev1.ResourceMemory: quantity("512Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: quantity("1"), corev1.ResourceMemory: quantity("1Gi")}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if controller != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: controller, Controller: &yes}}
	}
	return p
}

func TestANodeThatIsNotReadyIsReportedWithWhatItSaysAndWhatWasOnIt(t *testing.T) {
	pods := fakePods{"n1": {podOn("web-6f7d-abcde", "n1", "web-6f7d", "ReplicaSet"), podOn("db-0", "n1", "db", "StatefulSet")}}
	d := newDetector(pods)
	old := readyNode("n1", corev1.ConditionTrue, "KubeletReady", "kubelet is posting ready status", chAt.Add(-time.Hour))
	cur := readyNode("n1", corev1.ConditionUnknown, "NodeStatusUnknown", "Kubelet stopped posting node status.", chAt.Add(-30*time.Second))

	got := d.nodeChanges(old, cur)
	require.Len(t, got, 1)
	c := got[0]
	assert.Equal(t, clusteragent.KindNode, c.Kind)
	assert.Equal(t, "ready", c.Type)
	assert.Equal(t, "True", c.From)
	assert.Equal(t, "Unknown", c.To)
	assert.Equal(t, chAt.Add(-30*time.Second), c.OccurredAt, "the time the node gives for it")
	assert.Equal(t, chAt, c.ObservedAt)
	assert.Equal(t, []clusteragent.Reason{{Source: clusteragent.SourceNodeCondition, Reason: "NodeStatusUnknown", Message: "Kubelet stopped posting node status.", Component: "Ready"}}, c.Reasons)
	require.NotNil(t, c.Snapshot)
	assert.Equal(t, 2, c.Snapshot.PodCount)
	require.Len(t, c.Snapshot.Pods, 2)
	assert.Equal(t, "db-0", c.Snapshot.Pods[0].Name)
	assert.Equal(t, int64(500), c.Snapshot.Pods[0].Requests.CPUMilli)
	assert.Equal(t, "web", c.Snapshot.Pods[1].Workload, "the hash of the ReplicaSet is not part of the workload")
	assert.Equal(t, int64(3900), c.Snapshot.Allocatable.CPUMilli)
	assert.Equal(t, int64(110), c.Snapshot.CapacityPods)
	assert.Equal(t, "z1", c.Snapshot.Labels["topology.kubernetes.io/zone"])

	assert.Empty(t, d.nodeChanges(cur, cur), "an object that did not change reports nothing")
}

func TestPressureUnschedulableAndTaintsOfANodeAreChangesToo(t *testing.T) {
	d := newDetector(nil)
	old := readyNode("n1", corev1.ConditionTrue, "", "", chAt.Add(-time.Hour))
	cur := old.DeepCopy()
	cur.Status.Conditions[1] = corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasInsufficientMemory", Message: "kubelet has insufficient memory available", LastTransitionTime: metav1.NewTime(chAt.Add(-time.Minute))}
	cur.Spec.Unschedulable = true
	added := metav1.NewTime(chAt.Add(-10 * time.Second))
	cur.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/memory-pressure", Effect: corev1.TaintEffectNoSchedule, TimeAdded: &added}}

	by := map[string]clusteragent.Change{}
	for _, c := range d.nodeChanges(old, cur) {
		by[c.Type] = c
	}
	require.Len(t, by, 3)
	assert.Equal(t, "MemoryPressure:False", by["pressure"].From)
	assert.Equal(t, "MemoryPressure:True", by["pressure"].To)
	assert.Equal(t, "KubeletHasInsufficientMemory", by["pressure"].Reasons[0].Reason)
	assert.Equal(t, "false", by["unschedulable"].From)
	assert.Equal(t, "true", by["unschedulable"].To)
	assert.Equal(t, "+node.kubernetes.io/memory-pressure:NoSchedule", by["taint"].To)
	assert.Equal(t, clusteragent.SourceTaint, by["taint"].Reasons[0].Source)
	assert.Equal(t, added.UTC(), by["taint"].OccurredAt)

	// and it goes away again
	gone := d.nodeChanges(cur, old)
	var taint clusteragent.Change
	for _, c := range gone {
		if c.Type == "taint" {
			taint = c
		}
	}
	assert.Equal(t, "-node.kubernetes.io/memory-pressure:NoSchedule", taint.From)
	assert.Equal(t, "", taint.To)
}

func TestANodeThatIsDeletedKeepsTheReasonsItLastHad(t *testing.T) {
	d := newDetector(fakePods{"n1": {podOn("web-1", "n1", "", "")}})
	n := readyNode("n1", corev1.ConditionFalse, "KubeletNotReady", "container runtime is down", chAt.Add(-time.Minute))
	n.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoExecute}}
	c := d.nodeDeleted(n)
	assert.Equal(t, "deleted", c.Type)
	require.Len(t, c.Reasons, 2)
	assert.Equal(t, "KubeletNotReady", c.Reasons[0].Reason)
	assert.Equal(t, "node.kubernetes.io/not-ready", c.Reasons[1].Reason)
	assert.Equal(t, 1, c.Snapshot.PodCount, "the pods it had are the last cache of them")
	created := d.nodeCreated(readyNode("n2", corev1.ConditionTrue, "", "", chAt))
	assert.Equal(t, "created", created.Type)
}

func TestAPodThatRestartsSaysHowItEndedAndAWaitingPodSaysWhy(t *testing.T) {
	d := newDetector(nil)
	old := podOn("api-5c8b-xyz12", "n1", "api-5c8b", "ReplicaSet")
	old.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 2, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	cur := old.DeepCopy()
	cur.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 3,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 40s restarting failed container"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(chAt.Add(-20 * time.Second))}}}}

	by := map[string]clusteragent.Change{}
	for _, c := range d.podChanges(old, cur) {
		by[c.Type] = c
	}
	require.Len(t, by, 2)
	restart := by["restart"]
	assert.Equal(t, "2", restart.From)
	assert.Equal(t, "3", restart.To)
	assert.Equal(t, chAt.Add(-20*time.Second), restart.OccurredAt, "the time the container ended")
	assert.Equal(t, "OOMKilled", restart.Reasons[0].Reason)
	assert.Contains(t, restart.Reasons[0].Message, "exit code 137")
	assert.Equal(t, "app", restart.Reasons[0].Component)
	assert.Equal(t, "api", restart.Workload)
	assert.Equal(t, "n1", restart.Node)
	require.NotNil(t, restart.Snapshot)
	assert.Equal(t, int64(1000), restart.Snapshot.Limits.CPUMilli)
	assert.Equal(t, int64(1<<30), restart.Snapshot.Limits.MemoryBytes, "the memory limit the container was killed at")
	assert.Equal(t, "OOMKilled", restart.Snapshot.Containers[0].LastReason)
	assert.Equal(t, "ReplicaSet/api-5c8b", restart.Snapshot.Owner)
	waiting := by["waiting"]
	assert.Equal(t, "CrashLoopBackOff", waiting.To)
	assert.Equal(t, "back-off 40s restarting failed container", waiting.Reasons[0].Message)

	assert.Empty(t, d.podChanges(cur, cur))
}

func TestAPodThatCannotBeScheduledIsEvictedOrDeletedIsReportedAsThat(t *testing.T) {
	d := newDetector(nil)
	pending := podOn("ledger-0", "", "ledger", "StatefulSet")
	pending.Status.Phase = corev1.PodPending
	unsched := pending.DeepCopy()
	unsched.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/12 nodes are available: 12 Insufficient memory.", LastTransitionTime: metav1.NewTime(chAt.Add(-5 * time.Second))}}
	got := d.podChanges(pending, unsched)
	require.Len(t, got, 1)
	assert.Equal(t, "scheduled", got[0].Type)
	assert.Equal(t, "False", got[0].To)
	assert.Equal(t, "Unschedulable", got[0].Reasons[0].Reason)
	assert.Equal(t, "StatefulSet/ledger", got[0].Snapshot.Owner)
	assert.Equal(t, "ledger", got[0].Workload, "a StatefulSet Pod is the workload it belongs to")

	// scheduled to a node
	placed := unsched.DeepCopy()
	placed.Spec.NodeName = "n7"
	placed.Status.Conditions[0] = corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}
	types := map[string]bool{}
	for _, c := range d.podChanges(unsched, placed) {
		types[c.Type+":"+c.From+">"+c.To] = true
	}
	assert.True(t, types["node:>n7"])
	assert.True(t, types["scheduled:False>True"])

	// evicted: the status says why
	running := podOn("web-1", "n1", "web-6f7d", "ReplicaSet")
	evicted := running.DeepCopy()
	evicted.Status.Phase = corev1.PodFailed
	evicted.Status.Reason = "Evicted"
	evicted.Status.Message = "The node was low on resource: memory."
	by := map[string]clusteragent.Change{}
	for _, c := range d.podChanges(running, evicted) {
		by[c.Type] = c
	}
	assert.Equal(t, "Evicted", by["status"].To)
	assert.Equal(t, "The node was low on resource: memory.", by["status"].Reasons[0].Message)
	assert.Equal(t, "Failed", by["phase"].To)

	// being deleted, then gone
	now := metav1.NewTime(chAt)
	grace := int64(30)
	dying := running.DeepCopy()
	dying.DeletionTimestamp = &now
	dying.DeletionGracePeriodSeconds = &grace
	term := d.podChanges(running, dying)
	require.Len(t, term, 1)
	assert.Equal(t, "terminating", term[0].Type)
	assert.Contains(t, term[0].Reasons[0].Message, "30s")
	assert.Equal(t, "deleted", d.podDeleted(dying).Type)
	assert.Equal(t, "created", d.podCreated(running).Type)
}

func TestThePodsOfAnOwnerBelongToTheWorkloadTheOwnerIsNamedAfter(t *testing.T) {
	owner, wl := workloadOf(podOn("web-6f7d-abcde", "n", "web-6f7d", "ReplicaSet"))
	assert.Equal(t, "ReplicaSet/web-6f7d", owner)
	assert.Equal(t, "web", wl)
	owner, wl = workloadOf(podOn("db-0", "n", "db", "StatefulSet"))
	assert.Equal(t, "StatefulSet/db", owner)
	assert.Equal(t, "db", wl)
	owner, wl = workloadOf(podOn("lonely", "n", "", ""))
	assert.Equal(t, "", owner)
	assert.Equal(t, "lonely", wl)
	owner, wl = workloadOf(podOn("job-x", "n", "nightly", "Job"))
	assert.Equal(t, "Job/nightly", owner)
	assert.Equal(t, "nightly", wl)
}

func chDeployment(replicas int32, fields []metav1.ManagedFieldsEntry) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-web", ManagedFields: fields},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: podOn("x", "", "", "").Spec}},
		Status: appsv1.DeploymentStatus{ReadyReplicas: replicas - 1, AvailableReplicas: replicas - 1}}
}

func fieldsOf(manager string, op metav1.ManagedFieldsOperationType, at time.Time, raw, sub string) metav1.ManagedFieldsEntry {
	t := metav1.NewTime(at)
	return metav1.ManagedFieldsEntry{Manager: manager, Operation: op, Time: &t, Subresource: sub, FieldsV1: &metav1.FieldsV1{Raw: []byte(raw)}}
}

func TestAChangeOfTheReplicasNamesTheLastWriterTheObjectRecordsAndNothingElse(t *testing.T) {
	d := newDetector(nil)
	owns := `{"f:spec":{"f:replicas":{}}}`
	other := `{"f:spec":{"f:template":{}}}`
	fields := []metav1.ManagedFieldsEntry{
		fieldsOf("kubectl", metav1.ManagedFieldsOperationUpdate, chAt.Add(-48*time.Hour), owns, ""),
		fieldsOf("kube-controller-manager", metav1.ManagedFieldsOperationUpdate, chAt.Add(-time.Minute), owns, "scale"),
		fieldsOf("argocd", metav1.ManagedFieldsOperationApply, chAt.Add(-time.Second), other, ""),
	}
	old, _ := viewOf(chDeployment(3, nil))
	cur, _ := viewOf(chDeployment(5, fields))
	got := d.workloadChanges(old, cur)
	require.Len(t, got, 1)
	c := got[0]
	assert.Equal(t, clusteragent.KindWorkload, c.Kind)
	assert.Equal(t, "replicas", c.Type)
	assert.Equal(t, "3", c.From)
	assert.Equal(t, "5", c.To)
	assert.Equal(t, "web", c.Workload)
	require.Len(t, c.Reasons, 1)
	assert.Equal(t, clusteragent.SourceManagedFields, c.Reasons[0].Source)
	assert.Equal(t, "kube-controller-manager", c.Reasons[0].Component, "the newest manager that owns spec.replicas, not the newest manager")
	assert.Contains(t, c.Reasons[0].Message, "scale")
	assert.EqualValues(t, 5, *c.Snapshot.Desired)
	assert.EqualValues(t, 4, *c.Snapshot.Ready)
	assert.Equal(t, int64(500), c.Snapshot.Requests.CPUMilli)

	none, _ := viewOf(chDeployment(6, []metav1.ManagedFieldsEntry{fieldsOf("argocd", metav1.ManagedFieldsOperationApply, chAt, other, "")}))
	got = d.workloadChanges(cur, none)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Reasons, "no entry owns the field: nothing is said of who wrote it")

	assert.Empty(t, d.workloadChanges(cur, cur))
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "u"}}
	v, ok := viewOf(sts)
	require.True(t, ok)
	assert.EqualValues(t, 1, v.desired, "no replicas set is one")
	_, ok = viewOf(&corev1.Pod{})
	assert.False(t, ok)
	assert.Equal(t, "created", d.workloadCreated(v).Type)
	assert.Equal(t, "deleted", d.workloadDeleted(v).Type)
}

func hpaOf(min, max, desired int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "uid-hpa"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MinReplicas: &min, MaxReplicas: max,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"}},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{DesiredReplicas: desired, CurrentReplicas: desired}}
}

func TestAnHPAReportsItsBoundsItsDecisionsAndItsConditions(t *testing.T) {
	d := newDetector(nil)
	old := hpaOf(2, 10, 3)
	cur := hpaOf(2, 20, 5)
	scaled := metav1.NewTime(chAt.Add(-15 * time.Second))
	cur.Status.LastScaleTime = &scaled
	cur.Status.Conditions = []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound",
		Message: "the HPA was able to successfully calculate a replica count from cpu resource utilization"}}

	by := map[string]clusteragent.Change{}
	for _, c := range d.hpaChanges(old, cur) {
		by[c.Type] = c
	}
	require.Len(t, by, 2)
	assert.Equal(t, "2-10", by["bounds"].From)
	assert.Equal(t, "2-20", by["bounds"].To)
	desired := by["desired"]
	assert.Equal(t, "3", desired.From)
	assert.Equal(t, "5", desired.To)
	assert.Equal(t, scaled.UTC(), desired.OccurredAt, "when it last scaled")
	assert.Equal(t, "ValidMetricFound", desired.Reasons[0].Reason)
	assert.Equal(t, "web", desired.Workload, "the workload it targets")
	assert.Equal(t, "Deployment/web", desired.Snapshot.Target)
	assert.EqualValues(t, 20, *desired.Snapshot.Max)

	limited := cur.DeepCopy()
	limited.Status.Conditions = []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric",
		Message: "failed to get cpu utilization", LastTransitionTime: metav1.NewTime(chAt)}}
	got := d.hpaChanges(cur, limited)
	require.Len(t, got, 1)
	assert.Equal(t, "condition", got[0].Type)
	assert.Equal(t, "ScalingActive:True", got[0].From)
	assert.Equal(t, "ScalingActive:False", got[0].To)
	assert.Equal(t, "FailedGetResourceMetric", got[0].Reasons[0].Reason)
	assert.Equal(t, "created", d.hpaCreated(cur).Type)
	assert.Equal(t, "deleted", d.hpaDeleted(cur).Type)
}

func TestTheIdOfAChangeIsTheSameForTheSameChangeAndDifferentForAnother(t *testing.T) {
	a := idOf("Pod", "u1", "restart", "3|2", chAt)
	assert.Equal(t, a, idOf("Pod", "u1", "restart", "3|2", chAt), "a retry is the same change")
	assert.NotEqual(t, a, idOf("Pod", "u1", "restart", "4|3", chAt), "the next restart is another")
	assert.NotEqual(t, a, idOf("Pod", "u1", "restart", "3|2", chAt.Add(time.Second)))
	assert.NotEqual(t, a, idOf("Pod", "u2", "restart", "3|2", chAt))
	assert.Len(t, a, 40)
	assert.NoError(t, clusteragent.ChangeBatch{Changes: []clusteragent.Change{{RecordID: a, Kind: "Pod", Name: "p", Type: "restart", OccurredAt: chAt}}}.Validate())
}
