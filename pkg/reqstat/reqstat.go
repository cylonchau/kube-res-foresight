// Package reqstat measures how long the requests of the platform take and where the time goes (P11 B0): for every route, a histogram of the time
// a request took, how many ended in a 5xx, how many were stopped by the platform's own time limit (504) and how many the other side gave up on, and
// how much of the time was spent in the database and in Prometheus. The hours are kept in the database, so the baseline is there after a restart,
// and from them the platform writes down which route is slow, which layer the time is in and why: the conclusion of B0 comes from the numbers of
// the environment the platform runs in, not from a guess made over the code.
package reqstat

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Bounds are the upper edges, in milliseconds, of the buckets of the histogram of the time of a request. A percentile read from it is the upper edge
// of the bucket it falls in, so it is "at most": the figure is exact to the width of the bucket, not to the millisecond. The last bucket has no edge.
var Bounds = []int64{25, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 20000, 30000, 45000, 60000, 90000, 120000, 300000}

// BucketCount is the number of buckets: one for each bound and one above the last.
func BucketCount() int { return len(Bounds) + 1 }

// BucketOf is the bucket a duration falls in.
func BucketOf(ms int64) int {
	return sort.Search(len(Bounds), func(i int) bool { return ms <= Bounds[i] })
}

// Span is the time one request spent waiting on the layers below the handler: it is carried in the context of the request and added to by the
// database callbacks and the Prometheus client, whichever goroutine they run on.
type Span struct {
	dbNanos, dbCalls, promNanos, promCalls atomic.Int64
}

type spanKey struct{}

// WithSpan puts a new span in the context.
func WithSpan(ctx context.Context) (context.Context, *Span) {
	s := &Span{}
	return context.WithValue(ctx, spanKey{}, s), s
}

// SpanOf is the span of the context, nil when the work does not belong to a request.
func SpanOf(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}

// AddDB adds the time of one statement (a wait for a connection included) to the span in the context, if there is one.
func AddDB(ctx context.Context, d time.Duration) {
	if s := SpanOf(ctx); s != nil {
		s.dbNanos.Add(int64(d))
		s.dbCalls.Add(1)
	}
}

// AddProm adds the time of one query to Prometheus to the span in the context, if there is one.
func AddProm(ctx context.Context, d time.Duration) {
	if s := SpanOf(ctx); s != nil {
		s.promNanos.Add(int64(d))
		s.promCalls.Add(1)
	}
}

// Totals are what the span collected, in milliseconds. Statements that ran in parallel are added up, so a layer's time can exceed the request's.
func (s *Span) Totals() (dbMs, dbCalls, promMs, promCalls int64) {
	if s == nil {
		return
	}
	return s.dbNanos.Load() / int64(time.Millisecond), s.dbCalls.Load(), s.promNanos.Load() / int64(time.Millisecond), s.promCalls.Load()
}

// Slowest is one slow request kept as an example, so its request id can be looked for in the gateway log and the operation log.
type Slowest struct {
	RequestID string    `json:"request_id"`
	Millis    int64     `json:"ms"`
	Status    int       `json:"status"`
	At        time.Time `json:"at"`
	Abandoned bool      `json:"abandoned,omitempty"`
}

// MaxExamples is how many slow requests an hour of a route keeps.
const MaxExamples = 3

// Observation is one finished request.
type Observation struct {
	Method    string
	Route     string
	Status    int
	Took      time.Duration
	DBMs      int64
	DBCalls   int64
	PromMs    int64
	PromCalls int64
	RequestID string
	// Abandoned is true when the other side (the browser, the gateway) closed the connection before the answer was written.
	Abandoned bool
	// PlatformTimeout is true when the platform stopped the request at its own time limit and answered 504 with X-Timeout-Source: platform.
	PlatformTimeout bool
	At              time.Time
}

// Hour is the statistics of one route during one hour, as one process saw it.
type Hour struct {
	Hour      time.Time `json:"hour"`
	Method    string    `json:"method"`
	Route     string    `json:"route"`
	Count     int64     `json:"count"`
	Status4xx int64     `json:"status_4xx"`
	Status5xx int64     `json:"status_5xx"`
	Timeouts  int64     `json:"timeouts"`
	Abandoned int64     `json:"abandoned"`
	SumMs     int64     `json:"sum_ms"`
	MaxMs     int64     `json:"max_ms"`
	DBMs      int64     `json:"db_ms"`
	DBCalls   int64     `json:"db_calls"`
	PromMs    int64     `json:"prom_ms"`
	PromCalls int64     `json:"prom_calls"`
	Buckets   []int64   `json:"buckets"`
	Examples  []Slowest `json:"examples"`
}

func newHour(hour time.Time, method, route string) *Hour {
	return &Hour{Hour: hour, Method: method, Route: route, Buckets: make([]int64, BucketCount())}
}

func (h *Hour) add(o Observation) {
	ms := o.Took.Milliseconds()
	h.Count++
	switch {
	case o.Status >= 500:
		h.Status5xx++
	case o.Status >= 400:
		h.Status4xx++
	}
	if o.PlatformTimeout {
		h.Timeouts++
	}
	if o.Abandoned {
		h.Abandoned++
	}
	h.SumMs += ms
	if ms > h.MaxMs {
		h.MaxMs = ms
	}
	h.DBMs += o.DBMs
	h.DBCalls += o.DBCalls
	h.PromMs += o.PromMs
	h.PromCalls += o.PromCalls
	h.Buckets[BucketOf(ms)]++
	h.Examples = keepSlowest(h.Examples, Slowest{RequestID: o.RequestID, Millis: ms, Status: o.Status, At: o.At, Abandoned: o.Abandoned})
}

// keepSlowest keeps the MaxExamples slowest, the slowest first.
func keepSlowest(list []Slowest, s Slowest) []Slowest {
	list = append(list, s)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Millis > list[j].Millis })
	if len(list) > MaxExamples {
		list = list[:MaxExamples]
	}
	return list
}

// Merge adds the statistics of another hour of the same route to this one.
func (h *Hour) Merge(o *Hour) {
	h.Count += o.Count
	h.Status4xx += o.Status4xx
	h.Status5xx += o.Status5xx
	h.Timeouts += o.Timeouts
	h.Abandoned += o.Abandoned
	h.SumMs += o.SumMs
	if o.MaxMs > h.MaxMs {
		h.MaxMs = o.MaxMs
	}
	h.DBMs += o.DBMs
	h.DBCalls += o.DBCalls
	h.PromMs += o.PromMs
	h.PromCalls += o.PromCalls
	for i := range o.Buckets {
		if i < len(h.Buckets) {
			h.Buckets[i] += o.Buckets[i]
		}
	}
	for _, e := range o.Examples {
		h.Examples = keepSlowest(h.Examples, e)
	}
}

// Percentile is the upper edge, in milliseconds, of the bucket the q-th fraction of the requests fall in (nearest rank); 0 when there are none.
// A request in the last bucket is reported as the largest recorded time, since it has no edge.
func (h *Hour) Percentile(q float64) int64 {
	if h.Count == 0 {
		return 0
	}
	rank := int64(q*float64(h.Count) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	var seen int64
	for i, n := range h.Buckets {
		seen += n
		if seen >= rank {
			if i < len(Bounds) {
				return Bounds[i]
			}
			return h.MaxMs
		}
	}
	return h.MaxMs
}

type routeKey struct{ method, route string }

// Recorder collects the observations of this process, by route and hour, and hands the hours that changed to be stored.
type Recorder struct {
	mu    sync.Mutex
	hours map[routeKey]map[int64]*Hour
	dirty map[routeKey]map[int64]bool
	Now   func() time.Time
}

// NewRecorder makes an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{hours: map[routeKey]map[int64]*Hour{}, dirty: map[routeKey]map[int64]bool{}}
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Observe records one finished request.
func (r *Recorder) Observe(o Observation) {
	if o.Route == "" {
		return
	}
	if o.At.IsZero() {
		o.At = r.now()
	}
	hour := o.At.UTC().Truncate(time.Hour)
	key := routeKey{o.Method, o.Route}
	r.mu.Lock()
	defer r.mu.Unlock()
	byHour := r.hours[key]
	if byHour == nil {
		byHour = map[int64]*Hour{}
		r.hours[key] = byHour
	}
	h := byHour[hour.Unix()]
	if h == nil {
		h = newHour(hour, o.Method, o.Route)
		byHour[hour.Unix()] = h
	}
	h.add(o)
	d := r.dirty[key]
	if d == nil {
		d = map[int64]bool{}
		r.dirty[key] = d
	}
	d[hour.Unix()] = true
}

// TakeChanged returns copies of the hours that changed since the last call, and drops from memory the hours that ended more than an hour ago: they
// are stored and will not change again.
func (r *Recorder) TakeChanged() []Hour {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Hour
	for key, set := range r.dirty {
		for unix := range set {
			h := r.hours[key][unix]
			if h == nil {
				continue
			}
			cp := *h
			cp.Buckets = append([]int64(nil), h.Buckets...)
			cp.Examples = append([]Slowest(nil), h.Examples...)
			out = append(out, cp)
		}
		delete(r.dirty, key)
	}
	cutoff := r.now().UTC().Truncate(time.Hour).Add(-time.Hour).Unix()
	for key, byHour := range r.hours {
		for unix := range byHour {
			if unix < cutoff && !r.dirty[key][unix] {
				delete(byHour, unix)
			}
		}
		if len(byHour) == 0 {
			delete(r.hours, key)
		}
	}
	return out
}

// Redirty puts hours back among the changed ones, when they could not be stored.
func (r *Recorder) Redirty(hours []Hour) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range hours {
		key := routeKey{h.Method, h.Route}
		if r.dirty[key] == nil {
			r.dirty[key] = map[int64]bool{}
		}
		r.dirty[key][h.Hour.Unix()] = true
	}
}

// TimeProm starts timing one query to Prometheus: call the function it returns when the query is done (defer reqstat.TimeProm(ctx)()). A query that
// belongs to no request (the collection job) costs one context lookup and records nothing.
func TimeProm(ctx context.Context) func() {
	if SpanOf(ctx) == nil {
		return func() {}
	}
	start := time.Now()
	return func() { AddProm(ctx, time.Since(start)) }
}
