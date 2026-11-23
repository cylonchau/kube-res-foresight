package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The quantiles of a module's metrics asked of Prometheus itself (P10 A2, the dashboard): every figure is the answer of the data source of the module to
// quantile_over_time (and max_over_time) over a subquery of the PromQL the user wrote in the metric mapping of the source. Nothing in it is taken from the
// samples the platform stored, and the PromQL that was sent is returned with the figures, so any of them can be checked in Grafana.

// promSubqueryPoints is how many steps a subquery over the window is asked to evaluate at most: a window of 30 days at the step of a minute is 43200 points
// for each query, which a Prometheus answers slowly; the step is made coarser so that the window is at most this many steps.
const promSubqueryPoints = 1500

// PromQuantiles are the figures of one query set.
type PromQuantiles struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// PromMetric is one metric of the module with its quantiles from Prometheus. Total is the quantiles of the sum over the Pods (what a mapping of the
// workload level returns); Pod is the same query divided by the ready replicas at each moment (when the module has a replicas source), or the figure of a
// container when the mapping reads one. Error says why there is none.
type PromMetric struct {
	Key   string         `json:"key"`
	Name  string         `json:"name"`
	Kind  string         `json:"kind"`
	Group string         `json:"group"`
	Level string         `json:"level"`
	Unit  string         `json:"unit"`
	Total *PromQuantiles `json:"total,omitempty"`
	Pod   *PromQuantiles `json:"pod,omitempty"`
	// Query is the PromQL of the mapping as rendered for the module, Replicas the one of the replicas source; the quantile queries wrap them.
	Query    string `json:"query"`
	Replicas string `json:"replicas,omitempty"`
	// Example is the whole query of the P95 of the total, as it was sent.
	Example string `json:"example,omitempty"`
	Error   string `json:"error,omitempty"`
	// Skipped is true when the time limit of the request was over before the figures of this metric came (Error says so).
	Skipped bool `json:"skipped,omitempty"`
}

// PromStats is the answer: the metrics of the module over a window, as Prometheus computed them.
type PromStats struct {
	Datasource    CostSource   `json:"datasource"`
	At            time.Time    `json:"at"`
	WindowMinutes int          `json:"window_minutes"`
	Window        string       `json:"window"`
	Step          string       `json:"step"`
	Metrics       []PromMetric `json:"metrics"`
	// Partial is true when some metrics were not answered in the time the request had (Skipped counts them): the others are complete.
	Partial bool `json:"partial"`
	Skipped int  `json:"skipped"`
}

// gatedVector puts the process-wide gate in front of the instant queries as well.
type gatedVector struct {
	inner cost.Querier
	gate  *gate
}

func (q gatedVector) QueryVector(ctx context.Context, query string, at time.Time) ([]promclient.VectorSample, error) {
	select {
	case q.gate.slot <- struct{}{}:
		defer func() { <-q.gate.slot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return q.inner.QueryVector(ctx, query, at)
}

// vectorQuerierOf builds the instant querier of a data source with the gate in front of it.
func (s *Service) vectorQuerierOf(ctx context.Context, ds *model.Datasource) (cost.Querier, error) {
	factory := s.NewCostQuerier
	if factory == nil {
		factory = PromCostQuerier
	}
	inner, err := factory(ds)
	if err != nil {
		return nil, err
	}
	size := int(s.intParam(ctx, timing.PrometheusTotalConcurrency))
	return gatedVector{inner: inner, gate: gateFor(ds.ID, size)}, nil
}

// promDuration writes a duration in the whole seconds a PromQL range takes.
func promDuration(d time.Duration) string {
	secs := int64(d.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("%ds", secs)
}

// subqueryStep is the step of the subquery: the step of the mapping, made a whole multiple of itself coarse enough for the window to be at most
// promSubqueryPoints steps.
func subqueryStep(mapping time.Duration, window time.Duration) time.Duration {
	if mapping <= 0 {
		mapping = time.Minute
	}
	step := mapping
	if want := window / promSubqueryPoints; want > step {
		step = ((want + mapping - 1) / mapping) * mapping
	}
	return step
}

type promCall struct {
	name  string
	query string
}

// scalar runs an instant query that has to answer one series and returns its value.
func scalarOf(ctx context.Context, q cost.Querier, query string, at time.Time) (float64, error) {
	samples, err := q.QueryVector(ctx, query, at)
	if err != nil {
		return 0, err
	}
	var vals []float64
	for _, s := range samples {
		if !math.IsNaN(s.Value) && !math.IsInf(s.Value, 0) {
			vals = append(vals, s.Value)
		}
	}
	switch len(vals) {
	case 0:
		return 0, errors.New("Prometheus has no data for it in the window")
	case 1:
		return vals[0], nil
	}
	return 0, fmt.Errorf("the query answers %d series: the mapping has to sum them to one (sum(...))", len(vals))
}

// quantilesOf asks for the five figures of one expression over the window, the five queries at once.
func quantilesOf(ctx context.Context, q cost.Querier, expr string, window, step time.Duration, at time.Time, scale float64) (*PromQuantiles, string, error) {
	sub := fmt.Sprintf("(%s)[%s:%s]", expr, promDuration(window), promDuration(step))
	calls := []promCall{
		{"p50", "quantile_over_time(0.5, " + sub + ")"}, {"p90", "quantile_over_time(0.9, " + sub + ")"}, {"p95", "quantile_over_time(0.95, " + sub + ")"},
		{"p99", "quantile_over_time(0.99, " + sub + ")"}, {"max", "max_over_time(" + sub + ")"},
	}
	values := make([]float64, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c promCall) {
			defer wg.Done()
			values[i], errs[i] = scalarOf(ctx, q, c.query, at)
		}(i, c)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, calls[2].query, err
		}
	}
	if scale <= 0 {
		scale = 1
	}
	return &PromQuantiles{P50: values[0] * scale, P90: values[1] * scale, P95: values[2] * scale, P99: values[3] * scale, Max: values[4] * scale}, calls[2].query, nil
}

// promReserve is the time of the budget of a request that is kept for answering with what was got: the quantiles are asked until the budget less this.
const promReserve = 2 * time.Second

// promParallel is how many metrics of a module are asked at the same time (as many as the gate of the data source lets through by default), so that one slow
// metric does not keep the quick ones waiting behind it. The gate keeps the number of queries in flight of the whole platform in bounds.
const promParallel = 8

// The scopes of PrometheusStats: the CPU and the memory first (what the page needs at once), the other metrics after, or everything.
const (
	PromScopeAll  = ""
	PromScopeCore = "core"
	PromScopeMore = "more"
)

// readBudget is how long a request that reads may work: what is left of the deadline of its context, else the system parameter of the read time limit.
func (s *Service) readBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	// a request that asked to wait (X-Long-Request) has no deadline of its own: it is kept under the gateway all the same
	return timing.Under(s.gatewayTimeout(ctx), time.Duration(s.Count(ctx, timing.ListQuery))*time.Second)
}

// PrometheusStats asks the data source of a module for the P50, P90, P95, P99 and the maximum of its CPU, its memory and every other metric it has set up,
// over a window of the list or a custom range, as they are now (or at the end of the range). A metric whose query fails is listed with the reason; the
// others are still answered. The metrics are asked a few at a time and only as long as the time limit of the request allows: what was not reached is listed
// as such (Skipped) and the answer is marked Partial, so a slow data source or a long window gives the figures that were got and not a 504.
func (s *Service) PrometheusStats(ctx context.Context, moduleID uint, windowMinutes int, from, to time.Time) (*PromStats, error) {
	return s.PrometheusStatsScoped(ctx, moduleID, windowMinutes, from, to, PromScopeAll)
}

// PrometheusStatsScoped is PrometheusStats for the metrics of a scope: PromScopeCore (CPU and memory), PromScopeMore (the rest) or PromScopeAll.
func (s *Service) PrometheusStatsScoped(ctx context.Context, moduleID uint, windowMinutes int, from, to time.Time, scope string) (*PromStats, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	at := now
	window := time.Duration(windowMinutes) * time.Minute
	if !from.IsZero() || !to.IsZero() {
		at = to
		if at.IsZero() || at.After(now) {
			at = now
		}
		if from.IsZero() || !from.Before(at) {
			return nil, ErrOverprovisionWindow
		}
		window = at.Sub(from)
		if window > 30*24*time.Hour {
			window = 30 * 24 * time.Hour
		}
	} else {
		if windowMinutes == 0 {
			windowMinutes = s.OverprovisionBaseline(ctx)
			window = time.Duration(windowMinutes) * time.Minute
		}
		if _, ok := overprovision.WindowByMinutes(windowMinutes); !ok {
			return nil, ErrOverprovisionWindow
		}
	}
	run := &model.AlgorithmRun{ID: mod.ID, Dashboard: true, Sources: mod.Sources, Namespace: mod.Namespace, Workload: mod.Workload, Service: mod.Service, Container: mod.Container}
	core := []string{}
	for _, kind := range []string{"cpu", "memory"} {
		if src := run.FirstOfKind(kind); src != nil {
			core = append(core, src.Key)
		}
	}
	more := []string{}
	seen := map[string]bool{}
	for _, src := range run.ActiveSources() {
		if configKinds[src.Kind] || seen[src.Kind] {
			continue
		}
		seen[src.Kind] = true
		more = append(more, src.Key)
	}
	var keys []string
	switch scope {
	case PromScopeCore:
		keys = core
	case PromScopeMore:
		keys = more
	default:
		keys = append(append(keys, core...), more...)
	}
	out := &PromStats{At: at, WindowMinutes: int(window / time.Minute), Window: promDuration(window), Metrics: []PromMetric{}}
	if len(keys) == 0 {
		return out, nil
	}
	var replicasQuery string
	if rs := run.FirstOfKind("replicas"); rs != nil {
		if setup, err := s.setup(ctx, run, rs.Key); err == nil {
			replicasQuery, _ = collector.RenderMapping(setup.mapping, target(run))
		}
	}

	// what is asked of the data source stops at the budget less the reserve; the answer is made with what was got until then
	budget := s.readBudget(ctx)
	askCtx, cancel := ctx, context.CancelFunc(func() {})
	if budget > 0 {
		reserve := promReserve
		if reserve > budget/4 {
			reserve = budget / 4
		}
		askCtx, cancel = context.WithTimeout(ctx, budget-reserve)
	}
	defer cancel()

	type job struct {
		index     int
		expr      string
		step      time.Duration
		scale     float64
		container bool
		workload  bool
		replicas  bool
	}
	var jobs []job
	var querier cost.Querier
	out.Metrics = make([]PromMetric, len(keys))
	for i, key := range keys {
		src := run.Source(key)
		setup, err := s.setup(ctx, run, key)
		pm := &out.Metrics[i]
		*pm = PromMetric{Key: key, Name: src.Name, Kind: src.Kind, Unit: model.MetricKinds[src.Kind]}
		if err != nil {
			pm.Error = err.Error()
			continue
		}
		pm.Level, pm.Unit, pm.Group = setup.mapping.Level, setup.mapping.Unit, metricGroup(src.Kind, setup.mapping.Level)
		if querier == nil {
			if querier, err = s.vectorQuerierOf(ctx, setup.ds); err != nil {
				pm.Error = err.Error()
				continue
			}
			out.Datasource = CostSource{ID: setup.ds.ID, Name: setup.ds.Name}
		}
		expr, err := collector.RenderMapping(setup.mapping, target(run))
		if err != nil {
			pm.Error = err.Error()
			continue
		}
		expr = strings.TrimSpace(expr)
		step := subqueryStep(time.Duration(setup.mapping.StepSeconds)*time.Second, window)
		out.Step = promDuration(step)
		pm.Query = expr
		jobs = append(jobs, job{index: i, expr: expr, step: step, scale: src.Scale, container: setup.mapping.Level == "container",
			workload: setup.mapping.Level == "workload", replicas: src.Kind == "replicas"})
	}

	var wg sync.WaitGroup
	slots := make(chan struct{}, promParallel)
	for _, j := range jobs {
		// the metrics are started in their order (the CPU and the memory first) and a few at a time
		select {
		case slots <- struct{}{}:
		case <-askCtx.Done():
			out.Metrics[j.index].Skipped, out.Metrics[j.index].Error = true, promNotReached
			continue
		}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-slots }()
			pm := &out.Metrics[j.index]
			if j.container {
				// a container's figure is already one Pod's: it is the Pod figure
				q, example, err := quantilesOf(askCtx, querier, j.expr, window, j.step, at, j.scale)
				pm.Pod, pm.Example = q, example
				if err != nil {
					pm.Error = err.Error()
				}
				return
			}
			total, example, err := quantilesOf(askCtx, querier, j.expr, window, j.step, at, j.scale)
			pm.Total, pm.Example = total, example
			if err != nil {
				pm.Error = err.Error()
			} else if j.workload && replicasQuery != "" && !j.replicas {
				pm.Replicas = replicasQuery
				if pod, _, perr := quantilesOf(askCtx, querier, fmt.Sprintf("(%s) / scalar(%s)", j.expr, replicasQuery), window, j.step, at, j.scale); perr == nil {
					pm.Pod = pod
				}
			}
		}(j)
	}
	wg.Wait()

	// a metric that the time limit stopped is told as such, not as a failure of its query
	timedOut := askCtx.Err() != nil
	for i := range out.Metrics {
		pm := &out.Metrics[i]
		if timedOut && !pm.Skipped && pm.Error != "" && pm.Total == nil && pm.Pod == nil &&
			(strings.Contains(pm.Error, "context deadline exceeded") || strings.Contains(pm.Error, "context canceled")) {
			pm.Skipped, pm.Error = true, promNotReached
		}
		if pm.Skipped {
			out.Skipped++
		}
	}
	out.Partial = out.Skipped > 0
	return out, nil
}

// promNotReached is what a metric says when the time limit of the request was over before its figures came.
const promNotReached = "not answered within the time limit of the request: the figures that came are shown. Ask again for a shorter window, or raise http.list_query_seconds"
