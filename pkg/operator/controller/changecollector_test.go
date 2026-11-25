package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
)

type fakeChangePlatform struct {
	mu       sync.Mutex
	settings clusteragent.Settings
	sendErr  error
	batches  []clusteragent.ChangeBatch
}

func (f *fakeChangePlatform) ClusterSettings(context.Context) (clusteragent.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeChangePlatform) ReportChanges(_ context.Context, b clusteragent.ChangeBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.batches = append(f.batches, b)
	return nil
}

func (f *fakeChangePlatform) sent() []clusteragent.Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []clusteragent.Change
	for _, b := range f.batches {
		out = append(out, b.Changes...)
	}
	return out
}

// fakeCaches hands out a fake cache each time the collector asks for one, and counts them.
type fakeCaches struct {
	mu     sync.Mutex
	made   []*informertest.FakeInformers
	closed atomic.Int32
}

func (f *fakeCaches) New() (cache.Cache, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &informertest.FakeInformers{}
	f.made = append(f.made, c)
	return c, nil
}

func (f *fakeCaches) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

func (f *fakeCaches) last() *informertest.FakeInformers {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made[len(f.made)-1]
}

func collector(plat ChangePlatform, caches *fakeCaches) *ChangeCollector {
	return &ChangeCollector{Platform: plat, NewCache: caches.New, Operator: "op-1", Now: func() time.Time { return chAt }}
}

func podUpdate(t *testing.T, ctx context.Context, ca *informertest.FakeInformers, old, cur *corev1.Pod) {
	t.Helper()
	inf, err := ca.FakeInformerFor(ctx, &corev1.Pod{})
	require.NoError(t, err)
	inf.Update(old, cur)
}

func TestNothingIsWatchedUntilThePlatformSaysToCollectAndTheMemoryGoesWhenItStops(t *testing.T) {
	caches := &fakeCaches{}
	c := collector(&fakeChangePlatform{}, caches)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.Use(ctx, clusteragent.Settings{CollectChanges: false})
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, caches.count(), "collection is off on the platform: no cache over the cluster is made")

	c.Use(ctx, clusteragent.Settings{CollectChanges: true})
	require.Eventually(t, func() bool { return caches.count() == 1 && c.ready() }, 3*time.Second, 10*time.Millisecond)
	c.Use(ctx, clusteragent.Settings{CollectChanges: true})
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, caches.count(), "one cache, however often the settings are read")

	c.Use(ctx, clusteragent.Settings{CollectChanges: false})
	assert.False(t, c.ready(), "switched off: what it held is let go")
	c.Use(ctx, clusteragent.Settings{CollectChanges: true})
	require.Eventually(t, func() bool { return caches.count() == 2 && c.ready() }, 3*time.Second, 10*time.Millisecond, "switched on again: a new cache")
}

func TestWhatChangesAfterTheFirstListIsReportedAndWhatWasThereIsNot(t *testing.T) {
	caches := &fakeCaches{}
	plat := &fakeChangePlatform{}
	c := collector(plat, caches)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Use(ctx, clusteragent.Settings{CollectChanges: true, Namespaces: clusteragent.Scope{Exclude: []string{"kube-system"}}})
	require.Eventually(t, func() bool { return c.ready() }, 3*time.Second, 10*time.Millisecond)
	ca := caches.last()

	running := podOn("web-6f7d-abcde", "n1", "web-6f7d", "ReplicaSet")
	running.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app"}}
	crashed := running.DeepCopy()
	crashed.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 1,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}}
	podUpdate(t, ctx, ca, running, crashed)

	// a Pod of a namespace the user left out
	sys := podOn("coredns-1", "n1", "coredns", "ReplicaSet")
	sys.Namespace = "kube-system"
	sys.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app"}}
	sysCrashed := sys.DeepCopy()
	sysCrashed.Status.ContainerStatuses = crashed.Status.ContainerStatuses
	podUpdate(t, ctx, ca, sys, sysCrashed)

	// a node is in no namespace: never left out
	nodeInf, err := ca.FakeInformerFor(ctx, &corev1.Node{})
	require.NoError(t, err)
	nodeInf.Update(readyNode("n1", corev1.ConditionTrue, "", "", chAt.Add(-time.Hour)), readyNode("n1", corev1.ConditionFalse, "KubeletNotReady", "runtime down", chAt))
	nodeInf.Delete(readyNode("n9", corev1.ConditionFalse, "KubeletNotReady", "", chAt))

	// a workload and an HPA
	depInf, err := ca.FakeInformerFor(ctx, &appsv1.Deployment{})
	require.NoError(t, err)
	depInf.Update(chDeployment(3, nil), chDeployment(5, nil))
	hpaInf, err := ca.FakeInformerFor(ctx, &autoscalingv2.HorizontalPodAutoscaler{})
	require.NoError(t, err)
	hpaInf.Update(hpaOf(2, 10, 3), hpaOf(2, 10, 5))

	// a node that is added after the first list is new
	nodeInf.Add(readyNode("n2", corev1.ConditionTrue, "", "", chAt))

	got := map[string]bool{}
	for _, p := range c.pending().take(100) {
		got[p.Kind+"/"+p.Name+":"+p.Type] = true
	}
	assert.True(t, got["Pod/web-6f7d-abcde:restart"])
	assert.False(t, got["Pod/coredns-1:restart"], "kube-system is left out")
	assert.True(t, got["Node/n1:ready"])
	assert.True(t, got["Node/n9:deleted"])
	assert.True(t, got["Workload/web:replicas"])
	assert.True(t, got["HPA/web:desired"])
	assert.True(t, got["Node/n2:created"])
	assert.Len(t, got, 6)
}

func TestAnObjectThatIsListedBeforeTheCacheHasSyncedIsNotAChange(t *testing.T) {
	c := &ChangeCollector{Platform: &fakeChangePlatform{}, Now: func() time.Time { return chAt }}
	c.Use(context.Background(), clusteragent.Settings{CollectChanges: false}) // settings in force, nothing watched
	c.mu.Lock()
	c.settings.CollectChanges = true
	c.mu.Unlock()
	c.onAdd(podOn("p", "n1", "", ""), true)
	c.onAdd(podOn("p", "n1", "", ""), false)
	c.onUpdate(podOn("p", "n1", "", ""), podOn("p", "n2", "", ""))
	assert.Equal(t, 0, c.pending().len(), "the collector is not ready: nothing before the first list finished is a change")
}

func TestAChangeOfTheNamespacesDropsWhatWasCollectedUnderTheOldOnes(t *testing.T) {
	caches := &fakeCaches{}
	c := collector(&fakeChangePlatform{}, caches)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Use(ctx, clusteragent.Settings{CollectChanges: true})
	require.Eventually(t, func() bool { return c.ready() }, 3*time.Second, 10*time.Millisecond)
	running := podOn("p", "n1", "", "")
	moved := running.DeepCopy()
	moved.Spec.NodeName = "n2"
	podUpdate(t, ctx, caches.last(), running, moved)
	require.Equal(t, 1, c.pending().len())
	c.Use(ctx, clusteragent.Settings{CollectChanges: true, Namespaces: clusteragent.Scope{Exclude: []string{"shop"}}})
	assert.Equal(t, 0, c.pending().len())
}

func TestChangesThatCannotBeSentAreKeptAndSentWhenThePlatformIsBack(t *testing.T) {
	plat := &fakeChangePlatform{sendErr: unreachable(&statusError{Code: http.StatusBadGateway})}
	c := &ChangeCollector{Platform: plat, Operator: "op-1"}
	c.Use(context.Background(), clusteragent.Settings{})
	c.mu.Lock()
	c.settings = &clusteragent.Settings{CollectChanges: true}
	c.mu.Unlock()
	d := newDetector(nil)
	for _, ch := range d.podChanges(podOn("p", "n1", "", ""), func() *corev1.Pod { p := podOn("p", "n2", "", ""); return p }()) {
		c.pending().add(ch)
	}
	require.Equal(t, 1, c.pending().len())
	_, failed := c.flushOnce(context.Background())
	assert.True(t, failed)
	assert.Equal(t, 1, c.pending().len(), "nothing is lost")

	plat.mu.Lock()
	plat.sendErr = nil
	plat.mu.Unlock()
	more, failed := c.flushOnce(context.Background())
	assert.False(t, more)
	assert.False(t, failed)
	assert.Equal(t, 0, c.pending().len())
	require.Len(t, plat.sent(), 1)
	assert.Equal(t, "op-1", plat.batches[0].Operator)

	// refused for good: dropped, so it does not hold the ones behind it
	plat.mu.Lock()
	plat.sendErr = unreachable(&statusError{Code: http.StatusBadRequest})
	plat.mu.Unlock()
	c.pending().add(clusteragent.Change{RecordID: "x", Kind: "Pod", Name: "p", Type: "phase", OccurredAt: chAt})
	_, failed = c.flushOnce(context.Background())
	assert.False(t, failed)
	assert.Equal(t, 0, c.pending().len())
}

func TestTheChangeBufferIsBoundedAndDropsTheOldest(t *testing.T) {
	p := newPendingChanges(10)
	for i := 0; i < 30; i++ {
		p.add(clusteragent.Change{RecordID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Kind: "Pod", Name: "p", Type: "t", OccurredAt: chAt.Add(time.Duration(i) * time.Second)})
	}
	assert.LessOrEqual(t, p.len(), 10)
	assert.Greater(t, p.dropped, int64(0))
	kept := map[string]bool{}
	for _, c := range p.take(100) {
		kept[c.RecordID] = true
	}
	assert.False(t, kept["aa"], "the oldest is the first to go")
	assert.True(t, kept["db"], "the newest stays")
	p.add(clusteragent.Change{RecordID: "db"})
	assert.LessOrEqual(t, p.len(), 10, "the same id twice is one")
}

func TestWhatTheCollectorKeepsOfAPodIsWhatItReads(t *testing.T) {
	p := podOn("p", "n1", "", "")
	p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "SECRET", Value: "x"}}
	p.Spec.Containers[0].Image = "registry/app:1"
	p.Spec.Volumes = []corev1.Volume{{Name: "v"}}
	p.Annotations = map[string]string{"a": "b"}
	p.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}
	out, err := trim(p)
	require.NoError(t, err)
	got := out.(*corev1.Pod)
	assert.Empty(t, got.Spec.Containers[0].Env, "no environment is held, and none reaches the platform")
	assert.Empty(t, got.Spec.Containers[0].Image)
	assert.Empty(t, got.Spec.Volumes)
	assert.Empty(t, got.Annotations)
	assert.Empty(t, got.ManagedFields)
	assert.Equal(t, int64(500), resOf(got.Spec.Containers[0].Resources.Requests).CPUMilli, "the resources are kept")
	assert.Equal(t, "n1", got.Spec.NodeName)

	// a Deployment keeps the managed fields: they say who wrote the replicas
	d := chDeployment(3, []metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
	out, _ = trim(d)
	assert.Len(t, out.(*appsv1.Deployment).ManagedFields, 1)
}

func TestTheOperatorSendsTheChangesToThePlatformAndNamesItsCluster(t *testing.T) {
	var path, cluster string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, cluster = r.Method+" "+r.URL.Path, r.Header.Get(clusteragent.HeaderCluster)
		_, _ = w.Write([]byte(`{"code":10000,"msg":"ok","data":{"added":1}}`))
	}))
	defer srv.Close()
	d := &PlatformDecider{Cluster: "east", Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk"}}
	require.NoError(t, d.ReportChanges(context.Background(), clusteragent.ChangeBatch{Changes: []clusteragent.Change{{RecordID: "r", Kind: "Pod", Name: "p", Type: "phase", OccurredAt: chAt}}}))
	assert.Equal(t, "POST /api/v1/operator/changes", path)
	assert.Equal(t, "east", cluster)
}

func TestTheLogSaysWhyThePlatformRefusedNotOnlyThatItDid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":10003,"msg":"the credential is not bound to this cluster: bind the credential to cluster \"east\" on the clusters page"}`))
	}))
	defer srv.Close()
	d := &PlatformDecider{Cluster: "east", Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk"}}
	_, err := d.ClusterSettings(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform answered 403")
	assert.Contains(t, err.Error(), "not bound to this cluster", "the reason the platform gave is in the error")
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// an answer that says nothing readable still reports the status
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv2.Close()
	_, err = (&PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv2.URL, Credential: "ak:sk"}}).ClusterSettings(context.Background())
	require.Error(t, err)
	assert.Equal(t, "platform answered 502", err.Error())
}
