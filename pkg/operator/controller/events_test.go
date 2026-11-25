package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var evAt = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func k8sEvent(uid, reason, kind, ns, name string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{UID: types.UID(uid), Namespace: "default", CreationTimestamp: metav1.NewTime(evAt)},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: name, UID: types.UID("obj-" + name)},
		Reason:         reason, Message: reason + " happened", Type: "Normal", Count: 1,
		Source:         corev1.EventSource{Component: "kubelet", Host: "node-1"},
		FirstTimestamp: metav1.NewTime(evAt.Add(-time.Minute)), LastTimestamp: metav1.NewTime(evAt),
	}
}

func TestAnEventIsReportedAsKubernetesSaidIt(t *testing.T) {
	ev := k8sEvent("u1", "BackOff", "Pod", "shop", "web-0")
	out, ns := eventOf(ev)
	assert.Equal(t, clusteragent.Event{UID: "u1", Type: "Normal", Reason: "BackOff", Message: "BackOff happened", Component: "kubelet", Host: "node-1", Count: 1,
		Kind: "Pod", Namespace: "shop", Name: "web-0", ObjectUID: "obj-web-0", FirstAt: evAt.Add(-time.Minute), LastAt: evAt}, out)
	assert.Equal(t, "shop", ns)

	// an event of the newer API: the reporting controller is the component, the time is EventTime, and a series says how often
	ev = &corev1.Event{ObjectMeta: metav1.ObjectMeta{UID: "u2", Namespace: "kube-system"}, InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "n1"},
		Reason: "ScaleDown", Type: "Normal", ReportingController: "cluster-autoscaler", ReportingInstance: "ca-1", EventTime: metav1.NewMicroTime(evAt),
		Series: &corev1.EventSeries{Count: 4, LastObservedTime: metav1.NewMicroTime(evAt.Add(time.Hour))}}
	out, ns = eventOf(ev)
	assert.Equal(t, "cluster-autoscaler", out.Component)
	assert.Equal(t, "ca-1", out.Host)
	assert.Equal(t, 4, out.Count, "the series says how many times")
	assert.Equal(t, evAt, out.FirstAt)
	assert.Equal(t, evAt.Add(time.Hour), out.LastAt, "and when it last happened")
	assert.Equal(t, "", ns, "a node is in no namespace, whatever namespace its event is stored in")

	// no time at all on the event: the time it was created
	out, _ = eventOf(&corev1.Event{ObjectMeta: metav1.ObjectMeta{UID: "u3", CreationTimestamp: metav1.NewTime(evAt)}})
	assert.Equal(t, evAt, out.LastAt)
	assert.Equal(t, evAt, out.FirstAt)
	assert.Equal(t, 1, out.Count)
}

func TestOnlyTheNamespacesAndReasonsThePlatformAsksForAreKept(t *testing.T) {
	c := &EventCollector{}
	c.observe(k8sEvent("early", "A", "Pod", "shop", "p")) // no settings yet: nothing is collected
	assert.Equal(t, 0, c.pending().len())

	c.Use(clusteragent.Settings{Cluster: "east", CollectEvents: true, SkipReasons: []string{"Pulled"},
		Namespaces: clusteragent.Scope{Include: []string{"shop*"}, Exclude: []string{"shop-test"}}})
	c.observe(k8sEvent("a", "BackOff", "Pod", "shop", "p"))
	c.observe(k8sEvent("b", "BackOff", "Pod", "shop-test", "p"))
	c.observe(k8sEvent("c", "BackOff", "Pod", "billing", "p"))
	c.observe(k8sEvent("d", "Pulled", "Pod", "shop", "p"))
	c.observe(k8sEvent("e", "NodeNotReady", "Node", "", "n1"))
	c.observe("not an event")
	c.observe(k8sEvent("", "NoUID", "Pod", "shop", "p"))

	var got []string
	for _, e := range c.pending().take(100) {
		got = append(got, e.UID)
	}
	assert.ElementsMatch(t, []string{"a", "e"}, got, "included, not excluded, not noise; and a node is never left out")

	// the platform switches the collection off: what was waiting goes, and nothing more is taken
	c.Use(clusteragent.Settings{Cluster: "east", CollectEvents: false})
	assert.Equal(t, 0, c.pending().len())
	c.observe(k8sEvent("f", "BackOff", "Pod", "shop", "p"))
	assert.Equal(t, 0, c.pending().len())
}

func TestAChangeOfNamespacesDropsWhatWasCollectedUnderTheOldOnes(t *testing.T) {
	c := &EventCollector{}
	c.Use(clusteragent.Settings{CollectEvents: true})
	c.observe(k8sEvent("a", "BackOff", "Pod", "kube-system", "p"))
	require.Equal(t, 1, c.pending().len())
	c.Use(clusteragent.Settings{CollectEvents: true, Namespaces: clusteragent.Scope{Exclude: []string{"kube-system"}}})
	assert.Equal(t, 0, c.pending().len(), "an event of a namespace the user has now left out is not sent")
	c.Use(clusteragent.Settings{CollectEvents: true, Namespaces: clusteragent.Scope{Exclude: []string{"kube-system"}}})
	c.observe(k8sEvent("b", "BackOff", "Pod", "shop", "p"))
	c.Use(clusteragent.Settings{CollectEvents: true, Namespaces: clusteragent.Scope{Exclude: []string{"kube-system"}}})
	assert.Equal(t, 1, c.pending().len(), "the same answer again changes nothing")
}

func TestTheBufferHoldsOneEventPerUidAndDropsTheOldestWhenFull(t *testing.T) {
	p := newPending(10)
	e := func(uid string, at time.Time, count int) clusteragent.Event {
		return clusteragent.Event{UID: uid, Reason: "R", LastAt: at, Count: count}
	}
	p.add(e("x", evAt, 1))
	p.add(e("x", evAt.Add(time.Minute), 5))
	p.add(e("x", evAt, 2)) // older: ignored
	got := p.take(10)
	require.Len(t, got, 1)
	assert.Equal(t, 5, got[0].Count, "the later one stands")

	for i := 0; i < 30; i++ {
		p.add(e(fmt.Sprintf("e%02d", i), evAt.Add(time.Duration(i)*time.Second), 1))
	}
	assert.LessOrEqual(t, p.len(), 10, "bounded")
	assert.Greater(t, p.dropped, int64(0), "what was dropped is counted")
	kept := map[string]bool{}
	for _, e := range p.take(100) {
		kept[e.UID] = true
	}
	assert.True(t, kept["e29"], "the newest is kept")
	assert.False(t, kept["e00"], "the oldest is the first to go")

	// an event that happens again while it is being sent stays for the next batch
	p2 := newPending(10)
	p2.add(e("y", evAt, 1))
	sent := p2.take(10)
	p2.add(e("y", evAt.Add(time.Second), 2))
	p2.done(sent)
	assert.Equal(t, 1, p2.len())
	assert.Equal(t, 2, p2.take(10)[0].Count)
}

type fakeEventPlatform struct {
	mu       sync.Mutex
	settings clusteragent.Settings
	setErr   error
	sendErr  error
	batches  []clusteragent.Batch
}

func (f *fakeEventPlatform) ClusterSettings(context.Context) (clusteragent.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, f.setErr
}

func (f *fakeEventPlatform) ReportEvents(_ context.Context, b clusteragent.Batch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.batches = append(f.batches, b)
	return nil
}

func (f *fakeEventPlatform) sentUIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.batches {
		for _, e := range b.Events {
			out = append(out, e.UID)
		}
	}
	return out
}

func TestEventsThatCannotBeSentAreKeptAndSentWhenThePlatformIsBack(t *testing.T) {
	plat := &fakeEventPlatform{sendErr: unreachable(errors.New("connection refused"))}
	c := &EventCollector{Platform: plat, Operator: "op-1"}
	c.Use(clusteragent.Settings{CollectEvents: true})
	for i := 0; i < 3; i++ {
		c.observe(k8sEvent(fmt.Sprintf("u%d", i), "BackOff", "Pod", "shop", "p"))
	}
	more, failed := c.flushOnce(context.Background())
	assert.False(t, more)
	assert.True(t, failed, "the caller waits longer")
	assert.Equal(t, 3, c.pending().len(), "nothing is lost")

	plat.mu.Lock()
	plat.sendErr = nil
	plat.mu.Unlock()
	more, failed = c.flushOnce(context.Background())
	assert.False(t, more)
	assert.False(t, failed)
	assert.Equal(t, 0, c.pending().len())
	assert.ElementsMatch(t, []string{"u0", "u1", "u2"}, plat.sentUIDs())
	assert.Equal(t, "op-1", plat.batches[0].Operator)
}

func TestABatchThePlatformRefusesForGoodIsDroppedAndOneItCannotTakeNowIsKept(t *testing.T) {
	plat := &fakeEventPlatform{sendErr: unreachable(&statusError{Code: http.StatusBadRequest})}
	c := &EventCollector{Platform: plat}
	c.Use(clusteragent.Settings{CollectEvents: true})
	c.observe(k8sEvent("u1", "BackOff", "Pod", "shop", "p"))
	_, failed := c.flushOnce(context.Background())
	assert.False(t, failed)
	assert.Equal(t, 0, c.pending().len(), "a batch that will never be accepted does not hold the ones behind it")

	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable, http.StatusBadGateway} {
		plat.sendErr = unreachable(&statusError{Code: code})
		c.observe(k8sEvent(fmt.Sprintf("k%d", code), "BackOff", "Pod", "shop", "p"))
		_, failed = c.flushOnce(context.Background())
		assert.True(t, failed, "status %d: the cluster may be registered next minute", code)
	}
	assert.Equal(t, 4, c.pending().len())
}

func TestBatchesAreAsBigAsThePlatformTakes(t *testing.T) {
	plat := &fakeEventPlatform{}
	c := &EventCollector{Platform: plat, MaxPending: 2000}
	c.Use(clusteragent.Settings{CollectEvents: true})
	for i := 0; i < clusteragent.MaxBatchEvents+10; i++ {
		c.observe(k8sEvent(fmt.Sprintf("u%04d", i), "BackOff", "Pod", "shop", "p"))
	}
	more, _ := c.flushOnce(context.Background())
	assert.True(t, more, "there is more to send")
	more, _ = c.flushOnce(context.Background())
	assert.False(t, more)
	require.Len(t, plat.batches, 2)
	assert.Len(t, plat.batches[0].Events, clusteragent.MaxBatchEvents)
	assert.Len(t, plat.batches[1].Events, 10)
	assert.NoError(t, plat.batches[0].Validate())
}

func TestNothingIsSentBeforeThePlatformHasSaidWhatToCollect(t *testing.T) {
	plat := &fakeEventPlatform{}
	c := &EventCollector{Platform: plat}
	more, failed := c.flushOnce(context.Background())
	assert.False(t, more)
	assert.False(t, failed)
	assert.Empty(t, plat.batches)
}

// countingCache is a fake cache that counts the informers asked of it.
type countingCache struct {
	*informertest.FakeInformers
	asked atomic.Int32
}

func (c *countingCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	c.asked.Add(1)
	return c.FakeInformers.GetInformer(ctx, obj, opts...)
}

func TestTheCollectorWatchesTheEventsOnlyOnceThePlatformSaysToAndSendsThem(t *testing.T) {
	fake := &countingCache{FakeInformers: &informertest.FakeInformers{}}
	plat := &fakeEventPlatform{settings: clusteragent.Settings{Cluster: "east", CollectEvents: false}}
	c := &EventCollector{Cache: fake, Platform: plat, Operator: "op-1", Flush: 20 * time.Millisecond, Refresh: func() time.Duration { return 30 * time.Millisecond }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = c.Start(ctx); close(done) }()

	time.Sleep(150 * time.Millisecond)
	assert.EqualValues(t, 0, fake.asked.Load(), "collection is off on the platform: no informer over the events is made")

	plat.mu.Lock()
	plat.settings = clusteragent.Settings{Cluster: "east", CollectEvents: true, Namespaces: clusteragent.Scope{Exclude: []string{"kube-system"}}}
	plat.mu.Unlock()
	require.Eventually(t, func() bool { return fake.asked.Load() > 0 }, 3*time.Second, 10*time.Millisecond, "the platform switched collection on")
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 1, fake.asked.Load(), "one informer, however often the settings are read")

	inf, err := fake.FakeInformers.FakeInformerFor(ctx, &corev1.Event{})
	require.NoError(t, err)
	// the handler is added right after the informer is made: the events are given until it has taken them
	require.Eventually(t, func() bool {
		inf.Add(k8sEvent("keep", "ScaleDown", "Node", "", "n1"))
		inf.Add(k8sEvent("left-out", "BackOff", "Pod", "kube-system", "p"))
		inf.Add(k8sEvent("keep-2", "BackOff", "Pod", "shop", "p"))
		return len(plat.sentUIDs()) >= 2
	}, 3*time.Second, 20*time.Millisecond)
	assert.ElementsMatch(t, []string{"keep", "keep-2"}, plat.sentUIDs())

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the collector did not stop")
	}
}

func TestTheOperatorNamesItsClusterInEveryRequestAndReadsTheSettingsAndSendsEvents(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" cluster="+r.Header.Get(clusteragent.HeaderCluster))
		switch r.URL.Path {
		case "/api/v1/operator/cluster-settings":
			_, _ = w.Write([]byte(`{"code":10000,"msg":"ok","data":{"cluster":"east","namespaces":{"include":["shop"],"exclude":[]},"collect_events":true,"skip_reasons":["Pulled"]}}`))
		case "/api/v1/operator/events":
			_, _ = w.Write([]byte(`{"code":10000,"msg":"ok","data":{"added":1}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d := &PlatformDecider{Cluster: "east", Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk"}}
	s, err := d.ClusterSettings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "east", s.Cluster)
	assert.Equal(t, []string{"shop"}, s.Namespaces.Include)
	assert.Equal(t, []string{"Pulled"}, s.SkipReasons)
	require.NoError(t, d.ReportEvents(context.Background(), clusteragent.Batch{Events: []clusteragent.Event{{UID: "u", LastAt: evAt}}}))
	assert.Equal(t, []string{"GET /api/v1/operator/cluster-settings cluster=east", "POST /api/v1/operator/events cluster=east"}, seen)

	// an operator started without --cluster names none: it is the default cluster's, as before
	seen = nil
	_, err = (&PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk"}}).ClusterSettings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"GET /api/v1/operator/cluster-settings cluster="}, seen)

	// a platform that says no is told apart by its status
	err = (&PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL + "/nowhere", Credential: "ak:sk"}}).ReportEvents(context.Background(), clusteragent.Batch{})
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	assert.True(t, IsUnreachable(err))
}
