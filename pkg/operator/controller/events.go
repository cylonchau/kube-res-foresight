package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The Kubernetes Events of the cluster, read-only, for the platform (P12 E3): the reasons of what happens to nodes, Pods, workloads and autoscalers are in the events, said by
// the component that did it. The collector watches them, keeps the ones the platform is interested in (the namespaces it was told, and not the reasons it was told are noise),
// and sends them in batches. It runs on the elected replica only, like the module sync, and apart from it: a platform that cannot be reached, a cluster that does not let
// the operator read events, or a storm of events slow nothing that scales a workload. What it cannot send is held in a bounded buffer, and when that is full the oldest
// events are dropped and counted: the events are a record, never a reason to grow without limit.

// The sizes and times of the collector.
const (
	// DefaultEventFlush is how often the collected events are sent.
	DefaultEventFlush = 10 * time.Second
	// DefaultEventBuffer is how many events are held while the platform cannot take them.
	DefaultEventBuffer = 5000
	// DefaultSettingsEvery is how often the platform is asked which namespaces and reasons to collect.
	DefaultSettingsEvery = time.Minute
	maxEventBackoff      = 5 * time.Minute
	// informerWarnAfter is when the collector says the events cannot be read yet (the role does not let the operator, most likely).
	informerWarnAfter = time.Minute
)

// EventPlatform is the platform the collector talks to.
type EventPlatform interface {
	ClusterSettings(ctx context.Context) (clusteragent.Settings, error)
	ReportEvents(ctx context.Context, b clusteragent.Batch) error
}

// clusterScopedKinds are the kinds whose events carry no namespace of the object: they are never limited by the namespaces.
var clusterScopedKinds = map[string]bool{"node": true, "persistentvolume": true, "namespace": true, "storageclass": true, "csinode": true, "nodeclaim": true, "nodepool": true}

// eventOf makes the event the platform is sent, and says which namespace it is about ("" for an object that is in none). Nothing in it is translated or merged.
func eventOf(ev *corev1.Event) (clusteragent.Event, string) {
	out := clusteragent.Event{UID: string(ev.UID), Type: ev.Type, Reason: ev.Reason, Message: ev.Message,
		Component: ev.ReportingController, Host: ev.Source.Host, Count: int(ev.Count),
		Kind: ev.InvolvedObject.Kind, Namespace: ev.InvolvedObject.Namespace, Name: ev.InvolvedObject.Name, ObjectUID: string(ev.InvolvedObject.UID)}
	if out.Component == "" {
		out.Component = ev.Source.Component
	}
	if out.Host == "" {
		out.Host = ev.ReportingInstance
	}
	if out.Count < 1 {
		out.Count = 1
	}
	first, last := ev.FirstTimestamp.Time, ev.LastTimestamp.Time
	if first.IsZero() {
		first = ev.EventTime.Time
	}
	if first.IsZero() {
		first = ev.CreationTimestamp.Time
	}
	if last.IsZero() {
		last = ev.EventTime.Time
	}
	if s := ev.Series; s != nil {
		// an event that repeats is one object with a series: how many times, and when it last happened
		if int(s.Count) > out.Count {
			out.Count = int(s.Count)
		}
		if !s.LastObservedTime.IsZero() {
			last = s.LastObservedTime.Time
		}
	}
	if last.IsZero() {
		last = first
	}
	if first.IsZero() {
		first = last
	}
	out.FirstAt, out.LastAt = first.UTC(), last.UTC()
	ns := ev.InvolvedObject.Namespace
	if ns == "" && !clusterScopedKinds[strings.ToLower(ev.InvolvedObject.Kind)] {
		ns = ev.Namespace
	}
	return out, ns
}

// pendingEvents are the events waiting to be sent: one for each uid (the later or the higher count stands), bounded.
type pendingEvents struct {
	mu      sync.Mutex
	max     int
	byUID   map[string]clusteragent.Event
	dropped int64
}

func newPending(max int) *pendingEvents {
	if max < 1 {
		max = DefaultEventBuffer
	}
	return &pendingEvents{max: max, byUID: map[string]clusteragent.Event{}}
}

func newer(a, b clusteragent.Event) bool {
	return a.LastAt.After(b.LastAt) || (a.LastAt.Equal(b.LastAt) && a.Count > b.Count)
}

// add keeps an event; a full buffer drops the oldest tenth first, and counts them.
func (p *pendingEvents) add(e clusteragent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.byUID[e.UID]; ok {
		if newer(e, cur) {
			p.byUID[e.UID] = e
		}
		return
	}
	if len(p.byUID) >= p.max {
		all := make([]clusteragent.Event, 0, len(p.byUID))
		for _, v := range p.byUID {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].LastAt.Before(all[j].LastAt) })
		drop := len(all)/10 + 1
		for _, v := range all[:drop] {
			delete(p.byUID, v.UID)
		}
		p.dropped += int64(drop)
	}
	p.byUID[e.UID] = e
}

func (p *pendingEvents) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byUID)
}

func (p *pendingEvents) clear() {
	p.mu.Lock()
	p.byUID = map[string]clusteragent.Event{}
	p.mu.Unlock()
}

// take is up to n events, the oldest first, left in the buffer until done says they were taken.
func (p *pendingEvents) take(n int) []clusteragent.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	all := make([]clusteragent.Event, 0, len(p.byUID))
	for _, v := range p.byUID {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LastAt.Before(all[j].LastAt) })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// done removes what was sent, unless the event happened again while it was being sent (then the newer one stays for the next batch).
func (p *pendingEvents) done(sent []clusteragent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range sent {
		if cur, ok := p.byUID[e.UID]; ok && !newer(cur, e) {
			delete(p.byUID, e.UID)
		}
	}
}

// EventCollector watches the events of the cluster and sends them to the platform.
type EventCollector struct {
	// Cache is the cache of the manager: the informer over the events is made in it, and only on the elected replica.
	Cache    cache.Cache
	Platform EventPlatform
	// Operator names this Pod in what is sent.
	Operator string
	// Refresh is how often the platform is asked for the settings; nil is a minute. Flush is how often the events are sent; zero is DefaultEventFlush.
	Refresh func() time.Duration
	Flush   time.Duration
	// MaxPending bounds the buffer; zero is DefaultEventBuffer.
	MaxPending int

	once     sync.Once
	pend     *pendingEvents
	mu       sync.RWMutex
	settings *clusteragent.Settings
	started  bool
	sent     int64
	lastErr  string
}

func (c *EventCollector) pending() *pendingEvents {
	c.once.Do(func() { c.pend = newPending(c.MaxPending) })
	return c.pend
}

// NeedLeaderElection keeps the collection on the elected replica.
func (c *EventCollector) NeedLeaderElection() bool { return true }

func (c *EventCollector) current() (clusteragent.Settings, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.settings == nil {
		return clusteragent.Settings{}, false
	}
	return *c.settings, true
}

// Use sets the settings in force (what the platform said last).
func (c *EventCollector) Use(s clusteragent.Settings) {
	c.mu.Lock()
	prev := c.settings
	c.settings = &s
	c.mu.Unlock()
	if !s.CollectEvents || (prev != nil && !scopeEqual(prev.Namespaces, s.Namespaces)) {
		// switched off, or the namespaces changed: what is waiting was collected under the old rule
		c.pending().clear()
	}
}

func scopeEqual(a, b clusteragent.Scope) bool {
	eq := func(x, y []string) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	return eq(a.Include, b.Include) && eq(a.Exclude, b.Exclude)
}

// observe takes one event of the cluster: it is kept when the platform's settings say to collect it.
func (c *EventCollector) observe(obj any) {
	ev, ok := obj.(*corev1.Event)
	if !ok || ev == nil || ev.UID == "" {
		return
	}
	s, have := c.current()
	if !have || !s.CollectEvents {
		return
	}
	out, ns := eventOf(ev)
	if !s.Namespaces.Allows(ns) || clusteragent.Skipped(s.SkipReasons, out.Reason) {
		return
	}
	c.pending().add(out)
}

// statusOf is the HTTP status the platform answered with, or 0.
func statusOf(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// flushOnce sends one batch. It says whether the buffer has more to send, and whether the platform refused or could not be reached (the caller waits longer then).
func (c *EventCollector) flushOnce(ctx context.Context) (more bool, failed bool) {
	if _, have := c.current(); !have {
		return false, false
	}
	batch := c.pending().take(clusteragent.MaxBatchEvents)
	if len(batch) == 0 {
		return false, false
	}
	err := c.Platform.ReportEvents(ctx, clusteragent.Batch{Operator: c.Operator, Events: batch})
	if err == nil {
		c.pending().done(batch)
		c.mu.Lock()
		c.sent += int64(len(batch))
		c.lastErr = ""
		c.mu.Unlock()
		return c.pending().len() > 0, false
	}
	if code := statusOf(err); code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge {
		// the platform will not take this batch however often it is sent: drop it, so it does not hold the ones behind it
		logf.Log.WithName("events").Error(err, "the platform refused a batch of events; dropped", "events", len(batch))
		c.pending().done(batch)
		return c.pending().len() > 0, false
	}
	c.mu.Lock()
	msg := err.Error()
	first := c.lastErr == ""
	c.lastErr = msg
	c.mu.Unlock()
	if first {
		logf.Log.WithName("events").Info("the events could not be sent; they are kept and sent again", "error", msg, "waiting", c.pending().len())
	}
	return false, true
}

func (c *EventCollector) flushLoop(ctx context.Context) {
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

// startInformer makes the informer over the events and watches it. It blocks until the cache has listed them (so it runs on its own), and says so when that does not
// happen within a minute: the role of the operator may not allow it to read events.
func (c *EventCollector) startInformer(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()
	go func() {
		log := logf.Log.WithName("events")
		watchdog := time.AfterFunc(informerWarnAfter, func() {
			log.Info("the events of the cluster cannot be read yet: check that the role of the operator lets it get, list and watch events")
		})
		defer watchdog.Stop()
		inf, err := c.Cache.GetInformer(ctx, &corev1.Event{})
		if err != nil {
			if ctx.Err() == nil {
				log.Error(err, "the informer over the events could not be made")
			}
			return
		}
		watchdog.Stop()
		_, err = inf.AddEventHandler(eventHandler{c})
		if err != nil {
			log.Error(err, "the handler of the events could not be added")
		}
	}()
}

// Start implements manager.Runnable: it asks the platform for the settings now and from time to time, starts watching the events once the platform says to collect them,
// and sends what was collected.
func (c *EventCollector) Start(ctx context.Context) error {
	log := logf.Log.WithName("events")
	go c.flushLoop(ctx)
	failures := 0
	for {
		s, err := c.Platform.ClusterSettings(ctx)
		switch {
		case err == nil:
			failures = 0
			c.Use(s)
			if s.CollectEvents {
				c.startInformer(ctx)
			}
		default:
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
			return nil
		case <-time.After(every):
		}
	}
}

// eventHandler is the cache.ResourceEventHandler of the informer.
type eventHandler struct{ c *EventCollector }

func (h eventHandler) OnAdd(obj any, _ bool) { h.c.observe(obj) }
func (h eventHandler) OnUpdate(_, obj any)   { h.c.observe(obj) }
func (h eventHandler) OnDelete(any)          {}

// ClusterSettings implements EventPlatform: what the platform says to collect of this operator's cluster.
func (d *PlatformDecider) ClusterSettings(ctx context.Context) (clusteragent.Settings, error) {
	data, err := d.do(ctx, http.MethodGet, "/api/v1/operator/cluster-settings", url.Values{}, nil)
	if err != nil {
		return clusteragent.Settings{}, err
	}
	var env struct {
		Code int                   `json:"code"`
		Msg  string                `json:"msg"`
		Data clusteragent.Settings `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return clusteragent.Settings{}, unreachable(fmt.Errorf("unreadable platform response: %w", err))
	}
	if env.Code != 10000 {
		return clusteragent.Settings{}, unreachable(fmt.Errorf("platform error %d: %s", env.Code, env.Msg))
	}
	return env.Data, nil
}

// ReportEvents implements EventPlatform: one batch of events.
func (d *PlatformDecider) ReportEvents(ctx context.Context, b clusteragent.Batch) error {
	payload, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = d.do(ctx, http.MethodPost, "/api/v1/operator/events", url.Values{}, strings.NewReader(string(payload)))
	return err
}
