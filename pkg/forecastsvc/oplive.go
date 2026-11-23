package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
)

// The resource dashboard reads the use of a module from Prometheus itself, as it is now: one query_range of the PromQL the user wrote in the metric mapping of
// each source, over the window, and every figure, every chart and the verdict are made of those points. Nothing is read from the samples the platform stored
// for the module, and nothing is forecast. The queries that were sent are returned with the figures (LiveQuery), so each can be checked in Grafana.

// LiveQuery is the query sent to Prometheus for one metric of the module, and what came of it.
type LiveQuery struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Group string `json:"group"`
	Level string `json:"level"`
	Unit  string `json:"unit"`
	// Query is the PromQL as it was sent (the mapping rendered for the module), Step the step of the range query, Points how many points it answered: the
	// figures of the metric are made of that many samples.
	Query string `json:"query"`
	// Per says what the query answers: one Pod ("pod") or all the replicas together ("total"), as the module's source says or as the level of the template does.
	Per    string `json:"per"`
	Step   string `json:"step"`
	Points int    `json:"points"`
	// Error says why there are no points; Skipped is true when the time limit of the request was over before the query was sent or answered.
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// liveRead is what the loader keeps of reading Prometheus.
type liveRead struct {
	queries    []LiveQuery
	datasource CostSource
	repMaxAge  time.Duration
}

// livePriority are the kinds asked first: the ones the page needs at once.
var livePriority = []string{"cpu", "memory", "replicas", "k8s_cpu_requests", "k8s_mem_requests", "k8s_cpu_limits", "k8s_mem_limits"}

// newLiveLoader is a loader that reads Prometheus, for the window [from, to].
func (s *Service) newLiveLoader(ctx context.Context, m *model.ForecastModule, from, to time.Time) *opLoader {
	run := &model.AlgorithmRun{ID: m.ID, Dashboard: true, Sources: m.Sources, Namespace: m.Namespace, Workload: m.Workload, Service: m.Service, Container: m.Container}
	return &opLoader{s: s, ctx: ctx, m: m, run: run, from: from, to: to, cache: map[string]*opSeries{}, live: &liveRead{repMaxAge: costReplicaMaxAge}}
}

// liveKinds are the kinds of the module's active sources, the ones the page needs first.
func (l *opLoader) liveKinds() []string {
	var kinds []string
	seen := map[string]bool{}
	add := func(kind string) {
		if !seen[kind] && l.run.FirstOfKind(kind) != nil {
			seen[kind] = true
			kinds = append(kinds, kind)
		}
	}
	for _, kind := range livePriority {
		add(kind)
	}
	for _, src := range l.run.ActiveSources() {
		add(src.Kind)
	}
	return kinds
}

// readLive asks Prometheus for every kind of the module, a few at a time and only as long as the time limit of the request allows. A kind that did not answer is
// listed with the reason (or as skipped) and the others are used: the answer is partial, never a 504.
func (l *opLoader) readLive() {
	kinds := l.liveKinds()
	l.live.queries = make([]LiveQuery, len(kinds))
	window := l.to.Sub(l.from)
	budget := l.s.readBudget(l.ctx)
	askCtx, cancel := l.ctx, context.CancelFunc(func() {})
	if budget > 0 {
		reserve := promReserve
		if reserve > budget/4 {
			reserve = budget / 4
		}
		askCtx, cancel = context.WithTimeout(l.ctx, budget-reserve)
	}
	defer cancel()

	type job struct {
		index int
		src   *model.MetricSource
		expr  string
		step  time.Duration
		ds    *model.Datasource
		level string
	}
	raw := make([][]collector.Point, len(kinds))
	var jobs []job
	var mu sync.Mutex
	for i, kind := range kinds {
		src := l.run.FirstOfKind(kind)
		q := &l.live.queries[i]
		*q = LiveQuery{Key: src.Key, Name: src.Name, Kind: kind, Unit: model.MetricKinds[kind]}
		setup, err := l.s.setup(l.ctx, l.run, src.Key)
		if err != nil {
			q.Error = err.Error()
			continue
		}
		q.Level, q.Unit, q.Group = setup.mapping.Level, setup.mapping.Unit, metricGroup(kind, setup.mapping.Level)
		q.Per = src.PerOf(setup.mapping.Level)
		for _, name := range model.TemplateVariables(setup.mapping.Query) {
			if name == "pod" {
				q.Error = fmt.Sprintf("mapping %q is pod level and cannot be read for a workload", setup.mapping.Name)
			}
		}
		if q.Error != "" {
			continue
		}
		step := subqueryStep(time.Duration(setup.mapping.StepSeconds)*time.Second, window)
		// {{range}} is the step of this query, so each point looks back over the stretch up to the one before it and no stretch is counted twice
		expr, err := collectorRender(setup.mapping, target(l.run), step)
		if err != nil {
			q.Error = err.Error()
			continue
		}
		q.Query, q.Step = strings.TrimSpace(expr), promDuration(step)
		jobs = append(jobs, job{index: i, src: src, expr: q.Query, step: step, ds: setup.ds, level: setup.mapping.Level})
		l.live.datasource = CostSource{ID: setup.ds.ID, Name: setup.ds.Name}
	}

	var wg sync.WaitGroup
	slots := make(chan struct{}, promParallel)
	queriers := map[uint]collector.RangeQuerier{}
	for _, j := range jobs {
		select {
		case slots <- struct{}{}:
		case <-askCtx.Done():
			l.live.queries[j.index].Skipped, l.live.queries[j.index].Error = true, promNotReached
			continue
		}
		mu.Lock()
		querier := queriers[j.ds.ID]
		if querier == nil {
			var err error
			if querier, err = l.s.querierOf(l.ctx, j.ds); err != nil {
				mu.Unlock()
				<-slots
				l.live.queries[j.index].Error = err.Error()
				continue
			}
			queriers[j.ds.ID] = querier
		}
		mu.Unlock()
		wg.Add(1)
		go func(j job, querier collector.RangeQuerier) {
			defer wg.Done()
			defer func() { <-slots }()
			q := &l.live.queries[j.index]
			// the first point is one step after the start: a window of a day at 5 minutes is 288 points, not 289
			points, err := collector.Fetch(askCtx, querier, j.expr, l.from.Add(j.step), l.to, j.step, collector.Options{Attempts: 1, Concurrency: 1})
			if err != nil {
				q.Error = err.Error()
				return
			}
			scale := j.src.Scale
			if scale <= 0 {
				scale = 1
			}
			kept := points[:0:0]
			for _, p := range points {
				if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
					continue
				}
				kept = append(kept, collector.Point{Time: p.Time, Value: p.Value * scale})
			}
			q.Points = len(kept)
			if len(kept) == 0 {
				q.Error = "Prometheus has no data for it in the window"
				return
			}
			raw[j.index] = kept
		}(j, querier)
	}
	wg.Wait()

	// a query the time limit stopped is told as such, not as a failure of its own
	timedOut := askCtx.Err() != nil
	for i := range l.live.queries {
		q := &l.live.queries[i]
		if timedOut && !q.Skipped && q.Points == 0 && q.Error != "" &&
			(strings.Contains(q.Error, "context deadline exceeded") || strings.Contains(q.Error, "context canceled")) {
			q.Skipped, q.Error = true, promNotReached
		}
	}

	// the replicas first: the series of the other kinds are divided or multiplied by them
	byKind := map[string]int{}
	for i, kind := range kinds {
		byKind[kind] = i
	}
	if i, ok := byKind["replicas"]; ok && len(raw[i]) > 0 {
		for _, p := range raw[i] {
			l.reps = append(l.reps, model.MetricSample{TS: p.Time.Unix(), Value: p.Value})
		}
		if age := 2 * l.stepOf(i); age > l.live.repMaxAge {
			l.live.repMaxAge = age
		}
	}
	for i, kind := range kinds {
		if len(raw[i]) == 0 {
			continue
		}
		points := make([]overprovision.Point, len(raw[i]))
		for k, p := range raw[i] {
			points[k] = overprovision.Point{TS: p.Time.Unix(), V: p.Value}
		}
		step, _ := time.ParseDuration(l.live.queries[i].Step)
		// what the query answers decides how the series is turned into one Pod and into the total
		level := "container"
		if l.live.queries[i].Per == model.PerTotal {
			level = "workload"
		}
		l.cache[kind] = l.build(points, level, int(step/time.Second))
	}
}

func (l *opLoader) stepOf(index int) time.Duration {
	d, _ := time.ParseDuration(l.live.queries[index].Step)
	return d
}

// collectorRender renders the mapping of a metric for a target with the {{range}} of the step the query is asked at.
func collectorRender(m *model.MetricMapping, t collector.Target, step time.Duration) (string, error) {
	return collector.Render(m.Query, t.Variables(step))
}

// liveReplicas is the replicas the module has at the end of the window (the newest value of the replicas metric); 0 when Prometheus gave none.
func (l *opLoader) liveReplicas() int {
	if len(l.reps) == 0 {
		return 0
	}
	last := l.reps[len(l.reps)-1]
	if v := last.Value; v >= 1 && !math.IsNaN(v) && l.to.Unix()-last.TS <= int64(l.live.repMaxAge/time.Second) {
		return int(math.Round(v))
	}
	return 0
}

// liveSummary is what the answer says of how the reading went.
func (l *opLoader) liveSummary() (queries []LiveQuery, partial bool, skipped int) {
	queries = append([]LiveQuery{}, l.live.queries...)
	for _, q := range queries {
		if q.Skipped {
			skipped++
		}
	}
	return queries, skipped > 0, skipped
}
