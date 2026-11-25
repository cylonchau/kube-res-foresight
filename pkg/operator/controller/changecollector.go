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

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The change collector (P12 E7, E8) watches the nodes, the Pods, the Deployments and StatefulSets and the HPAs of the cluster, read-only, and reports what changed in them.
// Like the event collector it runs on the elected replica only and on its own: a platform that cannot be reached, a role that does not let it read, or a storm of Pod
// changes slow nothing that scales a workload, and what cannot be sent is held in a bounded buffer. Unlike it, it holds the state of the objects, so it makes its own cache,
// and only while the platform says to collect: a standby replica holds nothing, and switching the collection off on the platform lets the memory go.

// ChangePlatform is the platform the collector talks to.
type ChangePlatform interface {
	ClusterSettings(ctx context.Context) (clusteragent.Settings, error)
	ReportChanges(ctx context.Context, b clusteragent.ChangeBatch) error
}

// pendingChanges are the changes waiting to be sent: one for each record id, bounded; when it is full the oldest tenth goes first, and is counted.
type pendingChanges struct {
	mu      sync.Mutex
	max     int
	byID    map[string]clusteragent.Change
	dropped int64
}

func newPendingChanges(max int) *pendingChanges {
	if max < 1 {
		max = DefaultChangeBuffer
	}
	return &pendingChanges{max: max, byID: map[string]clusteragent.Change{}}
}

// DefaultChangeBuffer is how many changes are held while the platform cannot take them.
const DefaultChangeBuffer = 5000

func (p *pendingChanges) add(c clusteragent.Change) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byID[c.RecordID]; ok {
		return
	}
	if len(p.byID) >= p.max {
		all := make([]clusteragent.Change, 0, len(p.byID))
		for _, v := range p.byID {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].OccurredAt.Before(all[j].OccurredAt) })
		drop := len(all)/10 + 1
		for _, v := range all[:drop] {
			delete(p.byID, v.RecordID)
		}
		p.dropped += int64(drop)
	}
	p.byID[c.RecordID] = c
}

func (p *pendingChanges) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byID)
}

func (p *pendingChanges) clear() {
	p.mu.Lock()
	p.byID = map[string]clusteragent.Change{}
	p.mu.Unlock()
}

// take is up to n changes, the oldest first, left in the buffer until done.
func (p *pendingChanges) take(n int) []clusteragent.Change {
	p.mu.Lock()
	defer p.mu.Unlock()
	all := make([]clusteragent.Change, 0, len(p.byID))
	for _, v := range p.byID {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].OccurredAt.Before(all[j].OccurredAt) })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

func (p *pendingChanges) done(sent []clusteragent.Change) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range sent {
		delete(p.byID, c.RecordID)
	}
}

// ChangeCollector watches the objects and sends their changes to the platform.
type ChangeCollector struct {
	// Config and Scheme make the cache of the collector (the collector does not use the cache of the manager: that one is started on every replica). NewCache replaces
	// them, for a test.
	Config   *rest.Config
	Scheme   *runtime.Scheme
	NewCache func() (cache.Cache, error)
	Platform ChangePlatform
	// Operator names this Pod in what is sent.
	Operator string
	// Refresh is how often the platform is asked for the settings; nil is a minute. Flush is how often the changes are sent; zero is DefaultEventFlush.
	Refresh func() time.Duration
	Flush   time.Duration
	// MaxPending bounds the buffer; zero is DefaultChangeBuffer.
	MaxPending int
	// Now is the clock of the detector (the time of a change that the object does not date).
	Now func() time.Time

	once     sync.Once
	pend     *pendingChanges
	mu       sync.RWMutex
	settings *clusteragent.Settings
	watching bool
	stop     context.CancelFunc
	synced   bool
	pods     PodLister
	lastErr  string
}

func (c *ChangeCollector) pending() *pendingChanges {
	c.once.Do(func() { c.pend = newPendingChanges(c.MaxPending) })
	return c.pend
}

// NeedLeaderElection keeps the collection on the elected replica.
func (c *ChangeCollector) NeedLeaderElection() bool { return true }

func (c *ChangeCollector) current() (clusteragent.Settings, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.settings == nil {
		return clusteragent.Settings{}, false
	}
	return *c.settings, true
}

func (c *ChangeCollector) detector() *detector {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &detector{now: c.Now, pods: c.pods}
}

// Use sets the settings in force. A change of namespaces drops what waits (it was collected under the old rule); switching the collection off stops the watch and lets
// its memory go.
func (c *ChangeCollector) Use(ctx context.Context, s clusteragent.Settings) {
	c.mu.Lock()
	prev := c.settings
	c.settings = &s
	c.mu.Unlock()
	if !s.CollectChanges || (prev != nil && !scopeEqual(prev.Namespaces, s.Namespaces)) {
		c.pending().clear()
	}
	if !s.CollectChanges {
		c.stopWatching()
		return
	}
	c.startWatching(ctx)
}

func (c *ChangeCollector) stopWatching() {
	c.mu.Lock()
	stop := c.stop
	c.stop, c.watching, c.synced, c.pods = nil, false, false, nil
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// emit keeps the changes of an object that the namespaces of the cluster let through; a node is in none, and is never left out.
func (c *ChangeCollector) emit(namespace string, changes ...clusteragent.Change) {
	s, ok := c.current()
	if !ok || !s.CollectChanges || !s.Namespaces.Allows(namespace) {
		return
	}
	for _, ch := range changes {
		c.pending().add(ch)
	}
}

// ready is true once the cache has listed everything the first time: an object that appears before that was there already, and is not a change.
func (c *ChangeCollector) ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.synced
}

func (c *ChangeCollector) onAdd(obj any, initial bool) {
	if initial || !c.ready() {
		return
	}
	d := c.detector()
	switch o := obj.(type) {
	case *corev1.Node:
		c.emit("", d.nodeCreated(o))
	case *corev1.Pod:
		c.emit(o.Namespace, d.podCreated(o))
	case *autoscalingv2.HorizontalPodAutoscaler:
		c.emit(o.Namespace, d.hpaCreated(o))
	default:
		if v, ok := viewOf(obj); ok {
			c.emit(v.namespace, d.workloadCreated(v))
		}
	}
}

func (c *ChangeCollector) onUpdate(oldObj, newObj any) {
	if !c.ready() {
		return
	}
	d := c.detector()
	switch n := newObj.(type) {
	case *corev1.Node:
		if o, ok := oldObj.(*corev1.Node); ok {
			c.emit("", d.nodeChanges(o, n)...)
		}
	case *corev1.Pod:
		if o, ok := oldObj.(*corev1.Pod); ok {
			c.emit(n.Namespace, d.podChanges(o, n)...)
		}
	case *autoscalingv2.HorizontalPodAutoscaler:
		if o, ok := oldObj.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			c.emit(n.Namespace, d.hpaChanges(o, n)...)
		}
	default:
		nv, ok1 := viewOf(newObj)
		ov, ok2 := viewOf(oldObj)
		if ok1 && ok2 {
			c.emit(nv.namespace, d.workloadChanges(ov, nv)...)
		}
	}
}

func (c *ChangeCollector) onDelete(obj any) {
	if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tomb.Obj
	}
	if !c.ready() {
		return
	}
	d := c.detector()
	switch o := obj.(type) {
	case *corev1.Node:
		c.emit("", d.nodeDeleted(o))
	case *corev1.Pod:
		c.emit(o.Namespace, d.podDeleted(o))
	case *autoscalingv2.HorizontalPodAutoscaler:
		c.emit(o.Namespace, d.hpaDeleted(o))
	default:
		if v, ok := viewOf(obj); ok {
			c.emit(v.namespace, d.workloadDeleted(v))
		}
	}
}

type changeHandler struct{ c *ChangeCollector }

func (h changeHandler) OnAdd(obj any, initial bool) { h.c.onAdd(obj, initial) }
func (h changeHandler) OnUpdate(o, n any)           { h.c.onUpdate(o, n) }
func (h changeHandler) OnDelete(obj any)            { h.c.onDelete(obj) }

// cachePods lists the Pods of a node from the cache of the collector, through the index on the node a Pod is on.
type cachePods struct{ cache cache.Cache }

const podNodeIndex = "spec.nodeName"

func (p cachePods) PodsOnNode(node string) []*corev1.Pod {
	var list corev1.PodList
	if err := p.cache.List(context.Background(), &list, client.MatchingFields{podNodeIndex: node}); err != nil {
		return nil
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out
}

// trimPodSpec drops what a Pod or a template carries and the collector never reads (the environment, the volumes, the probes): a cluster has many Pods.
func trimPodSpec(spec *corev1.PodSpec) {
	spec.Volumes, spec.InitContainers, spec.EphemeralContainers, spec.ImagePullSecrets, spec.Affinity, spec.Tolerations, spec.TopologySpreadConstraints = nil, nil, nil, nil, nil, nil, nil
	for i := range spec.Containers {
		c := &spec.Containers[i]
		spec.Containers[i] = corev1.Container{Name: c.Name, Resources: c.Resources}
	}
}

func trim(obj any) (any, error) {
	switch o := obj.(type) {
	case *corev1.Pod:
		o.ManagedFields, o.Annotations = nil, nil
		trimPodSpec(&o.Spec)
	case *corev1.Node:
		o.ManagedFields, o.Annotations = nil, nil
		o.Status.Images, o.Status.VolumesInUse, o.Status.VolumesAttached = nil, nil, nil
	case *autoscalingv2.HorizontalPodAutoscaler:
		o.ManagedFields, o.Annotations = nil, nil
	case *appsv1.Deployment:
		o.Annotations = nil // the managed fields are kept: they say who wrote the replicas
		trimPodSpec(&o.Spec.Template.Spec)
	case *appsv1.StatefulSet:
		o.Annotations = nil
		trimPodSpec(&o.Spec.Template.Spec)
		o.Spec.VolumeClaimTemplates = nil
	}
	return obj, nil
}

func (c *ChangeCollector) makeCache() (cache.Cache, error) {
	if c.NewCache != nil {
		return c.NewCache()
	}
	transform := toolscache.TransformFunc(trim)
	opts := cache.Options{Scheme: c.Scheme, DefaultTransform: transform}
	return cache.New(c.Config, opts)
}

// startWatching makes the cache and the informers over the objects, once. The first list is not a change: the collector is ready (and reports) once it has finished.
func (c *ChangeCollector) startWatching(parent context.Context) {
	c.mu.Lock()
	if c.watching {
		c.mu.Unlock()
		return
	}
	c.watching = true
	ctx, cancel := context.WithCancel(parent)
	c.stop = cancel
	c.mu.Unlock()
	go func() {
		log := logf.Log.WithName("changes")
		ca, err := c.makeCache()
		if err != nil {
			log.Error(err, "the cache over the cluster could not be made; no changes are collected")
			return
		}
		if err := ca.IndexField(ctx, &corev1.Pod{}, podNodeIndex, func(o client.Object) []string {
			if p, ok := o.(*corev1.Pod); ok && p.Spec.NodeName != "" {
				return []string{p.Spec.NodeName}
			}
			return nil
		}); err != nil {
			log.Error(err, "the index of the Pods by node could not be made: a node's change will not list its Pods")
		} else {
			c.mu.Lock()
			c.pods = cachePods{cache: ca}
			c.mu.Unlock()
		}
		go func() {
			if err := ca.Start(ctx); err != nil && ctx.Err() == nil {
				log.Error(err, "the cache over the cluster stopped")
			}
		}()
		for _, obj := range []client.Object{&corev1.Node{}, &corev1.Pod{}, &appsv1.Deployment{}, &appsv1.StatefulSet{}, &autoscalingv2.HorizontalPodAutoscaler{}} {
			inf, err := ca.GetInformer(ctx, obj)
			if err != nil {
				if ctx.Err() == nil {
					log.Error(err, "an informer could not be made", "kind", fmt.Sprintf("%T", obj))
				}
				return
			}
			if _, err := inf.AddEventHandler(changeHandler{c}); err != nil {
				log.Error(err, "a handler could not be added")
			}
		}
		warn := time.AfterFunc(informerWarnAfter, func() {
			log.Info("the objects of the cluster cannot be read yet: check that the role of the operator lets it get, list and watch nodes, pods, deployments, statefulsets and horizontalpodautoscalers")
		})
		defer warn.Stop()
		if ca.WaitForCacheSync(ctx) {
			c.mu.Lock()
			c.synced = true
			c.mu.Unlock()
		}
	}()
}

func (c *ChangeCollector) flushOnce(ctx context.Context) (more bool, failed bool) {
	if _, have := c.current(); !have {
		return false, false
	}
	batch := c.pending().take(clusteragent.MaxBatchChanges)
	if len(batch) == 0 {
		return false, false
	}
	err := c.Platform.ReportChanges(ctx, clusteragent.ChangeBatch{Operator: c.Operator, Changes: batch})
	if err == nil {
		c.pending().done(batch)
		c.mu.Lock()
		c.lastErr = ""
		c.mu.Unlock()
		return c.pending().len() > 0, false
	}
	if code := statusOf(err); code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge {
		logf.Log.WithName("changes").Error(err, "the platform refused a batch of changes; dropped", "changes", len(batch))
		c.pending().done(batch)
		return c.pending().len() > 0, false
	}
	c.mu.Lock()
	first := c.lastErr == ""
	c.lastErr = err.Error()
	c.mu.Unlock()
	if first {
		logf.Log.WithName("changes").Info("the changes could not be sent; they are kept and sent again", "error", err.Error(), "waiting", c.pending().len())
	}
	return false, true
}

func (c *ChangeCollector) flushLoop(ctx context.Context) {
	every := c.Flush
	if every <= 0 {
		every = DefaultEventFlush
	}
	wait := every
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		more, failed := c.flushOnce(ctx)
		for more && ctx.Err() == nil {
			more, failed = c.flushOnce(ctx)
		}
		if failed {
			wait = 6 * every
			if wait > maxEventBackoff {
				wait = maxEventBackoff
			}
		}
	}
}

// Start implements manager.Runnable.
func (c *ChangeCollector) Start(ctx context.Context) error {
	log := logf.Log.WithName("changes")
	go c.flushLoop(ctx)
	failures := 0
	for {
		s, err := c.Platform.ClusterSettings(ctx)
		if err == nil {
			failures = 0
			c.Use(ctx, s)
		} else {
			if failures == 0 {
				log.Info("the settings of the cluster could not be read from the platform; the last ones stand", "error", err.Error())
			}
			failures++
		}
		every := DefaultSettingsEvery
		if c.Refresh != nil {
			if d := c.Refresh(); d > 0 {
				every = d
			}
		}
		select {
		case <-ctx.Done():
			c.stopWatching()
			return nil
		case <-time.After(every):
		}
	}
}

// ReportChanges implements ChangePlatform: one batch of changes.
func (d *PlatformDecider) ReportChanges(ctx context.Context, b clusteragent.ChangeBatch) error {
	payload, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = d.do(ctx, http.MethodPost, "/api/v1/operator/changes", url.Values{}, strings.NewReader(string(payload)))
	return err
}
