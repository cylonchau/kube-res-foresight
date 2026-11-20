package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The over-provisioning analysis of the modules (P10 A2): over seven windows, what a Pod of each module requested against what it used, and what the
// use supports. The use is read from the samples the platform stored for the module (the source of kind cpu and memory, throttling, OOM kills and the
// JVM), the request from the module (or its k8s_*_requests source). Nothing is read from the cluster, no forecast takes part, and only the modules that
// are set up are analysed (P10 E2, E3). It changes no request, no replica and no recommendation.

// ErrOverprovisionWindow says the window asked for is not one of the windows, and no range was given.
var ErrOverprovisionWindow = errors.New("the window is not one of 15m, 30m, 1h, 1d, 7d, 15d, 30d")

const (
	// opMaxPoints bounds the points of a chart series.
	opMaxPoints = 300
	// opHistoryRows is how many stored results the detail of a module shows by default.
	opHistoryRows = 60
)

// OverprovisionParams reads the numbers of the analysis from the system parameters.
func (s *Service) OverprovisionParams(ctx context.Context) overprovision.Params {
	pc := func(p timing.Param) float64 { return float64(s.targetPercent(ctx, p)) / 100 }
	p := overprovision.Params{
		CPUMargin: pc(timing.OPCPUMargin), MemoryMargin: pc(timing.OPMemoryMargin), MemoryPeakFactor: pc(timing.OPMemoryPeak), OOMUplift: pc(timing.OPOOMUplift),
		ThrottleLimit: pc(timing.OPThrottle), Severe: pc(timing.OPGradeSevere), Moderate: pc(timing.OPGradeModerate), Mild: pc(timing.OPGradeMild),
		CoverageHigh: pc(timing.OPCoverageHigh), CoverageMid: pc(timing.OPCoverageMid), PeakNearLimit: 0.9,
	}
	p.BaselineMinutes = s.OverprovisionBaseline(ctx)
	// the limits must be in order, or a share would be graded twice: a limit above the one over it is brought down to it
	if p.Moderate > p.Severe {
		p.Moderate = p.Severe
	}
	if p.Mild > p.Moderate {
		p.Mild = p.Moderate
	}
	if p.CoverageMid > p.CoverageHigh {
		p.CoverageMid = p.CoverageHigh
	}
	return p
}

// OverprovisionBaseline is the window (in minutes) the dashboard shows: the system parameter in days, taken to the nearest window of the list.
func (s *Service) OverprovisionBaseline(ctx context.Context) int {
	days := s.targetPercent(ctx, timing.OPBaselineDays)
	best, diff := overprovision.Windows[4].Minutes, math.MaxInt
	for _, w := range overprovision.Windows {
		if w.Minutes < 24*60 {
			continue
		}
		if d := absInt(w.Minutes/(24*60) - days); d < diff {
			best, diff = w.Minutes, d
		}
	}
	return best
}

// opSeries is what was read of one metric of a module over the span that is analysed: per Pod and, where it can be made, over all the Pods.
type opSeries struct {
	pod, total []overprovision.Point
	step       int
	// raw is the stored values as they are (a count of kills, a working set), for the metrics that are not per Pod.
	raw []overprovision.Point
}

func sliceFrom(points []overprovision.Point, from, to int64) []overprovision.Point {
	i := sort.Search(len(points), func(i int) bool { return points[i].TS >= from })
	j := sort.Search(len(points), func(i int) bool { return points[i].TS > to })
	if i >= j {
		return nil
	}
	return points[i:j]
}

// opLoader reads the series of one module once; every window is cut from what it read.
type opLoader struct {
	s        *Service
	ctx      context.Context
	m        *model.ForecastModule
	run      *model.AlgorithmRun
	from, to time.Time
	reps     []model.MetricSample
	cache    map[string]*opSeries
}

func (s *Service) newOPLoader(ctx context.Context, m *model.ForecastModule, from, to time.Time) *opLoader {
	l := &opLoader{s: s, ctx: ctx, m: m, run: &model.AlgorithmRun{ID: m.ID, Sources: m.Sources}, from: from, to: to, cache: map[string]*opSeries{}}
	if src := l.run.FirstOfKind("replicas"); src != nil {
		if series, err := s.Samples.Series(ctx, m.ID, src.Key); err == nil {
			l.reps, _ = s.Samples.Range(ctx, series.ID, from.Add(-costReplicaMaxAge), to)
		}
	}
	return l
}

// repAt is the ready replicas at a moment (the newest sample not after it and not older than costReplicaMaxAge); false when there is none.
func (l *opLoader) repAt(ts int64) (float64, bool) {
	i := sort.Search(len(l.reps), func(i int) bool { return l.reps[i].TS > ts }) - 1
	if i < 0 || ts-l.reps[i].TS > int64(costReplicaMaxAge/time.Second) || l.reps[i].Value < 1 {
		return 0, false
	}
	return l.reps[i].Value, true
}

// get reads the source of a kind (nil when the module has none, or nothing is stored).
func (l *opLoader) get(kind string) *opSeries {
	if v, ok := l.cache[kind]; ok {
		return v
	}
	l.cache[kind] = nil
	src := l.run.FirstOfKind(kind)
	if src == nil {
		return nil
	}
	series, err := l.s.Samples.Series(l.ctx, l.m.ID, src.Key)
	if err != nil {
		return nil
	}
	rows, err := l.s.Samples.Range(l.ctx, series.ID, l.from, l.to)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := &opSeries{step: series.StepSeconds, raw: make([]overprovision.Point, 0, len(rows))}
	level := ""
	if mp, err := l.s.Mappings.GetByID(l.ctx, src.MappingID); err == nil {
		level = mp.Level
	}
	for _, r := range rows {
		if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			continue
		}
		out.raw = append(out.raw, overprovision.Point{TS: r.TS, V: r.Value})
	}
	// a workload-total series is divided by the ready replicas of the same moment, a series that is already per Pod is taken as it is
	for _, p := range out.raw {
		rep, known := l.repAt(p.TS)
		switch {
		case level == "workload":
			out.total = append(out.total, p)
			if known {
				out.pod = append(out.pod, overprovision.Point{TS: p.TS, V: p.V / rep})
			}
		default:
			out.pod = append(out.pod, p)
			if known {
				out.total = append(out.total, overprovision.Point{TS: p.TS, V: p.V * rep})
			}
		}
	}
	l.cache[kind] = out
	return out
}

func lastValue(points []overprovision.Point) float64 {
	if len(points) == 0 {
		return 0
	}
	return points[len(points)-1].V
}

func meanOf(points []overprovision.Point) float64 {
	if len(points) == 0 {
		return 0
	}
	sum := 0.0
	for _, p := range points {
		sum += p.V
	}
	return sum / float64(len(points))
}

func maxOf(points []overprovision.Point) float64 {
	m := 0.0
	for _, p := range points {
		m = math.Max(m, p.V)
	}
	return m
}

func sumPositive(points []overprovision.Point) float64 {
	t := 0.0
	for _, p := range points {
		if p.V > 0 {
			t += p.V
		}
	}
	return t
}

// JVMInfo is what the JVM metrics of a module say over a window, beside the working set of the container: they are shown, never taken for the
// container's use, and the hints are only hints.
type JVMInfo struct {
	Expected bool `json:"expected"`
	Present  bool `json:"present"`
	// HeapUsed, HeapMax and NonHeapUsed are the quantiles over the window (bytes, per Pod); nil when the module has no such metric.
	HeapUsed    *overprovision.Usage `json:"heap_used,omitempty"`
	HeapMax     *overprovision.Usage `json:"heap_max,omitempty"`
	NonHeapUsed *overprovision.Usage `json:"nonheap_used,omitempty"`
	// The committed figures are what the JVM asked the system for, not what it uses: they are shown next to the used and maximum, and are never taken for the
	// memory of the container. GCCount and GCPause are the collections and the time in them over each interval, when the module has the metric.
	HeapCommitted    *overprovision.Usage `json:"heap_committed,omitempty"`
	NonHeapCommitted *overprovision.Usage `json:"nonheap_committed,omitempty"`
	NonHeapMax       *overprovision.Usage `json:"nonheap_max,omitempty"`
	GCCount          *overprovision.Usage `json:"gc_count,omitempty"`
	GCPause          *overprovision.Usage `json:"gc_pause,omitempty"`
	WorkingSetP99    float64              `json:"working_set_p99"`
	Hints            []string             `json:"hints"`
}

// ResourceChart is the series of a chart: the use of the Pod in the window with its moving averages, and the request. The arrays are aligned.
type ResourceChart struct {
	Resource string    `json:"resource"`
	StepSecs int       `json:"step_seconds"`
	TS       []int64   `json:"ts"`
	Value    []float64 `json:"value"`
	// MA5, MA15 and MA25 are the moving averages of the last 5, 15 and 25 points of the step (the labels say it: MA5@1m is 5 minutes).
	MA5     []float64 `json:"ma5"`
	MA15    []float64 `json:"ma15"`
	MA25    []float64 `json:"ma25"`
	Labels  []string  `json:"ma_labels"`
	Request float64   `json:"request"`
	Suggest float64   `json:"suggested"`
	// TotalTS and Total are the use of all the replicas together (the sum over the Pods) over the window, in the step of the chart; empty when the module's
	// metric has no sum over the Pods. Replicas is the number of replicas the module has now, which the request and the suggestion of one Pod are
	// multiplied by to be drawn against the total.
	TotalTS  []int64   `json:"total_ts"`
	Total    []float64 `json:"total"`
	Replicas int       `json:"replicas"`
}

// ModuleOverprovision is the analysis of one module over the windows asked for.
type ModuleOverprovision struct {
	ModuleID     uint      `json:"module_id"`
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	WorkloadKind string    `json:"workload_kind"`
	Workload     string    `json:"workload"`
	Replicas     int       `json:"replicas"`
	At           time.Time `json:"at"`
	// Windows are the results, shortest window first; each has a CPU and a memory result (nil when the module has no such use series).
	Windows []*WindowOverprovision `json:"windows"`
	JVM     *JVMInfo               `json:"jvm,omitempty"`
	Charts  []*ResourceChart       `json:"charts,omitempty"`
	// Metrics are the other metrics the module has set up (container, JVM, throttling, OOM ...), over the window.
	Metrics []MetricStat `json:"metrics"`
}

// WindowOverprovision is the result of one window.
type WindowOverprovision struct {
	Label         string                `json:"label"`
	WindowMinutes int                   `json:"window_minutes"`
	CPU           *overprovision.Result `json:"cpu"`
	Memory        *overprovision.Result `json:"memory"`
}

func (s *Service) opRounders() (cpu, mem func(float64) float64) {
	cpu = func(v float64) float64 { return float64(RoundCPUMilli(int64(math.Ceil(v*1000)))) / 1000 }
	mem = func(v float64) float64 { return float64(RoundMemoryBytes(int64(math.Ceil(v)))) }
	return
}

// analyzeWindow analyses both resources of the module over the window that ends at "to".
func (l *opLoader) analyzeWindow(label string, minutes int, p overprovision.Params, replicas int) *WindowOverprovision {
	from := l.to.Add(-time.Duration(minutes) * time.Minute).Unix()
	to := l.to.Unix()
	cpuRound, memRound := l.s.opRounders()
	jvmExpected := false
	for _, src := range l.run.ActiveSources() {
		if strings.HasPrefix(src.Kind, "jvm_") {
			jvmExpected = true
		}
	}
	jvmPresent := false
	if u := l.get("jvm_heap_used"); u != nil && len(sliceFrom(u.pod, from, to)) > 0 {
		jvmPresent = true
	}
	oomKills, oomWS := 0.0, 0.0
	if u := l.get("oom"); u != nil {
		oomKills = sumPositive(sliceFrom(u.raw, from, to))
	}
	if oomKills > 0 {
		if u := l.get("oom_working_set"); u != nil {
			oomWS = maxOf(sliceFrom(u.raw, from, to))
		}
	}

	one := func(resource, useKind, requestKind, limitKind string, requestFixed, limitRatio float64, round func(float64) float64) *overprovision.Result {
		use := l.get(useKind)
		if use == nil {
			return nil
		}
		in := overprovision.Input{Resource: resource, Points: sliceFrom(use.pod, from, to), Total: sliceFrom(use.total, from, to), StepSeconds: use.step,
			WindowMinutes: minutes, Throttle: -1, JVMExpected: jvmExpected, JVMPresent: jvmPresent, Round: round}
		in.Request = requestFixed
		if !(in.Request > 0) {
			if rs := l.get(requestKind); rs != nil {
				in.Request = lastValue(sliceFrom(rs.pod, from, to))
			}
		}
		if ls := l.get(limitKind); ls != nil {
			if v := lastValue(sliceFrom(ls.pod, from, to)); v > 0 {
				in.Limit, in.LimitFrom = v, "metric"
			}
		}
		if in.Limit <= 0 && limitRatio >= 1 && in.Request > 0 {
			// admission gives a new Pod the request times this ratio as its limit: it is what the module is configured to, not what was measured
			in.Limit, in.LimitFrom = in.Request*limitRatio, "module"
		}
		if resource == overprovision.CPU {
			if ts := l.get("k8s_cpu_throttle"); ts != nil {
				if pts := sliceFrom(ts.pod, from, to); len(pts) > 0 {
					in.Throttle = meanOf(pts)
				}
			}
		} else {
			in.OOMKills, in.OOMWorkingSet = oomKills, oomWS
		}
		r := overprovision.Analyze(in, p)
		return &r
	}
	return &WindowOverprovision{Label: label, WindowMinutes: minutes,
		CPU:    one(overprovision.CPU, "cpu", "k8s_cpu_requests", "k8s_cpu_limits", float64(l.m.PodCPURequestMilli)/1000, l.m.LimitCPURatio, cpuRound),
		Memory: one(overprovision.Memory, "memory", "k8s_mem_requests", "k8s_mem_limits", float64(l.m.PodMemoryRequestBytes), l.m.LimitMemoryRatio, memRound)}
}

func (s *Service) moduleHeader(m *model.ForecastModule, replicas int, at time.Time) *ModuleOverprovision {
	return &ModuleOverprovision{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload, Replicas: replicas, At: at}
}

// AnalyzeOverprovision analyses a module over every window and stores the results (one row for each window and resource that has a result). It is what the
// scheduled task runs and what the dashboard's "analyse now" asks for.
func (s *Service) AnalyzeOverprovision(ctx context.Context, m *model.ForecastModule) (*ModuleOverprovision, error) {
	now := s.now()
	last := overprovision.Windows[len(overprovision.Windows)-1]
	p := s.OverprovisionParams(ctx)
	l := s.newOPLoader(ctx, m, now.Add(-time.Duration(last.Minutes)*time.Minute), now)
	replicas := s.costReplicas(ctx, m, l.run, nil, now)
	out := s.moduleHeader(m, replicas, now)
	var rows []*model.OverprovisionAnalysis
	for _, w := range overprovision.Windows {
		wr := l.analyzeWindow(w.Label, w.Minutes, p, replicas)
		out.Windows = append(out.Windows, wr)
		for _, r := range []*overprovision.Result{wr.CPU, wr.Memory} {
			if r != nil {
				rows = append(rows, resultRow(m.ID, replicas, now, r))
			}
		}
	}
	if err := s.Overprovision.CreateBatch(ctx, rows); err != nil {
		return nil, err
	}
	return out, nil
}

func resultRow(moduleID uint, replicas int, at time.Time, r *overprovision.Result) *model.OverprovisionAnalysis {
	row := &model.OverprovisionAnalysis{CreatedAt: at, ModuleID: moduleID, Resource: r.Resource, WindowMinutes: r.WindowMinutes, Replicas: replicas, Request: r.Request,
		Limit: r.Limit, LimitFrom: r.LimitFrom, Points: r.Usage.Points, ExpectedPoints: r.ExpectedPoints, Coverage: r.Coverage, P50: r.Usage.P50, P90: r.Usage.P90, P95: r.Usage.P95, P99: r.Usage.P99, Max: r.Usage.Max,
		Suggested: r.Suggested, Ratio: r.Ratio, Numerator: r.Numerator, Denominator: r.Denominator, Reclaimable: r.Reclaimable, Status: r.Status, Grade: r.Grade,
		Confidence: r.Confidence, Risk: r.Risk, Throttle: r.Throttle, OOMKills: r.OOMKills, OOMWorkingSet: r.OOMWorkingSet,
		Reasons: overprovision.JoinReasons(r.Reasons), Formula: clip(r.Formula, 600), FormulaVersion: r.FormulaVersion}
	if r.Total != nil {
		row.TotalP50, row.TotalP90, row.TotalP95, row.TotalP99, row.TotalMax = r.Total.P50, r.Total.P90, r.Total.P95, r.Total.P99, r.Total.Max
	}
	return row
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// RunOverprovision analyses a module (every enabled module when moduleID is 0), stores the results, and removes the ones older than the retention.
// It returns how many modules were analysed; a module that fails does not stop the others, and the failures are joined.
func (s *Service) RunOverprovision(ctx context.Context, moduleID uint) (int, error) {
	var modules []*model.ForecastModule
	if moduleID != 0 {
		m, err := s.Modules.Get(ctx, moduleID)
		if err != nil {
			return 0, err
		}
		modules = []*model.ForecastModule{m}
	} else {
		var err error
		if modules, err = s.Modules.ListEnabled(ctx); err != nil {
			return 0, err
		}
	}
	done := 0
	var failed []string
	for _, m := range modules {
		if _, err := s.AnalyzeOverprovision(ctx, m); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		}
		done++
	}
	days := s.targetPercent(ctx, timing.OPRetentionDays)
	if _, err := s.Overprovision.PruneBefore(ctx, s.now().AddDate(0, 0, -days), 5000); err != nil {
		failed = append(failed, "pruning: "+err.Error())
	}
	if len(failed) > 0 {
		return done, errors.New(strings.Join(failed, "; "))
	}
	return done, nil
}

// OverprovisionDetail analyses one module live, over a window of the list or over a custom range (from and to; both zero for a window), with the
// series of the chart, the JVM metrics and the stored history. A custom range is never stored.
func (s *Service) OverprovisionDetail(ctx context.Context, moduleID uint, windowMinutes int, from, to time.Time) (*DetailOverprovision, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	label := ""
	custom := !from.IsZero() || !to.IsZero()
	if custom {
		if to.IsZero() || to.After(now) {
			to = now
		}
		if from.IsZero() || !from.Before(to) {
			return nil, ErrOverprovisionWindow
		}
		if to.Sub(from) > 30*24*time.Hour {
			from = to.Add(-30 * 24 * time.Hour)
		}
		windowMinutes = int(math.Ceil(to.Sub(from).Minutes()))
		label = "custom"
	} else {
		if windowMinutes == 0 {
			windowMinutes = s.OverprovisionBaseline(ctx)
		}
		w, ok := overprovision.WindowByMinutes(windowMinutes)
		if !ok {
			return nil, ErrOverprovisionWindow
		}
		label, to = w.Label, now
	}
	p := s.OverprovisionParams(ctx)
	l := s.newOPLoader(ctx, m, to.Add(-time.Duration(windowMinutes)*time.Minute), to)
	replicas := s.costReplicas(ctx, m, l.run, nil, to)
	mo := s.moduleHeader(m, replicas, to)
	wr := l.analyzeWindow(label, windowMinutes, p, replicas)
	mo.Windows = []*WindowOverprovision{wr}

	fromTS, toTS := to.Add(-time.Duration(windowMinutes)*time.Minute).Unix(), to.Unix()
	for _, c := range []struct {
		kind string
		res  *overprovision.Result
	}{{"cpu", wr.CPU}, {"memory", wr.Memory}} {
		if c.res == nil {
			continue
		}
		use := l.get(c.kind)
		if use == nil {
			continue
		}
		chart := opChart(c.kind, sliceFrom(use.pod, fromTS, toTS), use.step, windowMinutes, c.res)
		opTotal(chart, sliceFrom(use.total, fromTS, toTS), use.step, windowMinutes, replicas)
		mo.Charts = append(mo.Charts, chart)
	}
	mo.JVM = l.jvm(fromTS, toTS, wr)
	mo.Metrics = l.metrics(fromTS, toTS)

	hist, err := s.Overprovision.History(ctx, moduleID, "", windowMinutes, opHistoryRows*2)
	if err != nil {
		return nil, err
	}
	views := make([]*OverprovisionResult, 0, len(hist))
	for _, h := range hist {
		views = append(views, viewOf(h))
	}
	return &DetailOverprovision{ModuleOverprovision: mo, Params: p, History: views}, nil
}

// DetailOverprovision is the answer of the detail: the analysis, the numbers it was made with, and the stored results of the same window.
type DetailOverprovision struct {
	*ModuleOverprovision
	Params  overprovision.Params   `json:"params"`
	History []*OverprovisionResult `json:"history"`
}

// opChart downsamples the use of a Pod to at most opMaxPoints and adds the moving averages in the step of the window.
func opChart(kind string, pts []overprovision.Point, stored, windowMinutes int, res *overprovision.Result) *ResourceChart {
	// the moving averages are made in the step of the window (MA5@5m is five points of five minutes) and only then thinned for the chart: a
	// thinned series would have a step of its own, and the label would no longer say what the average is
	step := overprovision.MAStepFor(windowMinutes, stored)
	rs := overprovision.Resample(pts, step)
	series := [][]float64{pointValues(rs), pointValues(overprovision.MovingAverage(rs, 5)), pointValues(overprovision.MovingAverage(rs, 15)), pointValues(overprovision.MovingAverage(rs, 25))}
	stride := (len(rs) + opMaxPoints - 1) / opMaxPoints
	if stride < 1 {
		stride = 1
	}
	c := &ResourceChart{Resource: kind, StepSecs: step, Request: res.Request, Suggest: res.Suggested,
		Labels: []string{overprovision.MALabel(5, step), overprovision.MALabel(15, step), overprovision.MALabel(25, step)}}
	var thinned [4][]float64
	for i := 0; i < len(rs); i += stride {
		c.TS = append(c.TS, rs[i].TS)
		for k := range series {
			thinned[k] = append(thinned[k], series[k][i])
		}
	}
	c.Value, c.MA5, c.MA15, c.MA25 = thinned[0], thinned[1], thinned[2], thinned[3]
	return c
}

// opTotal adds the use of all the replicas together to the chart, thinned the same way as the series of one Pod.
func opTotal(c *ResourceChart, pts []overprovision.Point, stored, windowMinutes, replicas int) {
	c.TotalTS, c.Total, c.Replicas = []int64{}, []float64{}, replicas
	if len(pts) == 0 {
		return
	}
	rs := overprovision.Resample(pts, overprovision.MAStepFor(windowMinutes, stored))
	stride := (len(rs) + opMaxPoints - 1) / opMaxPoints
	if stride < 1 {
		stride = 1
	}
	for i := 0; i < len(rs); i += stride {
		c.TotalTS = append(c.TotalTS, rs[i].TS)
		c.Total = append(c.Total, round6(rs[i].V))
	}
}

func pointValues(p []overprovision.Point) []float64 {
	out := make([]float64, len(p))
	for i := range p {
		out[i] = round6(p[i].V)
	}
	return out
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// jvm reads the JVM metrics of the window; nil when the module has none set up.
func (l *opLoader) jvm(from, to int64, wr *WindowOverprovision) *JVMInfo {
	expected := false
	for _, src := range l.run.ActiveSources() {
		if strings.HasPrefix(src.Kind, "jvm_") {
			expected = true
		}
	}
	if !expected {
		return nil
	}
	info := &JVMInfo{Expected: true, Hints: []string{}}
	usage := func(kind string) *overprovision.Usage {
		s := l.get(kind)
		if s == nil {
			return nil
		}
		pts := sliceFrom(s.pod, from, to)
		if len(pts) == 0 {
			return nil
		}
		u := overprovision.UsageOf(pts)
		return &u
	}
	info.HeapUsed, info.HeapMax, info.NonHeapUsed = usage("jvm_heap_used"), usage("jvm_heap_max"), usage("jvm_nonheap_used")
	info.HeapCommitted, info.NonHeapCommitted, info.NonHeapMax = usage("jvm_heap_committed"), usage("jvm_nonheap_committed"), usage("jvm_nonheap_max")
	info.GCCount, info.GCPause = usage("jvm_gc_count"), usage("jvm_gc_pause")
	info.Present = info.HeapUsed != nil
	if wr.Memory != nil {
		info.WorkingSetP99 = wr.Memory.Usage.P99
	}
	// only hints: the heap is not the container, and the maximum is not what is used
	if info.HeapMax != nil && info.WorkingSetP99 > 0 && info.HeapMax.Max > 1.5*info.WorkingSetP99 {
		info.Hints = append(info.Hints, "heap_max_above_working_set")
	}
	if info.HeapUsed != nil && info.HeapMax != nil && info.HeapMax.Max > 0 && info.HeapUsed.P99 >= 0.9*info.HeapMax.Max {
		info.Hints = append(info.Hints, "heap_near_max")
	}
	if info.HeapCommitted != nil {
		// said whenever it is shown: the committed heap is a request to the system, not a measure of use
		info.Hints = append(info.Hints, "committed_is_not_use")
		if info.HeapUsed != nil && info.HeapCommitted.P99 > 0 && info.HeapUsed.P99 >= 0.9*info.HeapCommitted.P99 {
			info.Hints = append(info.Hints, "heap_near_committed")
		}
	}
	if info.Present && info.GCCount == nil && info.GCPause == nil {
		info.Hints = append(info.Hints, "gc_missing") // the analysis goes on with used, committed and maximum
	}
	if !info.Present {
		info.Hints = append(info.Hints, "jvm_missing")
	}
	return info
}

// OverprovisionRow is one module on the dashboard: the newest stored result of each resource over the window.
type OverprovisionRow struct {
	ModuleID     uint   `json:"module_id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	WorkloadKind string `json:"workload_kind"`
	Workload     string `json:"workload"`
	Replicas     int    `json:"replicas"`
	// Health is the worst of the two resources: risk, under, over, limited, ok or unknown (see healthOf).
	Health     string               `json:"health"`
	AnalyzedAt time.Time            `json:"analyzed_at"`
	CPU        *OverprovisionResult `json:"cpu"`
	Memory     *OverprovisionResult `json:"memory"`
}

// OverprovisionResult is a stored result with its reasons as a list.
type OverprovisionResult struct {
	*model.OverprovisionAnalysis
	Reasons []string `json:"reasons"`
}

func viewOf(r *model.OverprovisionAnalysis) *OverprovisionResult {
	reasons := []string{}
	if r.Reasons != "" {
		reasons = strings.Split(r.Reasons, ",")
	}
	return &OverprovisionResult{OverprovisionAnalysis: r, Reasons: reasons}
}

// Health values, worst first.
const (
	HealthRisk    = "risk"    // the memory was OOM-killed, or the use is near the limit, or the CPU is throttled
	HealthUnder   = "under"   // a request below the P95 use
	HealthOver    = "over"    // a request that is more than the use needs
	HealthLimited = "limited" // see the status limited
	HealthOK      = "ok"
	HealthUnknown = "unknown"
)

func healthOf(rs ...*OverprovisionResult) string {
	rank := map[string]int{HealthRisk: 5, HealthUnder: 4, HealthOver: 3, HealthLimited: 2, HealthOK: 1, HealthUnknown: 0}
	best := HealthUnknown
	for _, r := range rs {
		if r == nil {
			continue
		}
		h := HealthUnknown
		switch {
		case r.Status == overprovision.StatusUnknown:
		case r.Risk == overprovision.High:
			h = HealthRisk
		case r.Status == overprovision.StatusUnder:
			h = HealthUnder
		case r.Status == overprovision.StatusOver:
			h = HealthOver
		case r.Status == overprovision.StatusLimited:
			h = HealthLimited
		default:
			h = HealthOK
		}
		if rank[h] > rank[best] {
			best = h
		}
	}
	return best
}

// ResourceTotals are the sums of a resource over the workloads of the dashboard (CPU in cores, memory in bytes), over all the Pods.
type ResourceTotals struct {
	// Request is what the workloads reserve, Reclaimable what the use does not need of it, After what would be reserved with the suggestions.
	Request     float64 `json:"request"`
	Reclaimable float64 `json:"reclaimable"`
	After       float64 `json:"after"`
	// Share is Reclaimable / Request (0 when nothing is reserved).
	Share float64 `json:"share"`
}

// OverprovisionBoard is the dashboard: the totals, the counts and the workloads, from the newest stored results over one window.
type OverprovisionBoard struct {
	WindowMinutes int    `json:"window_minutes"`
	Window        string `json:"window"`
	// AnalyzedAt is when the newest result was made; nil when nothing was analysed yet.
	AnalyzedAt *time.Time     `json:"analyzed_at"`
	Modules    int            `json:"modules"`
	Analyzed   int            `json:"analyzed"`
	CPU        ResourceTotals `json:"cpu"`
	Memory     ResourceTotals `json:"memory"`
	// Over, Under, Risk and Limited count the workloads by health.
	Over    int                 `json:"over"`
	Under   int                 `json:"under"`
	Risk    int                 `json:"risk"`
	Limited int                 `json:"limited"`
	Basis   string              `json:"basis"`
	Rows    []*OverprovisionRow `json:"rows"`
}

// OverprovisionBoard builds the dashboard from the stored results of a window (the baseline window when 0).
func (s *Service) OverprovisionBoard(ctx context.Context, windowMinutes int) (*OverprovisionBoard, error) {
	if windowMinutes == 0 {
		windowMinutes = s.OverprovisionBaseline(ctx)
	}
	w, ok := overprovision.WindowByMinutes(windowMinutes)
	if !ok {
		return nil, ErrOverprovisionWindow
	}
	modules, err := s.Modules.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := s.Overprovision.Latest(ctx, windowMinutes)
	if err != nil {
		return nil, err
	}
	byModule := map[uint]*OverprovisionRow{}
	for _, m := range modules {
		byModule[m.ID] = &OverprovisionRow{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload, Health: HealthUnknown}
	}
	out := &OverprovisionBoard{WindowMinutes: windowMinutes, Window: w.Label, Modules: len(modules), Rows: make([]*OverprovisionRow, 0, len(modules)),
		Basis: "the quantiles of the use the platform stored for each module over the window, against the Pod request recorded on the module; the suggestion is a formula of the measured use, no forecast takes part, nothing was read from the cluster, only modules that are set up are analysed, and no money is counted"}
	for _, r := range stored {
		row := byModule[r.ModuleID]
		if row == nil {
			continue // the module is gone or disabled
		}
		v := viewOf(r)
		if r.Resource == overprovision.CPU {
			row.CPU = v
		} else {
			row.Memory = v
		}
		row.Replicas = r.Replicas
		if r.CreatedAt.After(row.AnalyzedAt) {
			row.AnalyzedAt = r.CreatedAt
		}
		if out.AnalyzedAt == nil || r.CreatedAt.After(*out.AnalyzedAt) {
			t := r.CreatedAt
			out.AnalyzedAt = &t
		}
	}
	add := func(t *ResourceTotals, r *OverprovisionResult, replicas int) {
		if r == nil || !(r.Request > 0) || r.Status == overprovision.StatusUnknown {
			return
		}
		n := float64(max(replicas, 1))
		t.Request += r.Request * n
		t.Reclaimable += r.Reclaimable * n
	}
	for _, m := range modules {
		row := byModule[m.ID]
		row.Health = healthOf(row.CPU, row.Memory)
		if row.CPU != nil || row.Memory != nil {
			out.Analyzed++
		}
		add(&out.CPU, row.CPU, row.Replicas)
		add(&out.Memory, row.Memory, row.Replicas)
		switch row.Health {
		case HealthOver:
			out.Over++
		case HealthUnder:
			out.Under++
		case HealthRisk:
			out.Risk++
		case HealthLimited:
			out.Limited++
		}
		out.Rows = append(out.Rows, row)
	}
	for _, t := range []*ResourceTotals{&out.CPU, &out.Memory} {
		t.After = t.Request - t.Reclaimable
		if t.Request > 0 {
			t.Share = t.Reclaimable / t.Request
		}
	}
	// the most to give back first (by the share of the request, so a small workload that is mostly idle is not buried under a large one)
	reclaim := func(r *OverprovisionRow) float64 {
		best := 0.0
		for _, v := range []*OverprovisionResult{r.CPU, r.Memory} {
			if v != nil && v.Status == overprovision.StatusOver && v.Ratio > best {
				best = v.Ratio
			}
		}
		return best
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return reclaim(out.Rows[i]) > reclaim(out.Rows[j]) })
	return out, nil
}

// MetricStat is one of the other metrics a module has set up, over the window: what the page shows besides the CPU and the memory, and only what is
// configured (a module without JVM metrics has no JVM tile). Group is container (the mapping reads one container), jvm, throttle, oom or other.
type MetricStat struct {
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Group  string  `json:"group"`
	Level  string  `json:"level"`
	Unit   string  `json:"unit"`
	Points int     `json:"points"`
	Latest float64 `json:"latest"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

// configKinds are the kinds the page shows as configuration or as the main figures, not as tiles of their own.
var configKinds = map[string]bool{"cpu": true, "memory": true, "replicas": true, "k8s_cpu_requests": true, "k8s_cpu_limits": true, "k8s_mem_requests": true, "k8s_mem_limits": true}

func metricGroup(kind, level string) string {
	switch {
	case level == "container":
		return "container"
	case strings.HasPrefix(kind, "jvm_"):
		return "jvm"
	case kind == "k8s_cpu_throttle":
		return "throttle"
	case kind == "oom" || kind == "oom_working_set":
		return "oom"
	}
	return "other"
}

// metrics reads the other enabled sources of the module over the window. A source with nothing stored in it is still listed (with no points), so the
// page can say it is configured and empty.
func (l *opLoader) metrics(from, to int64) []MetricStat {
	out := []MetricStat{}
	seen := map[string]bool{}
	for _, src := range l.run.ActiveSources() {
		if configKinds[src.Kind] || seen[src.Kind] {
			continue
		}
		seen[src.Kind] = true
		ms := MetricStat{Key: src.Key, Name: src.Name, Kind: src.Kind, Unit: model.MetricKinds[src.Kind]}
		level := ""
		if mp, err := l.s.Mappings.GetByID(l.ctx, src.MappingID); err == nil {
			level, ms.Unit = mp.Level, mp.Unit
		}
		ms.Level, ms.Group = level, metricGroup(src.Kind, level)
		if s := l.get(src.Kind); s != nil {
			pts := sliceFrom(s.pod, from, to)
			if len(pts) == 0 {
				pts = sliceFrom(s.raw, from, to)
			}
			u := overprovision.UsageOf(pts)
			ms.Points, ms.P50, ms.P90, ms.P95, ms.P99, ms.Max = u.Points, u.P50, u.P90, u.P95, u.P99, u.Max
			ms.Latest = lastValue(pts)
		}
		out = append(out, ms)
	}
	return out
}
