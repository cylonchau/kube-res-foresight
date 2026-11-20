package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func invScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	return s
}

func qty(s string) resource.Quantity { return resource.MustParse(s) }

func rl(cpu, mem string) corev1.ResourceList {
	out := corev1.ResourceList{}
	if cpu != "" {
		out[corev1.ResourceCPU] = qty(cpu)
	}
	if mem != "" {
		out[corev1.ResourceMemory] = qty(mem)
	}
	return out
}

func node(name string, ready bool, labels map[string]string, providerID string, conds ...corev1.NodeCondition) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{corev1.ResourceCPU: qty("4"), corev1.ResourceMemory: qty("16Gi"), corev1.ResourcePods: qty("110")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: qty("3900m"), corev1.ResourceMemory: qty("15Gi"), corev1.ResourcePods: qty("110")},
			NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.30.2"}, Conditions: append([]corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}, conds...)}}
}

func invDeployment(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", Generation: 7},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "app", Resources: corev1.ResourceRequirements{Requests: rl("500m", "512Mi"), Limits: rl("1", "1Gi")}},
				{Name: "sidecar", Resources: corev1.ResourceRequirements{Requests: rl("50m", "64Mi")}}}}}},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 3}}
}

func webPod(name, nodeName string, phase corev1.PodPhase, ready bool, restarts int32) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	started := metav1.NewTime(time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC))
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{
			{Name: "app", Resources: corev1.ResourceRequirements{Requests: rl("500m", "512Mi"), Limits: rl("1", "1Gi")}},
			{Name: "sidecar", Resources: corev1.ResourceRequirements{Requests: rl("50m", "64Mi")}}},
			InitContainers: []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{Requests: rl("2", "100Mi")}}}},
		Status: corev1.PodStatus{Phase: phase, StartTime: &started, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: restarts}, {Name: "sidecar", RestartCount: 1}}}}
}

func reader(t *testing.T, objs ...client.Object) *InventoryReader {
	return &InventoryReader{Reader: fake.NewClientBuilder().WithScheme(invScheme(t)).WithObjects(objs...).Build()}
}

func TestTheNodesAreReadWithWhereTheyAreWhatTheyAreAndHowTheyAre(t *testing.T) {
	r := reader(t,
		node("n2", false, map[string]string{"topology.kubernetes.io/region": "us-east-1", "topology.kubernetes.io/zone": "us-east-1b", "node.kubernetes.io/instance-type": "m5.xlarge"},
			"aws:///us-east-1b/i-2", corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue}, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}),
		node("n1", true, map[string]string{"failure-domain.beta.kubernetes.io/zone": "zone-a", "beta.kubernetes.io/instance-type": "ecs.g6"}, "aliyun://cn-hangzhou.i-1"))
	nodes, err := r.Nodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, "n1", nodes[0].Name, "by name")
	assert.True(t, nodes[0].Ready)
	assert.Equal(t, "alibaba", nodes[0].Provider)
	assert.Equal(t, "zone-a", nodes[0].Zone, "the older label is read when the newer one is not there")
	assert.Equal(t, "ecs.g6", nodes[0].InstanceType)
	assert.Equal(t, inventory.Capacity{CPUMilli: 4000, MemoryBytes: 16 << 30, Pods: 110}, nodes[0].Capacity)
	assert.Equal(t, inventory.Capacity{CPUMilli: 3900, MemoryBytes: 15 << 30, Pods: 110}, nodes[0].Allocatable)
	assert.Equal(t, "v1.30.2", nodes[0].KubeletVersion)
	assert.False(t, nodes[1].Ready)
	assert.Equal(t, "aws", nodes[1].Provider)
	assert.Equal(t, "us-east-1", nodes[1].Region)
	assert.Equal(t, []string{"MemoryPressure"}, nodes[1].Pressure, "only a condition that is true")
}

func TestTheWorkloadOfAModuleIsReadWithItsContainersItsPodsAndItsReplicas(t *testing.T) {
	hpaMin := int32(2)
	stab := int32(300)
	r := reader(t, invDeployment(3),
		webPod("web-b", "n2", corev1.PodRunning, false, 3), webPod("web-a", "n1", corev1.PodRunning, true, 0),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "prod", Labels: map[string]string{"app": "other"}}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "web-hpa", Namespace: "prod"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"}, MinReplicas: &hpaMin, MaxReplicas: 10,
			Behavior: &autoscalingv2.HorizontalPodAutoscalerBehavior{ScaleDown: &autoscalingv2.HPAScalingRules{StabilizationWindowSeconds: &stab}}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3, DesiredReplicas: 5}})
	w, pods := r.Workload(context.Background(), inventory.Target{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"})
	assert.Equal(t, "shop", w.Module, "the module that named it")
	assert.Equal(t, "web", w.Name)
	assert.EqualValues(t, 3, w.Desired)
	assert.EqualValues(t, 2, w.Ready)
	assert.EqualValues(t, 3, w.Updated)
	assert.EqualValues(t, 7, w.Generation)
	require.Len(t, w.Containers, 2)
	assert.Equal(t, inventory.Container{Name: "app", Requests: inventory.Resources{CPUMilli: 500, MemoryBytes: 512 << 20}, Limits: inventory.Resources{CPUMilli: 1000, MemoryBytes: 1 << 30}}, w.Containers[0])
	assert.Equal(t, inventory.Container{Name: "sidecar", Requests: inventory.Resources{CPUMilli: 50, MemoryBytes: 64 << 20}}, w.Containers[1])
	require.Len(t, w.Pods, 2, "the Pods the selector names, not the Pod of another app")
	assert.Len(t, pods, 2)
	assert.Equal(t, "web-a", w.Pods[0].Name)
	assert.True(t, w.Pods[0].Ready)
	assert.Equal(t, "n1", w.Pods[0].Node)
	assert.Equal(t, "Running", w.Pods[0].Phase)
	assert.Equal(t, 1, w.Pods[0].Restarts)
	assert.Equal(t, 4, w.Pods[1].Restarts, "the restarts of every container")
	require.NotNil(t, w.Pods[0].StartedAt)
	require.NotNil(t, w.HPA)
	assert.Equal(t, inventory.HPA{Kind: "hpa", Name: "web-hpa", Min: 2, Max: 10, Current: 3, Desired: 5, Behavior: "scaleDown stabilization 300s"}, *w.HPA)
	assert.False(t, w.Missing)
	assert.Empty(t, w.Error)
}

func TestAStatefulSetAndADaemonSetAreReadLikeADeployment(t *testing.T) {
	two := int32(2)
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}
	tpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Resources: corev1.ResourceRequirements{Requests: rl("1", "2Gi")}}}}}
	r := reader(t,
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: appsv1.StatefulSetSpec{Replicas: &two, Selector: sel, Template: tpl},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 2}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "prod"}, Spec: appsv1.DaemonSetSpec{Selector: sel, Template: tpl},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 5, NumberReady: 4, NumberAvailable: 4, UpdatedNumberScheduled: 5}})
	sts, _ := r.Workload(context.Background(), inventory.Target{Name: "db", Namespace: "prod", Kind: "StatefulSet", Workload: "db"})
	assert.Equal(t, "StatefulSet", sts.Kind)
	assert.EqualValues(t, 2, sts.Desired)
	assert.EqualValues(t, 2, sts.Ready)
	require.Len(t, sts.Containers, 1)
	ds, _ := r.Workload(context.Background(), inventory.Target{Name: "agent", Namespace: "prod", Kind: "DaemonSet", Workload: "agent"})
	assert.EqualValues(t, 5, ds.Desired, "the nodes it should run on")
	assert.EqualValues(t, 4, ds.Ready)
}

func TestAWorkloadThatIsNotThereIsMissingAndOneThatCannotBeReadSaysWhyAndNeitherStopsTheOthers(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "statefulsets"}, "db", errors.New("no permission"))
	c := fake.NewClientBuilder().WithScheme(invScheme(t)).WithObjects(invDeployment(3)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return forbidden
			}
			return cl.Get(ctx, key, obj, opts...)
		}}).Build()
	r := &InventoryReader{Reader: c}
	snap := r.Read(context.Background(), []inventory.Target{
		{Name: "gone", Namespace: "prod", Kind: "Deployment", Workload: "nothing"},
		{Name: "locked", Namespace: "prod", Kind: "StatefulSet", Workload: "db"},
		{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}, time.Now())
	require.Len(t, snap.Workloads, 3)
	assert.True(t, snap.Workloads[0].Missing)
	assert.Contains(t, snap.Workloads[1].Error, "forbidden")
	assert.False(t, snap.Workloads[1].Missing)
	assert.EqualValues(t, 3, snap.Workloads[2].Desired, "the third is read")
	assert.NotNil(t, snap.Workloads[0].Containers, "a list, never null")
	assert.NotNil(t, snap.Workloads[0].Pods)
}

func TestTheNodesThatCannotBeReadAreToldAndTheWorkloadsAreStillReported(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(invScheme(t)).WithObjects(invDeployment(3)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.NodeList); ok {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("nodes are not for this account"))
			}
			return cl.List(ctx, list, opts...)
		}}).Build()
	snap := (&InventoryReader{Reader: c}).Read(context.Background(), []inventory.Target{{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}, time.Now())
	assert.Contains(t, snap.NodesError, "forbidden")
	assert.Empty(t, snap.Nodes)
	assert.NotNil(t, snap.Nodes, "a list, never null")
	require.Len(t, snap.Workloads, 1)
}

func TestTheRequestsOfTheModulePodsAreSummedOnTheNodeTheyRunOnAsTheSchedulerReadsThem(t *testing.T) {
	done := webPod("web-done", "n1", corev1.PodSucceeded, false, 0)
	r := reader(t, node("n1", true, nil, ""), node("n2", true, nil, ""), invDeployment(3),
		webPod("web-a", "n1", corev1.PodRunning, true, 0), webPod("web-b", "n1", corev1.PodRunning, true, 0), webPod("web-c", "n2", corev1.PodRunning, true, 0),
		webPod("web-pending", "", corev1.PodPending, false, 0), done)
	snap := r.Read(context.Background(), []inventory.Target{{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}, time.Now())
	require.Len(t, snap.Nodes, 2)
	n1, n2 := snap.Nodes[0], snap.Nodes[1]
	assert.Equal(t, 2, n1.Pods, "a Pod that finished does not count, and one that is not placed has no node")
	// each Pod asks for the sum of its containers, 550m, but its init container asks for 2 cores and the scheduler places it by the larger
	assert.EqualValues(t, 2*2000, n1.Requests.CPUMilli)
	assert.EqualValues(t, 2*((512+64)<<20), n1.Requests.MemoryBytes)
	assert.EqualValues(t, 2*1000, n1.Limits.CPUMilli)
	assert.Equal(t, 1, n2.Pods)
	assert.EqualValues(t, 2000, n2.Requests.CPUMilli)
}

type fakeTargets struct {
	mu      sync.Mutex
	targets []inventory.Target
	err     error
}

func (f *fakeTargets) InventoryTargets(context.Context) ([]inventory.Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets, f.err
}

type queue struct{ got []inventory.Snapshot }

func (q *queue) ReportInventory(_ context.Context, s inventory.Snapshot) error {
	q.got = append(q.got, s)
	return nil
}

func TestTheRunnerReportsWhenSomethingThatIsConfigurationChangedAndOnceAnHourWhenNothingDid(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(invScheme(t)).WithObjects(node("n1", true, nil, ""), invDeployment(3)).Build()
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	targets := &fakeTargets{targets: []inventory.Target{{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}}
	q := &queue{}
	run := &InventoryRunner{Reader: &InventoryReader{Reader: c}, Targets: targets, Queue: q, Now: func() time.Time { return clock }}
	ctx := context.Background()

	assert.True(t, run.Once(ctx), "the first reading is reported")
	clock = clock.Add(5 * time.Minute)
	assert.False(t, run.Once(ctx), "nothing changed")
	// a Pod restarts and the ready count moves: not configuration
	require.NoError(t, c.Create(ctx, webPod("web-a", "n1", corev1.PodRunning, true, 5)))
	assert.False(t, run.Once(ctx), "Pods are not configuration")

	var dep appsv1.Deployment
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "prod", Name: "web"}, &dep))
	dep.Spec.Template.Spec.Containers[0].Resources.Requests = rl("750m", "512Mi")
	require.NoError(t, c.Update(ctx, &dep))
	clock = clock.Add(5 * time.Minute)
	assert.True(t, run.Once(ctx), "a request was raised")
	assert.False(t, run.Once(ctx))

	clock = clock.Add(61 * time.Minute)
	assert.True(t, run.Once(ctx), "an hour without a report: the history of the replicas needs one")
	assert.Len(t, q.got, 3)
	assert.EqualValues(t, 750, q.got[1].Workloads[0].Containers[0].Requests.CPUMilli)
	assert.False(t, q.got[0].ObservedAt.IsZero())
}

func TestWhenThePlatformCannotBeReachedTheModulesItLastNamedAreStillRead(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(invScheme(t)).WithObjects(node("n1", true, nil, ""), invDeployment(3)).Build()
	targets := &fakeTargets{err: errors.New("platform unreachable")}
	q := &queue{}
	run := &InventoryRunner{Reader: &InventoryReader{Reader: c}, Targets: targets, Queue: q, Force: time.Nanosecond}
	ctx := context.Background()
	assert.False(t, run.Once(ctx), "nothing is known about the modules yet: nothing is read")
	targets.mu.Lock()
	targets.err, targets.targets = nil, []inventory.Target{{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}
	targets.mu.Unlock()
	assert.True(t, run.Once(ctx))
	targets.mu.Lock()
	targets.err = errors.New("platform unreachable")
	targets.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	assert.True(t, run.Once(ctx), "the outage goes on, and the reading with it: the outbox keeps the report")
	require.Len(t, q.got, 2)
	assert.Len(t, q.got[1].Workloads, 1)
}

func TestInventoryReportsGoThroughTheOutboxLikeTheRecordOfAScalingAction(t *testing.T) {
	var mu sync.Mutex
	var got []inventory.Snapshot
	up := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var s inventory.Snapshot
		_ = json.NewDecoder(r.Body).Decode(&s)
		got = append(got, s)
		assert.Equal(t, "/api/v1/operator/inventory", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "msg": "success", "data": map[string]any{}})
	}))
	defer server.Close()
	platform := platformDecider(server.URL, "AK:SK")
	o := &Outbox{Sink: platform, InventorySink: platform, Operator: "op-1"}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, o.ReportInventory(ctx, inventory.Snapshot{ObservedAt: time.Date(2026, 11, 20, 12+i, 0, 0, 0, time.UTC), Nodes: []inventory.Node{{Name: "n1"}}}))
	}
	o.Flush(ctx)
	assert.Equal(t, 3, o.Pending(), "the platform is away: the reports wait")

	mu.Lock()
	up = true
	mu.Unlock()
	o.Flush(ctx)
	assert.Zero(t, o.Pending())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 3)
	assert.True(t, got[0].ObservedAt.Before(got[2].ObservedAt), "oldest first, so the platform sees the cluster change in order")
	ids := map[string]bool{}
	for _, s := range got {
		assert.NotEmpty(t, s.RecordID)
		assert.Equal(t, "op-1", s.Operator)
		ids[s.RecordID] = true
	}
	assert.Len(t, ids, 3, "each report has an id of its own")
}

func TestTheOutboxHoldsAFewInventoryReportsAndNeverDropsARecordOfAScalingActionForThem(t *testing.T) {
	platform := platformDecider("http://127.0.0.1:1", "AK:SK") // never answers: everything stays queued
	o := &Outbox{Sink: platform, InventorySink: platform, Max: 1000, Metrics: NewMetrics(nil)}
	ctx := context.Background()
	require.NoError(t, o.Report(ctx, &Scaler{Module: "web"}, Report{Result: "applied"}))
	for i := 0; i < MaxInventoryReports+10; i++ {
		require.NoError(t, o.ReportInventory(ctx, inventory.Snapshot{RecordID: "inv-" + string(rune('A'+i%26)) + time.Duration(i).String()}))
	}
	require.NoError(t, o.Report(ctx, &Scaler{Module: "web"}, Report{Result: "held"}))
	assert.Equal(t, 2+MaxInventoryReports, o.Pending(), "the two records of actions and the newest inventory reports")
	o.mu.Lock()
	defer o.mu.Unlock()
	assert.Nil(t, o.queue[0].Inventory, "the record of the action that came first is still the first")
	assert.Equal(t, "applied", o.queue[0].Report.Result)
	assert.Equal(t, "held", o.queue[len(o.queue)-1].Report.Result)
}

func TestInventoryReportsSurviveARestartThroughTheSpoolFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "spool.jsonl")
	platform := platformDecider("http://127.0.0.1:1", "AK:SK")
	first := &Outbox{Sink: platform, InventorySink: platform, File: file}
	require.NoError(t, first.ReportInventory(context.Background(), inventory.Snapshot{RecordID: "inv-1", Nodes: []inventory.Node{{Name: "n1", Ready: true}}}))
	require.NoError(t, first.Report(context.Background(), &Scaler{Module: "web"}, Report{Result: "applied", RecordID: "rec-1"}))

	second := &Outbox{Sink: platform, InventorySink: platform, File: file}
	require.NoError(t, second.Load())
	assert.Equal(t, 2, second.Pending())
	second.mu.Lock()
	defer second.mu.Unlock()
	require.NotNil(t, second.queue[0].Inventory)
	assert.Equal(t, "inv-1", second.queue[0].Inventory.RecordID)
	assert.True(t, second.queue[0].Inventory.Nodes[0].Ready)
	assert.Equal(t, "rec-1", second.queue[1].Report.RecordID)
}

func TestARefusalOfAReportOfTheInventoryDropsItInsteadOfBlockingTheQueueAndNoSinkDropsItToo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()
	platform := platformDecider(server.URL, "AK:SK")
	o := &Outbox{Sink: platform, InventorySink: platform, Metrics: NewMetrics(nil)}
	require.NoError(t, o.ReportInventory(context.Background(), inventory.Snapshot{RecordID: "bad"}))
	o.Flush(context.Background())
	assert.Zero(t, o.Pending(), "a report the platform refuses for good is not sent again")

	none := &Outbox{Sink: platform, Metrics: NewMetrics(nil)}
	require.NoError(t, none.ReportInventory(context.Background(), inventory.Snapshot{RecordID: "x"}))
	none.Flush(context.Background())
	assert.Zero(t, none.Pending())
}

func TestThePlatformDeciderReadsTheTargetsAndSendsTheReport(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/v1/operator/inventory-targets":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "data": map[string]any{"targets": []map[string]any{{"name": "shop", "namespace": "prod", "workload_kind": "Deployment", "workload": "web"}}}})
		case "/api/v1/operator/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 10004, "msg": "refused"})
		}
	}))
	defer server.Close()
	d := platformDecider(server.URL, "AK:SK")
	targets, err := d.InventoryTargets(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "kube-res-foresight-AK AK:SK", gotAuth)
	assert.Equal(t, []inventory.Target{{Name: "shop", Namespace: "prod", Kind: "Deployment", Workload: "web"}}, targets)
	err = d.ReportInventory(context.Background(), inventory.Snapshot{RecordID: "r"})
	require.Error(t, err, "an answer that says no is an error")
	assert.Contains(t, err.Error(), "refused")
}
