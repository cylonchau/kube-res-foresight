package forecastsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
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
	band := func(target, low, high timing.Param) overprovision.Band {
		return overprovision.Band{Target: pc(target), Low: pc(low), High: pc(high)}
	}
	p := overprovision.Params{
		P90: band(timing.OPP90Target, timing.OPP90Low, timing.OPP90High), P95: band(timing.OPP95Target, timing.OPP95Low, timing.OPP95High), P99: band(timing.OPP99Target, timing.OPP99Low, timing.OPP99High),
		LimitFactor: pc(timing.OPLimitFactor), OOMUplift: pc(timing.OPOOMUplift), ThrottleLimit: pc(timing.OPThrottle), Severe: pc(timing.OPGradeSevere), Moderate: pc(timing.OPGradeModerate),
		CoverageHigh: pc(timing.OPCoverageHigh), CoverageMid: pc(timing.OPCoverageMid), PeakNearLimit: pc(timing.OPLimitGuard), SustainMinutes: s.targetPercent(ctx, timing.OPSustain),
	}
	p.BaselineMinutes = s.OverprovisionBaseline(ctx)
	// the limits must be in order, or a share would be graded twice: a limit above the one over it is brought down to it
	if p.Moderate > p.Severe {
		p.Moderate = p.Severe
	}
	// a band whose range is upside down would call everything under- and over-provisioned: the low is brought down to the high
	for _, b := range []*overprovision.Band{&p.P90, &p.P95, &p.P99} {
		if b.Low > b.High {
			b.Low = b.High
		}
	}
	if p.CoverageMid > p.CoverageHigh {
		p.CoverageMid = p.CoverageHigh
	}
	return p
}

// moduleParams are the numbers of the analysis for one module: the system settings, with the coefficients the module sets in module management over them (the target
// share of each percentile, the margin of the recommended Limits, the OOM protection coefficient). A coefficient the module leaves at 0 follows the system.
func moduleParams(p overprovision.Params, m *model.ForecastModule) overprovision.Params {
	if m.RecP90Target > 0 {
		p.P90.Target = m.RecP90Target
	}
	if m.RecP95Target > 0 {
		p.P95.Target = m.RecP95Target
	}
	if m.RecP99Target > 0 {
		p.P99.Target = m.RecP99Target
	}
	if m.RecSustainMinutes > 0 {
		p.SustainMinutes = m.RecSustainMinutes
	}
	if m.RecLimitGuard > 0 {
		p.PeakNearLimit = m.RecLimitGuard
	}
	// each resource has its own margins: the Requests margin is the module's (none by default), the Limits margin follows the system setting where the module sets none
	p.CPURequestHeadroom, p.MemoryRequestHeadroom = m.RecCPURequestHeadroom, m.RecMemoryRequestHeadroom
	p.CPULimitFactor, p.MemoryLimitFactor = m.RecCPULimitFactor, m.RecMemoryLimitFactor
	if m.OOMMemoryUplift >= 1 {
		p.OOMUplift = m.OOMMemoryUplift
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

// recommendKinds are the kinds the verdict and the recommendation are made of: the use of the CPU and of the memory, the replicas they are divided by, the Requests and the
// Limits, the throttling and the OOM kills, and the heap of the JVM (its presence is a condition of the confidence).
var recommendKinds = map[string]bool{
	"cpu": true, "memory": true, "replicas": true, "k8s_cpu_requests": true, "k8s_cpu_limits": true, "k8s_mem_requests": true, "k8s_mem_limits": true,
	"k8s_cpu_throttle": true, "oom": true, "oom_working_set": true, "jvm_heap_used": true,
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
	// byKey is the series of each source of a live reading, the first of a kind and the others alike.
	byKey map[string]*opSeries
	// live is set when the loader reads Prometheus (see oplive.go) and not the samples the platform stored.
	live *liveRead
	// podsHint, when set, is the number of Pods a sum of Requests or Limits is of (the replicas the workload is set to, from the Kubernetes API); else the ready replicas are the start.
	podsHint int
	// podSpec, when set, is the Requests and the Limits of one Pod as Kubernetes has them (the operator read the Pod template): they are the figures now, and nothing is worked
	// out of a sum. configEstimated is set when a figure of a Pod had to be worked out of a sum of Prometheus (see buildConfig).
	podSpec         *PodSpec
	desired         int
	configEstimated bool
	// only, when set, limits a live reading to the kinds in it (the reading of the recommendation period needs only what the verdict is made of).
	only map[string]bool
}

func (s *Service) newOPLoader(ctx context.Context, m *model.ForecastModule, from, to time.Time) *opLoader {
	l := &opLoader{s: s, ctx: ctx, m: m, run: &model.AlgorithmRun{ID: m.ID, AllSources: true, Sources: m.Sources}, from: from, to: to, cache: map[string]*opSeries{}}
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
	maxAge := costReplicaMaxAge
	if l.live != nil {
		maxAge = l.live.repMaxAge
	}
	if i < 0 || ts-l.reps[i].TS > int64(maxAge/time.Second) || l.reps[i].Value < 1 {
		return 0, false
	}
	return l.reps[i].Value, true
}

// get reads the source of a kind (nil when the module has none, or nothing is stored); a live loader has read them all already.
func (l *opLoader) get(kind string) *opSeries {
	if v, ok := l.cache[kind]; ok {
		return v
	}
	l.cache[kind] = nil
	if l.live != nil {
		return nil
	}
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
	level := ""
	if mp, err := l.s.Mappings.GetByID(l.ctx, src.MappingID); err == nil {
		level = mp.Level
	}
	points := make([]overprovision.Point, 0, len(rows))
	for _, r := range rows {
		if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			continue
		}
		points = append(points, overprovision.Point{TS: r.TS, V: r.Value})
	}
	out := l.build(points, level, series.StepSeconds)
	l.cache[kind] = out
	return out
}

// build makes the series of a kind of its points: a workload-total series is divided by the ready replicas of the same moment, a series that is already per
// Pod is taken as it is (and multiplied by them for the total).
func (l *opLoader) build(raw []overprovision.Point, level string, step int) *opSeries {
	out := &opSeries{step: step, raw: raw}
	for _, p := range raw {
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
	return out
}

// buildConfig makes the series of a Requests or a Limits source whose query answers all the replicas together. The figure of one Pod is the total divided by the
// number of Pods it was summed over, and that number is not always the ready replicas: a Pod that is Pending or still starting is in the sum (a Pod that has
// been scheduled has its Requests) and not among the ready ones, so 48 cores of Requests of 12 Pods of 4 cores would be 4.36 over 11 ready replicas. The Requests
// and Limits of a Pod are figures somebody wrote in a spec (4 cores, 6 GiB), so of the counts near the ready replicas the first that gives such a figure is taken.
func (l *opLoader) buildConfig(raw []overprovision.Point, step int, unit string) *opSeries {
	out := &opSeries{step: step, raw: raw}
	est, estimated := l.estimatePod(raw, unit)
	for _, p := range raw {
		out.total = append(out.total, p)
		if estimated {
			out.pod = append(out.pod, overprovision.Point{TS: p.TS, V: est})
			continue
		}
		rep, known := l.repAt(p.TS)
		if l.podsHint > 0 {
			rep, known = float64(l.podsHint), true
		}
		if known {
			out.pod = append(out.pod, overprovision.Point{TS: p.TS, V: p.V / podsOfTotal(p.V, rep, unit)})
		}
	}
	return out
}

// estimateWindow is how far back the figure of one Pod is worked out of a sum: the median of the sum and of the replicas over it. A rollout puts the old and the new Pods in the
// sum for a while and the newest total is then twice what a steady workload has; the median of a few hours is not.
const estimateWindow = 6 * time.Hour

// estimatePod works out the Requests (or the Limits) of one Pod from the sum over the Pods, with no Kubernetes report to read it from: the median of the sum over the last hours
// divided by the median of the replicas over the same hours (the Pods near it are tried for a figure a spec can say, see podsOfTotal). It is an estimate, and the answer says
// so. false when there is no such stretch (too few points of either), and then the sum at each moment is divided by the replicas of that moment.
func (l *opLoader) estimatePod(raw []overprovision.Point, unit string) (float64, bool) {
	if l.podsHint > 0 || len(raw) < 6 {
		return 0, false
	}
	since := raw[len(raw)-1].TS - int64(estimateWindow/time.Second)
	var totals, reps []float64
	for _, p := range raw {
		if p.TS >= since && p.V > 0 {
			totals = append(totals, p.V)
		}
	}
	for _, sm := range l.reps {
		if sm.TS >= since && sm.Value >= 1 && !math.IsNaN(sm.Value) {
			reps = append(reps, sm.Value)
		}
	}
	if len(totals) < 6 || len(reps) < 6 {
		return 0, false
	}
	sort.Float64s(totals)
	sort.Float64s(reps)
	total, rep := totals[len(totals)/2], reps[len(reps)/2]
	l.configEstimated = true
	return total / podsOfTotal(total, rep, unit), true
}

// podsOfTotal is how many Pods a sum of the Requests or the Limits was made over, taken near the ready replicas: the ready replicas themselves when that gives a figure a
// spec could say, else one or two more, one fewer, three more; when none does, the ready replicas.
func podsOfTotal(total, ready float64, unit string) float64 {
	base := math.Max(math.Round(ready), 1)
	for _, d := range []float64{0, 1, 2, -1, 3} {
		if n := base + d; n >= 1 && specLike(total/n, unit) {
			return n
		}
	}
	return base
}

// specLike says whether a figure is one a Pod spec could say: CPU in whole 10 mCPU below a core and in 50 mCPU from it, memory in 16 MiB below a GiB and in 64 MiB from it.
func specLike(v float64, unit string) bool {
	if !(v > 0) {
		return false
	}
	near := func(x, step float64) bool { return math.Abs(x-math.Round(x/step)*step) < 0.5 }
	if strings.EqualFold(unit, "bytes") {
		mib := v / (1 << 20)
		if mib >= 1024 {
			return near(mib, 64)
		}
		return near(mib, 16)
	}
	milli := v * 1000
	if milli >= 1000 {
		return near(milli, 50)
	}
	return near(milli, 10)
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
	// Threshold is the heap pressure of the module (module management: the heap used over the heap maximum above which the replicas are raised; 0 = the rule is off),
	// Ratio the P99 of the heap used over the highest heap maximum of the window, and Score what the pressure comes to: 100 x (1 - Ratio / Threshold), 0 at the
	// threshold and above it. Score is set only when both the threshold and the two heap metrics are there.
	Threshold float64 `json:"threshold"`
	Ratio     float64 `json:"ratio,omitempty"`
	Score     float64 `json:"score"`
	Scored    bool    `json:"scored"`
}

// OOMEvent is a moment containers of the module were killed for memory and how many.
type OOMEvent struct {
	TS    int64   `json:"ts"`
	Count float64 `json:"count"`
}

// OOMInfo is what the OOM metric of a module says over the window: how many kills, when, and the score they come to. The coefficients of the OOM protection are not a
// part of it (they belong to module management); the dashboard shows the count, the moments and the score.
type OOMInfo struct {
	// Configured is whether the module has an OOM metric at all.
	Configured bool    `json:"configured"`
	Kills      float64 `json:"kills"`
	// Moments is how many moments the kills were at; Events lists the newest of them (at most maxOOMEvents).
	Moments int        `json:"moments"`
	Events  []OOMEvent `json:"events"`
	// LookbackDays is how far back the OOM protection looks (module management). Weight is the share of the memory score one kill takes away; ScoreOn says whether the
	// OOM weight is switched on, Score is 100 x (1 - kills x weight), never below 0.
	LookbackDays int     `json:"lookback_days"`
	ScoreOn      bool    `json:"score_on"`
	Weight       float64 `json:"weight"`
	Score        float64 `json:"score"`
	// Protection is what the kills did to the recommendation of the memory of this page (nil when there was no kill in the recommendation period). The score is a different
	// thing: it counts kills against the memory score of a forecasting algorithm (which algorithm is followed) and never changes a Requests or a replica count here.
	Protection *OOMProtection `json:"protection,omitempty"`
}

// OOMProtection is the protection a kill gives the memory: the memory Requests is not recommended below max(the working set measured at the kill x the OOM protection
// coefficient, the Requests it has now), and no replica is taken away. The kills counted are those of the recommendation period (the period of the page's recommendation); the
// "look-back days" of module management are the period the forecast pipeline's own OOM floor looks at.
type OOMProtection struct {
	Kills         float64 `json:"kills"`
	PeriodMinutes int     `json:"period_minutes"`
	WorkingSet    float64 `json:"working_set"`
	Uplift        float64 `json:"uplift"`
	Request       float64 `json:"request"`
	Floor         float64 `json:"floor"`
	// NeedBefore is what the percentiles asked of the Requests before the floor, and FloorRaised whether the floor is above it (the kill is what sets the recommendation).
	NeedBefore   float64 `json:"need_before"`
	FloorRaised  bool    `json:"floor_raised"`
	ReplicasHeld bool    `json:"replicas_held"`
}

// maxOOMEvents is how many moments of kills the detail lists (the newest).
const maxOOMEvents = 20

func (l *opLoader) oomInfo(m *model.ForecastModule, from, to int64) *OOMInfo {
	info := &OOMInfo{LookbackDays: m.OOMLookbackDays, ScoreOn: m.OOMWeightEnabled, Weight: m.OOMScoreWeight(), Score: 100, Events: []OOMEvent{}}
	u := l.get("oom")
	if u == nil {
		return info
	}
	info.Configured = true
	for _, pt := range sliceFrom(u.raw, from, to) {
		if pt.V > 0 && !math.IsNaN(pt.V) {
			info.Kills += pt.V
			info.Events = append(info.Events, OOMEvent{TS: pt.TS, Count: pt.V})
		}
	}
	info.Moments = len(info.Events)
	sort.Slice(info.Events, func(i, j int) bool { return info.Events[i].TS > info.Events[j].TS })
	if len(info.Events) > maxOOMEvents {
		info.Events = info.Events[:maxOOMEvents]
	}
	if info.ScoreOn {
		info.Score = math.Max(0, 100*(1-info.Kills*info.Weight))
	}
	return info
}

// ResourceChart is the series of a chart: the use of the Pod in the window, the request and the suggestion. The arrays are aligned.
type ResourceChart struct {
	Resource string    `json:"resource"`
	StepSecs int       `json:"step_seconds"`
	TS       []int64   `json:"ts"`
	Value    []float64 `json:"value"`
	Request  float64   `json:"request"`
	Suggest  float64   `json:"suggested"`
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
	// ReplicasFrom says where the replicas now come from: "kubernetes" (what the operator read from the Kubernetes API a few minutes ago: the replicas the workload is set to), else
	// "prometheus" (the newest figure of the replicas metric the module has set up, usually the ready replicas) or
	// "stored" (the newest sample the platform stored of that metric when Prometheus did not answer, not older than the age a figure may have); empty when there is none.
	// ReplicasAt is when it was measured (unix seconds), ReplicasMin and ReplicasMax the fewest and the most in the recommendation period.
	ReplicasFrom string `json:"replicas_from,omitempty"`
	// ReplicasReady is how many of them are ready when the source is Kubernetes (the replicas are what the workload is set to).
	ReplicasReady int   `json:"replicas_ready,omitempty"`
	ReplicasAt    int64 `json:"replicas_at,omitempty"`
	ReplicasMin   int   `json:"replicas_min,omitempty"`
	ReplicasMax   int   `json:"replicas_max,omitempty"`
	// What Kubernetes says of the replicas when the source is Kubernetes: ReplicasAbnormal are the Pods that are not ready, ReplicasPods the Pods that exist (more than the replicas
	// the workload is set to while a rollout has the old and the new Pods side by side) and ReplicasUpdating whether a rollout is under way.
	ReplicasAbnormal int  `json:"replicas_abnormal,omitempty"`
	ReplicasPods     int  `json:"replicas_pods,omitempty"`
	ReplicasUpdating bool `json:"replicas_updating,omitempty"`
	// ReplicasWhy says why Kubernetes could not give the replicas (no_operator, stale, missing, error) when the figure comes from elsewhere. ReplicasLatest is the newest figure of
	// Prometheus when it was set aside for being far above the usual (from "estimated": the replicas are what the period usually had, not the figure of a rollout).
	ReplicasWhy string `json:"replicas_why,omitempty"`
	// PodSpecFrom says where the Requests and the Limits of one Pod come from: "kubernetes" (the Pod template the operator read) or "estimated" (worked out of the sums of Prometheus
	// by the median of the last hours, see estimatePod); empty when the module has neither.
	PodSpecFrom    string `json:"pod_spec_from,omitempty"`
	ReplicasLatest int    `json:"replicas_latest,omitempty"`
	// Coefficients are those of the module in module management.
	Coefficients *ModuleCoefficients `json:"coefficients,omitempty"`
	// Identity says which workload this is in the cluster: the cluster the operator reports for it and its Pods by name (nil when the operator never reported the workload).
	Identity *WorkloadIdentity `json:"identity,omitempty"`
	// Windows are the results, shortest window first; each has a CPU and a memory result (nil when the module has no such use series).
	Windows []*WindowOverprovision `json:"windows"`
	JVM     *JVMInfo               `json:"jvm,omitempty"`
	OOM     *OOMInfo               `json:"oom,omitempty"`
	Charts  []*ResourceChart       `json:"charts,omitempty"`
	// Recommend is the analysis over the recommendation period (a system parameter), which does not follow the window of the page: the verdict, the figures of what the
	// Requests and the Limits could be and by how much they are more or less than the use needs are this one's. The usage figures, the charts and the metrics are of the window.
	Recommend *WindowOverprovision `json:"recommend,omitempty"`
	// Metrics are the other metrics the module has set up (container, JVM, throttling, OOM ...), over the window.
	Metrics []MetricStat `json:"metrics"`
	// Datasource, Queries, Partial and Skipped are set when the figures were read from Prometheus (the dashboard): the data source, the query of each metric with
	// what came of it, and whether the time limit of the request stopped some of them.
	Datasource CostSource  `json:"datasource"`
	Queries    []LiveQuery `json:"queries"`
	Partial    bool        `json:"partial"`
	Skipped    int         `json:"skipped"`
}

// ModuleCoefficients are the coefficients of the calculation as module management has them for the module, so a card of the dashboard shows the figure that is
// set and not one written into the page.
type ModuleCoefficients struct {
	// CapacityReserve is the capacity reserve coefficient in force (1 when the module sets none, Set false): a recommendation is the need divided by it.
	CapacityReserve    float64 `json:"capacity_reserve"`
	CapacityReserveSet bool    `json:"capacity_reserve_set"`
	// Headroom is the recommendation coefficient (the peak is multiplied by it), OOMUplift the OOM protection coefficient.
	Headroom  float64 `json:"headroom"`
	OOMUplift float64 `json:"oom_uplift"`
}

func coefficientsOf(m *model.ForecastModule) *ModuleCoefficients {
	headroom := m.HeadroomRatio
	if headroom < 1 {
		headroom = 1
	}
	return &ModuleCoefficients{CapacityReserve: m.CapacityReserve(), CapacityReserveSet: m.TargetUtilization > 0 && m.TargetUtilization <= 1, Headroom: headroom, OOMUplift: m.OOMMemoryUplift}
}

// WindowOverprovision is the result of one window.
type WindowOverprovision struct {
	Label         string                `json:"label"`
	WindowMinutes int                   `json:"window_minutes"`
	CPU           *overprovision.Result `json:"cpu"`
	Memory        *overprovision.Result `json:"memory"`
	// ReplicaPlan is how many replicas the module needs if every Pod keeps the Requests it has (the other way to the same use: the Pod spec stays, the replicas move);
	// the CPU and memory results are the way where the replicas stay and the Pod spec moves. The replicas it starts from are the ones the cluster has now.
	ReplicaPlan *overprovision.ReplicaPlan `json:"replica_plan"`
	// LoadProfile is the replicas each hour of the day asks for: which hours are the busy ones and what a count that follows them would save (nil when the window is shorter than
	// two days or the use of all the Pods was not measured).
	LoadProfile *overprovision.LoadProfile `json:"load_profile"`
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
				in.RequestTotal = lastValue(sliceFrom(rs.total, from, to))
			}
		}
		if ls := l.get(limitKind); ls != nil {
			if v := lastValue(sliceFrom(ls.pod, from, to)); v > 0 {
				in.Limit, in.LimitFrom = v, "metric"
				in.LimitTotal = lastValue(sliceFrom(ls.total, from, to))
			}
		}
		// what the Pod template says is what the Pod has: it is not worked out of a sum, and it is not a figure of Prometheus
		if spec := l.podSpec; spec != nil {
			request, limit := spec.CPURequest, spec.CPULimit
			if resource == overprovision.Memory {
				request, limit = spec.MemoryRequest, spec.MemoryLimit
			}
			if request > 0 {
				in.Request, in.RequestTotal = request, request*float64(l.desired)
			}
			if limit > 0 {
				in.Limit, in.LimitFrom, in.LimitTotal = limit, "kubernetes", limit*float64(l.desired)
			}
		}
		if in.Limit <= 0 && limitRatio >= 1 && in.Request > 0 {
			// admission gives a new Pod the request times this ratio as its limit: it is what the module is configured to, not what was measured
			in.Limit, in.LimitFrom = in.Request*limitRatio, "module"
		}
		// all the replicas together are one Pod times the replicas the workload has now (12 x 16 GiB = 192 GiB): not the sum Prometheus made, which counts the old and the
		// new Pods of a rollout and every series it holds twice, and gave 24 x 16 GiB = 384 GiB for 12 replicas. It is kept only when no replicas are known.
		if replicas > 0 {
			if in.Request > 0 {
				in.RequestTotal = in.Request * float64(replicas)
			}
			if in.Limit > 0 {
				in.LimitTotal = in.Limit * float64(replicas)
			}
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
	// the requests and limits of the dashboard come from Prometheus (the metrics k8s_*_requests and k8s_*_limits) and from nothing the platform holds: the
	// settings of the module and its limit ratio stand in for neither when the module has no such metric
	cpuRequest, memRequest := float64(l.m.PodCPURequestMilli)/1000, float64(l.m.PodMemoryRequestBytes)
	cpuRatio, memRatio := l.m.LimitCPURatio, l.m.LimitMemoryRatio
	if l.live != nil {
		cpuRequest, memRequest, cpuRatio, memRatio = 0, 0, 0, 0
	}
	cpu := one(overprovision.CPU, "cpu", "k8s_cpu_requests", "k8s_cpu_limits", cpuRequest, cpuRatio, cpuRound)
	mem := one(overprovision.Memory, "memory", "k8s_mem_requests", "k8s_mem_limits", memRequest, memRatio, memRound)
	return &WindowOverprovision{Label: label, WindowMinutes: minutes, CPU: cpu, Memory: mem, ReplicaPlan: overprovision.PlanReplicas(replicas, p, replicaLimits(l.m), cpu, mem),
		LoadProfile: l.loadProfile(from, to, p, cpu, mem)}
}

// loadProfile is the replicas each hour of the day asks for, from the use of all the Pods as it counts (held for the sustain window) and the Requests of a Pod.
func (l *opLoader) loadProfile(from, to int64, p overprovision.Params, cpu, mem *overprovision.Result) *overprovision.LoadProfile {
	loc, err := time.LoadLocation(l.m.Timezone)
	if err != nil {
		loc = time.UTC
	}
	var ins []overprovision.LoadInput
	for _, c := range []struct {
		kind string
		r    *overprovision.Result
	}{{"cpu", cpu}, {"memory", mem}} {
		use := l.get(c.kind)
		if use == nil || c.r == nil || !(c.r.Request > 0) {
			continue
		}
		total := sliceFrom(use.total, from, to)
		if len(total) == 0 {
			continue
		}
		ins = append(ins, overprovision.LoadInput{Resource: c.r.Resource, Total: overprovision.Sustained(total, p.SustainMinutes), Raw: total, Request: c.r.Request, StepSeconds: use.step})
	}
	prof := overprovision.BuildLoadProfile(ins, p.P95.Target, 95, loc)
	prof.AddRecent(ins, p.P95.Target, time.Unix(to, 0), loc)
	// the replicas the workload really had, hour by hour: the module's replicas metric as it was measured, not what the use asks for
	var reps []overprovision.Point
	for _, sm := range l.reps {
		if sm.TS >= from && sm.TS <= to {
			reps = append(reps, overprovision.Point{TS: sm.TS, V: sm.Value})
		}
	}
	source := ""
	if src := l.run.FirstOfKind("replicas"); src != nil {
		source = src.Name
	}
	prof.AddActual(reps, time.Unix(to, 0), loc, source)
	return prof
}

// replicaLimits are the constraints the module sets on its replicas in module management: its least and most replicas and the most one action adds or removes. A module
// that is kept at a fixed number of replicas has no room to move. The replica plan keeps to them and has no numbers of its own.
func replicaLimits(m *model.ForecastModule) overprovision.ReplicaLimits {
	lim := overprovision.ReplicaLimits{Min: m.MinReplicas, Max: m.MaxReplicas, ScaleUpStep: m.ScaleUpMaxStep, ScaleDownStep: m.ScaleDownMaxStep}
	if m.FixedReplicas > 0 {
		lim.Min, lim.Max = m.FixedReplicas, m.FixedReplicas
	}
	return lim
}

// k8sReportMaxAge is how old the report of the operator may be and still say what the replicas are now: it reads the cluster every five minutes.
const k8sReportMaxAge = 15 * time.Minute

// WorkloadReplicas is what the operator last read of the replicas of the workload of a module from the Kubernetes API: what it is set to (Desired), what is ready, the Pods that
// are not ready (Abnormal), the Pods that exist (Pods: more than Desired while a rollout runs the old and the new side by side) and whether a rollout is under way.
// OK is false when there is no usable report, and Why says why: no_operator (nothing was ever reported), stale (older than the operator reports), missing (the workload is
// not in the cluster) or error (it could not be read).
type WorkloadReplicas struct {
	// Spec is the Requests and the Limits of one Pod as the operator read them from the Pod template of the workload (the sum over its containers); nil when the report has none.
	Spec                                               *PodSpec
	Desired, Ready, Available, Updated, Pods, Abnormal int
	Rolling                                            bool
	At                                                 time.Time
	OK                                                 bool
	Why                                                string
}

// PodSpec is the Requests and the Limits of one Pod, CPU in cores and memory in bytes (a Limits of 0 says the Pod has none for that resource).
type PodSpec struct {
	CPURequest, CPULimit, MemoryRequest, MemoryLimit float64
}

// podSpecOf sums the containers of the Pod template (the Pod asks for what its containers ask for together). A Limits counts only when every container sets one: a container
// without it can use what the node has, so the Pod has none.
func podSpecOf(containers string) *PodSpec {
	var list []struct {
		Requests struct {
			CPUMilli    int64 `json:"cpu_milli"`
			MemoryBytes int64 `json:"memory_bytes"`
		} `json:"requests"`
		Limits struct {
			CPUMilli    int64 `json:"cpu_milli"`
			MemoryBytes int64 `json:"memory_bytes"`
		} `json:"limits"`
	}
	if json.Unmarshal([]byte(containers), &list) != nil || len(list) == 0 {
		return nil
	}
	spec := &PodSpec{}
	cpuLimit, memLimit := true, true
	for _, c := range list {
		spec.CPURequest += float64(c.Requests.CPUMilli) / 1000
		spec.MemoryRequest += float64(c.Requests.MemoryBytes)
		spec.CPULimit += float64(c.Limits.CPUMilli) / 1000
		spec.MemoryLimit += float64(c.Limits.MemoryBytes)
		cpuLimit = cpuLimit && c.Limits.CPUMilli > 0
		memLimit = memLimit && c.Limits.MemoryBytes > 0
	}
	if !cpuLimit {
		spec.CPULimit = 0
	}
	if !memLimit {
		spec.MemoryLimit = 0
	}
	if spec.CPURequest <= 0 && spec.MemoryRequest <= 0 {
		return nil
	}
	return spec
}

func (s *Service) workloadReplicas(ctx context.Context, m *model.ForecastModule, now time.Time) WorkloadReplicas {
	if s.Inventory == nil {
		return WorkloadReplicas{Why: "no_operator"}
	}
	row, err := s.Inventory.GetWorkload(ctx, m.ID)
	switch {
	case err != nil || row == nil:
		return WorkloadReplicas{Why: "no_operator"}
	case row.Missing:
		return WorkloadReplicas{At: row.ObservedAt, Why: "missing"}
	case row.Error != "":
		return WorkloadReplicas{At: row.ObservedAt, Why: "error"}
	case now.Sub(row.ObservedAt) > k8sReportMaxAge:
		return WorkloadReplicas{At: row.ObservedAt, Why: "stale"}
	case row.Desired < 1:
		return WorkloadReplicas{At: row.ObservedAt, Why: "error"}
	}
	w := WorkloadReplicas{Desired: row.Desired, Ready: row.Ready, Available: row.Available, Updated: row.Updated, At: row.ObservedAt, OK: true, Spec: podSpecOf(row.Containers)}
	var pods []struct {
		Ready bool `json:"ready"`
	}
	if json.Unmarshal([]byte(row.Pods), &pods) == nil && len(pods) > 0 {
		w.Pods = len(pods)
		for _, pod := range pods {
			if !pod.Ready {
				w.Abnormal++
			}
		}
	} else {
		w.Abnormal = max(row.Desired-row.Ready, 0)
	}
	w.Rolling = (row.Updated > 0 && row.Updated < row.Desired) || w.Pods > row.Desired
	return w
}

// WorkloadIdentity is the workload of a module as the operator last saw it: the cluster, the namespace, the kind and the name, and the Pods it has by name with whether each is ready.
// ObservedAt is when the operator read it (unix seconds); Missing says the workload was not found in the cluster then.
type WorkloadIdentity struct {
	Cluster    string        `json:"cluster"`
	Namespace  string        `json:"namespace"`
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	Pods       []PodIdentity `json:"pods"`
	ObservedAt int64         `json:"observed_at"`
	Missing    bool          `json:"missing,omitempty"`
}

// PodIdentity is one Pod of the workload: its name, the node it runs on, its phase, whether it is ready and how often its containers restarted.
type PodIdentity struct {
	Name     string `json:"name"`
	Node     string `json:"node,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
}

// workloadIdentity reads the workload of the module from the report of the operator: nil when there is none.
func (s *Service) workloadIdentity(ctx context.Context, m *model.ForecastModule) *WorkloadIdentity {
	if s.Inventory == nil {
		return nil
	}
	row, err := s.Inventory.GetWorkload(ctx, m.ID)
	if err != nil || row == nil {
		return nil
	}
	out := &WorkloadIdentity{Cluster: row.Cluster, Namespace: row.Namespace, Kind: row.Kind, Name: row.Name, Pods: []PodIdentity{}, ObservedAt: row.ObservedAt.Unix(), Missing: row.Missing}
	var pods []PodIdentity
	if json.Unmarshal([]byte(row.Pods), &pods) == nil {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		out.Pods = append(out.Pods, pods...)
	}
	return out
}

// PodsCounted is the number of Pods a sum of Requests or Limits is of: the Pods that exist (the old and the new ones of a rollout are all in the sum), else the replicas the
// workload is set to.
func (w WorkloadReplicas) PodsCounted() int {
	if w.Pods > 0 {
		return w.Pods
	}
	return w.Desired
}

// stableReplicas is what the period usually had: the median of the replicas metric since a moment (false when there are too few samples to say). A rollout that has the old
// and the new Pods side by side makes the newest figure much higher for a while; this is not.
func (l *opLoader) stableReplicas(since int64) (int, bool) {
	var values []float64
	for _, sm := range l.reps {
		if sm.TS >= since && sm.Value >= 1 && !math.IsNaN(sm.Value) {
			values = append(values, sm.Value)
		}
	}
	if len(values) < 12 {
		return 0, false
	}
	sort.Float64s(values)
	return int(math.Round(values[len(values)/2])), true
}

// kubernetesReplicas is what the operator last read from the Kubernetes API of the workload of the module: the replicas it is set to (spec.replicas), and how many of
// them are ready. ok is false when nothing was reported, the report is old, the workload was not found or could not be read.
func (s *Service) kubernetesReplicas(ctx context.Context, m *model.ForecastModule, now time.Time) (desired, ready int, at time.Time, ok bool) {
	if s.Inventory == nil {
		return 0, 0, time.Time{}, false
	}
	row, err := s.Inventory.GetWorkload(ctx, m.ID)
	if err != nil || row == nil || row.Missing || row.Error != "" || row.Desired < 1 || now.Sub(row.ObservedAt) > k8sReportMaxAge {
		return 0, 0, time.Time{}, false
	}
	return row.Desired, row.Ready, row.ObservedAt, true
}

// replicaFacts are what the replicas metric says over the period: the newest value and when it was measured, the fewest and the most. The replicas "now" are the
// newest figure of the cluster (Prometheus reads what Kubernetes has), never one a forecast, a recommendation or an old stored result made: when there is none, there is none.
type replicaFacts struct {
	Now, Min, Max int
	At            int64
}

func (l *opLoader) replicaFacts(replicas int, since int64) replicaFacts {
	f := replicaFacts{Now: replicas}
	for i, s := range l.reps {
		if i == len(l.reps)-1 && !math.IsNaN(s.Value) && s.Value >= 1 {
			f.At = s.TS
		}
		if math.IsNaN(s.Value) || s.Value < 1 || s.TS < since {
			continue
		}
		v := int(math.Round(s.Value))
		if f.Min == 0 || v < f.Min {
			f.Min = v
		}
		if v > f.Max {
			f.Max = v
		}
	}
	return f
}

func (s *Service) moduleHeader(m *model.ForecastModule, replicas int, at time.Time) *ModuleOverprovision {
	return &ModuleOverprovision{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload, Replicas: replicas, At: at}
}

// AnalyzeOverprovision analyses a module over every window and stores the results (one row for each window and resource that has a result). It is what the
// scheduled task runs and what the dashboard's "analyse now" asks for.
func (s *Service) AnalyzeOverprovision(ctx context.Context, m *model.ForecastModule) (*ModuleOverprovision, error) {
	now := s.now()
	last := overprovision.Windows[len(overprovision.Windows)-1]
	p := moduleParams(s.OverprovisionParams(ctx), m)
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

// OverprovisionDetail analyses one module from Prometheus, over a window of the list or over a custom range (from and to; both zero for a window), with the
// series of the chart, the JVM metrics and the query of each metric. Nothing is read from what the platform stored, nothing is stored, and no forecast takes part.
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
	p := moduleParams(s.OverprovisionParams(ctx), m)
	l := s.newLiveLoader(ctx, m, to.Add(-time.Duration(windowMinutes)*time.Minute), to)
	// the replicas the workload is set to, from the Kubernetes API: the Pods a sum of Requests was made over are these (a Pod still starting has its Requests and is not ready)
	w := s.workloadReplicas(ctx, m, now)
	if w.OK {
		l.podsHint = w.PodsCounted()
		l.podSpec, l.desired = w.Spec, w.Desired
	}
	// the recommendation has a period of its own: when it is longer than the window of the page it is read apart (the window would be read in a coarser step to
	// reach it), and when it is not the window's own series is cut to it
	recMinutes := s.OverprovisionBaseline(ctx)
	recLoader := l
	var wg sync.WaitGroup
	if recMinutes > windowMinutes {
		recLoader = s.newLiveLoader(ctx, m, to.Add(-time.Duration(recMinutes)*time.Minute), to)
		recLoader.podsHint, recLoader.podSpec, recLoader.desired = l.podsHint, l.podSpec, l.desired
		recLoader.only = recommendKinds
		wg.Add(1)
		go func() {
			defer wg.Done()
			recLoader.readLive()
		}()
	}
	l.readLive()
	wg.Wait()
	replicas, repFrom := l.liveReplicas(), "prometheus"
	latest := 0
	if w.OK {
		replicas, repFrom = w.Desired, "kubernetes"
	} else if replicas > 0 {
		// Prometheus counts the Pods that exist, and during a rollout that is the old ones and the new ones: a figure far above what the period usually had is not the replicas
		// of the workload, and a recommendation made from it would take replicas away that are needed
		if stable, ok := l.stableReplicas(to.Add(-time.Duration(s.OverprovisionBaseline(ctx)) * time.Minute).Unix()); ok && replicas >= stable+2 && float64(replicas) > 1.25*float64(stable) {
			latest, replicas, repFrom = replicas, stable, "estimated"
		}
	}
	if replicas == 0 {
		// Prometheus did not answer the replicas: the newest sample the platform stored of the same metric (a measured figure of the cluster, with an age limit) stands in
		// for it; a recommendation or a forecast never does
		replicas, repFrom = s.costReplicas(ctx, m, l.run, nil, now), "stored"
	}
	if replicas == 0 {
		repFrom = ""
	}
	mo := s.moduleHeader(m, replicas, to)
	mo.Coefficients = coefficientsOf(m)
	mo.Identity = s.workloadIdentity(ctx, m)
	switch {
	case l.podSpec != nil:
		mo.PodSpecFrom = "kubernetes"
	case l.configEstimated || recLoader.configEstimated:
		mo.PodSpecFrom = "estimated"
	}
	facts := l.replicaFacts(replicas, to.Add(-time.Duration(recMinutes)*time.Minute).Unix())
	mo.ReplicasFrom, mo.ReplicasAt, mo.ReplicasMin, mo.ReplicasMax = repFrom, facts.At, facts.Min, facts.Max
	if w.OK {
		mo.ReplicasAt, mo.ReplicasReady = w.At.Unix(), w.Ready
		mo.ReplicasAbnormal, mo.ReplicasPods, mo.ReplicasUpdating = w.Abnormal, w.Pods, w.Rolling
	} else {
		mo.ReplicasWhy, mo.ReplicasLatest = w.Why, latest
	}
	wr := l.analyzeWindow(label, windowMinutes, p, replicas)
	mo.Windows = []*WindowOverprovision{wr}
	recLabel := fmt.Sprintf("%dm", recMinutes)
	if w, ok := overprovision.WindowByMinutes(recMinutes); ok {
		recLabel = w.Label
	}
	mo.Recommend = recLoader.analyzeWindow(recLabel, recMinutes, p, replicas)

	fromTS, toTS := to.Add(-time.Duration(windowMinutes)*time.Minute).Unix(), to.Unix()
	for _, c := range []struct {
		kind string
		res  *overprovision.Result
		rec  *overprovision.Result
	}{{"cpu", wr.CPU, mo.Recommend.CPU}, {"memory", wr.Memory, mo.Recommend.Memory}} {
		if c.res == nil {
			continue
		}
		use := l.get(c.kind)
		if use == nil {
			continue
		}
		chart := opChart(c.kind, sliceFrom(use.pod, fromTS, toTS), use.step, windowMinutes, c.res)
		if c.rec != nil {
			// the line of the recommended Requests is the one of the recommendation period, not of the window
			chart.Suggest = c.rec.Suggested
		}
		opTotal(chart, sliceFrom(use.total, fromTS, toTS), use.step, windowMinutes, replicas)
		mo.Charts = append(mo.Charts, chart)
	}
	mo.JVM = l.jvm(m, fromTS, toTS, wr)
	mo.OOM = l.oomInfo(m, fromTS, toTS)
	if mem := mo.Recommend.Memory; mem != nil && mem.OOMKills > 0 && mo.OOM != nil {
		need := 0.0
		for _, c := range mem.Candidates {
			need = math.Max(need, c.Needed)
		}
		floor := math.Max(mem.OOMWorkingSet*p.OOMUplift, mem.Request)
		held := mo.Recommend.ReplicaPlan != nil && containsString(mo.Recommend.ReplicaPlan.Reasons, overprovision.ReasonReplicasLimited)
		mo.OOM.Protection = &OOMProtection{Kills: mem.OOMKills, PeriodMinutes: recMinutes, WorkingSet: mem.OOMWorkingSet, Uplift: p.OOMUplift, Request: mem.Request, Floor: floor,
			NeedBefore: need, FloorRaised: floor > need, ReplicasHeld: held}
	}
	mo.Metrics = l.metrics(fromTS, toTS)
	mo.Datasource = l.live.datasource
	mo.Queries, mo.Partial, mo.Skipped = l.liveSummary()
	return &DetailOverprovision{ModuleOverprovision: mo, Params: p}, nil
}

// DetailOverprovision is the answer of the detail: the analysis and the numbers it was made with.
type DetailOverprovision struct {
	*ModuleOverprovision
	Params overprovision.Params `json:"params"`
}

// opChart downsamples the use of a Pod to at most opMaxPoints, in the step of the window.
func opChart(kind string, pts []overprovision.Point, stored, windowMinutes int, res *overprovision.Result) *ResourceChart {
	step := overprovision.MAStepFor(windowMinutes, stored)
	rs := overprovision.Resample(pts, step)
	stride := (len(rs) + opMaxPoints - 1) / opMaxPoints
	if stride < 1 {
		stride = 1
	}
	c := &ResourceChart{Resource: kind, StepSecs: step, Request: res.Request, Suggest: res.Suggested}
	for i := 0; i < len(rs); i += stride {
		c.TS = append(c.TS, rs[i].TS)
		c.Value = append(c.Value, round6(rs[i].V))
	}
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

// opThin resamples a series to the step of the window and thins it to at most opMaxPoints, as the series of the charts are.
func opThin(pts []overprovision.Point, stored, windowMinutes int) ([]int64, []float64) {
	rs := overprovision.Resample(pts, overprovision.MAStepFor(windowMinutes, stored))
	stride := (len(rs) + opMaxPoints - 1) / opMaxPoints
	if stride < 1 {
		stride = 1
	}
	ts, values := []int64{}, []float64{}
	for i := 0; i < len(rs); i += stride {
		ts = append(ts, rs[i].TS)
		values = append(values, round6(rs[i].V))
	}
	return ts, values
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
func (l *opLoader) jvm(m *model.ForecastModule, from, to int64, wr *WindowOverprovision) *JVMInfo {
	expected := false
	for _, src := range l.run.ActiveSources() {
		if strings.HasPrefix(src.Kind, "jvm_") {
			expected = true
		}
	}
	if !expected {
		return nil
	}
	info := &JVMInfo{Expected: true, Hints: []string{}, Threshold: m.JVMHeapPressure}
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
	if info.HeapUsed != nil && info.HeapMax != nil && info.HeapMax.Max > 0 {
		info.Ratio = info.HeapUsed.P99 / info.HeapMax.Max
		if m.JVMHeapPressure > 0 {
			// the threshold of the module is the line: no number of the code says where "near" starts
			info.Scored = true
			info.Score = math.Max(0, 100*(1-info.Ratio/m.JVMHeapPressure))
			if info.Ratio >= m.JVMHeapPressure {
				info.Hints = append(info.Hints, "heap_near_max")
			}
		}
	}
	if info.HeapCommitted != nil {
		// said whenever it is shown: the committed heap is a request to the system, not a measure of use
		info.Hints = append(info.Hints, "committed_is_not_use")
		if m.JVMHeapPressure > 0 && info.HeapUsed != nil && info.HeapCommitted.P99 > 0 && info.HeapUsed.P99 >= m.JVMHeapPressure*info.HeapCommitted.P99 {
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
	// Display is true for a metric that is only shown, with no weight and no part in the algorithms.
	Display bool `json:"display"`
	// TS and Value are the series of the metric over the window (of one Pod where the metric is of Pods), thinned to what a chart draws.
	TS    []int64   `json:"ts,omitempty"`
	Value []float64 `json:"value,omitempty"`
}

// seriesOf is the series of one source: of its own for a live reading, the series of its kind for stored samples.
func (l *opLoader) seriesOf(src *model.MetricSource) *opSeries {
	if l.live != nil {
		return l.byKey[src.Key]
	}
	return l.get(src.Kind)
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

// metrics reads the other enabled sources of the module over the window: every source the module has, but the main one of the figures of the page (the CPU, the
// memory, the replicas, the Requests and the Limits, which the tables and the cards show). A source with nothing in it is still listed (with no points), so the page
// can say it is configured and empty.
func (l *opLoader) metrics(from, to int64) []MetricStat {
	out := []MetricStat{}
	for _, src := range l.run.ActiveSources() {
		if configKinds[src.Kind] && l.run.FirstOfKind(src.Kind) == src {
			continue
		}
		ms := MetricStat{Key: src.Key, Name: src.Name, Kind: src.Kind, Unit: model.MetricKinds[src.Kind], Display: src.IsDisplay()}
		level := ""
		if mp, err := l.s.Mappings.GetByID(l.ctx, src.MappingID); err == nil {
			level, ms.Unit = mp.Level, mp.Unit
		}
		ms.Level, ms.Group = level, metricGroup(src.Kind, level)
		if s := l.seriesOf(src); s != nil {
			pts := sliceFrom(s.pod, from, to)
			if len(pts) == 0 {
				pts = sliceFrom(s.raw, from, to)
			}
			u := overprovision.UsageOf(pts)
			ms.Points, ms.P50, ms.P90, ms.P95, ms.P99, ms.Max = u.Points, u.P50, u.P90, u.P95, u.P99, u.Max
			ms.Latest = lastValue(pts)
			ms.TS, ms.Value = opThin(pts, s.step, int((to-from)/60))
		}
		out = append(out, ms)
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
