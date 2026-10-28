package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

const (
	// uncertaintyMinEvaluations is how many scored prediction runs an algorithm needs before its measured
	// peak shortfall replaces the module headroom.
	uncertaintyMinEvaluations = 5
	uncertaintyWindow         = 30 * 24 * time.Hour
	uncertaintyMaxRuns        = 30
	// maxShortfall caps the shortfall used to scale a forecast: 1/(1-s) explodes as s approaches 1, and a measured shortfall is
	// the P90 of the last runs, so a few jumps of the load (a stress test, a release) that the algorithm could not have seen coming
	// would otherwise multiply every later forecast by up to 10 and put the replicas at the maximum for good. At 0.5 the forecast is
	// scaled by at most 2; an algorithm that misses peaks by more than half is not made safe by a bigger factor, it is the wrong
	// algorithm, and the plan says so.
	maxShortfall = 0.5
	// capacityLookback is the history the pod capacity is estimated from.
	capacityLookback = 7 * 24 * time.Hour
	// executionFreshness is how long the operator's last report is trusted as the current replica count.
	executionFreshness = 2 * time.Hour
)

// CapacityDetail says where the throughput of one pod came from.
type CapacityDetail struct {
	Source    string  `json:"source"`
	RPSPerPod float64 `json:"rps_per_pod"`
	Note      string  `json:"note,omitempty"`
	Samples   int     `json:"samples,omitempty"`
	Used      int     `json:"used,omitempty"`
}

// ReplicaDetail explains how the replica count was solved; it is stored in the recommendation detail.
type ReplicaDetail struct {
	Mode            string                 `json:"mode"`
	Selected        string                 `json:"selected,omitempty"`
	CurrentReplicas int                    `json:"current_replicas,omitempty"`
	Capacity        CapacityDetail         `json:"capacity"`
	Plans           []decision.ReplicaPlan `json:"plans"`
	Notes           []string               `json:"notes,omitempty"`
}

// shortfallToUncertainty turns the relative shortfall of the predicted peak (0.2: the forecast peak was 20%
// below what happened) into the factor u such that forecast x (1 + u) restores the actual peak.
func shortfallToUncertainty(s float64) float64 {
	s = math.Min(math.Max(s, 0), maxShortfall)
	return s / (1 - s)
}

// uncertaintyCapNote is the sentence that says a measured shortfall was cut to the cap; empty when it was not.
func uncertaintyCapNote(algorithm string, shortfall float64) string {
	if shortfall <= maxShortfall {
		return ""
	}
	return fmt.Sprintf("%s missed the peak by %.0f%% at the P90 of its recent evaluations, above the limit of %.0f%%: its forecast is scaled by at most x%.1f, not by x%.1f",
		algorithm, shortfall*100, maxShortfall*100, 1+shortfallToUncertainty(maxShortfall), 1/(1-math.Min(shortfall, 0.99)))
}

// uncertainty is the expected peak shortfall of an algorithm on a metric: the P90 of its measured shortfall
// over recent prediction runs, else its latest backtest, else nothing (the caller uses the module headroom).
func (s *Service) uncertainty(ctx context.Context, instanceID uint, metric, algorithm string, now time.Time) (*float64, string, float64, error) {
	since := now.Add(-uncertaintyWindow)
	evals, err := s.Predictions.AllEvaluations(ctx, rdb.EvaluationFilter{ModuleID: instanceID, Metric: metric, AlgorithmID: algorithm, Since: &since})
	if err != nil {
		return nil, "", 0, err
	}
	if len(evals) > uncertaintyMaxRuns {
		evals = evals[:uncertaintyMaxRuns] // newest first
	}
	if len(evals) >= uncertaintyMinEvaluations {
		shortfalls := make([]float64, len(evals))
		for i, e := range evals {
			shortfalls[i] = e.PeakUnder
		}
		raw := decision.Percentile(shortfalls, 0.9)
		u := shortfallToUncertainty(raw)
		return &u, decision.UncertaintyMeasured, raw, nil
	}
	run, err := s.Predictions.LatestBacktest(ctx, instanceID, metric, algorithm)
	if err != nil {
		return nil, "", 0, err
	}
	if run != nil {
		var res struct {
			PeakUnder float64 `json:"peak_under"`
			Skipped   string  `json:"skipped"`
			Points    int     `json:"points"`
		}
		if json.Unmarshal([]byte(run.Metrics), &res) == nil && res.Skipped == "" && res.Points > 0 {
			u := shortfallToUncertainty(res.PeakUnder)
			return &u, decision.UncertaintyBacktest, res.PeakUnder, nil
		}
	}
	return nil, decision.UncertaintyHeadroom, 0, nil
}

// estimateCapacity derives the pod capacity from the stored rps, replicas and cpu history.
func (s *Service) estimateCapacity(ctx context.Context, m *model.ForecastModule, bs boundSet, now time.Time) (CapacityDetail, error) {
	d := CapacityDetail{Source: decision.CapacityNone}
	load := func(kind string) (map[int64]float64, error) {
		ref := bs.firstOfKind(kind)
		if ref == nil {
			return nil, fmt.Errorf("no algorithm instance bound to the module has a %s metric source", kind)
		}
		series, err := s.Samples.Series(ctx, ref.Inst.ID, ref.Src.Key)
		if err != nil {
			return nil, fmt.Errorf("no %s history collected yet", kind)
		}
		rows, err := s.Samples.Range(ctx, series.ID, now.Add(-capacityLookback), now)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]float64, len(rows))
		for _, r := range rows {
			out[r.TS] = r.Value
		}
		return out, nil
	}
	rps, err := load("rps")
	if err != nil {
		return d, err
	}
	replicas, err := load("replicas")
	if err != nil {
		return d, err
	}
	cpu, err := load("cpu")
	if err != nil {
		return d, err
	}
	var samples []decision.Sample
	for ts, r := range rps {
		n, okN := replicas[ts]
		c, okC := cpu[ts]
		if okN && okC {
			samples = append(samples, decision.Sample{TS: time.Unix(ts, 0).UTC(), RPS: r, Replicas: n, CPUCores: c})
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TS.Before(samples[j].TS) })
	est, err := decision.EstimateCapacity(samples, float64(m.PodCPURequestMilli)/1000, m.TargetUtilization)
	d.Samples, d.Used = est.Samples, est.Used
	if err != nil {
		d.Note = err.Error()
		return d, err
	}
	d.Source, d.RPSPerPod, d.Note = decision.CapacityEstimated, est.RPSPerPod, est.Basis
	return d, nil
}

// currentReplicas is the replica count the operator last reported, when that report is recent.
func (s *Service) currentReplicas(ctx context.Context, moduleID uint, now time.Time) int {
	list, _, err := s.Predictions.ListExecutions(ctx, moduleID, "", 1, 1)
	if err != nil || len(list) == 0 || now.Sub(list[0].CreatedAt) > executionFreshness {
		return 0
	}
	return list[0].Current
}

// workloadLevel reports whether the mapping of a metric source returns the workload total, which the cpu and memory
// constraints need (a per-container series would understate the demand of many pods).
func (s *Service) workloadLevel(ctx context.Context, a *model.AlgorithmRun, metric string) bool {
	id := a.MappingID(metric)
	if id == 0 {
		return false
	}
	mapping, err := s.Mappings.GetByID(ctx, id)
	return err == nil && mapping.Level == "workload"
}

// allWorkloadLevel reports whether every source in the list returns the workload total.
func (s *Service) allWorkloadLevel(ctx context.Context, a *model.AlgorithmRun, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if !s.workloadLevel(ctx, a, key) {
			return false
		}
	}
	return true
}

// solverSeries turns the multi-metric calculation of each usable instance into the series the replica solver takes:
// per resource, the combined demand of the instance over the horizon, with the measured uncertainty of the algorithm
// on the source behind it. Resources whose sources are not workload totals cannot give a replica count and are left out.
//
// cache keeps the measured uncertainty of an algorithm between calls, so the look-ahead windows of one decision do not measure it
// again for each window; nil measures every time.
func (s *Service) solverSeries(ctx context.Context, m *model.ForecastModule, calcs []instanceCalc, now time.Time, cache uncertaintyCache) (decision.ReplicaInputs, []string) {
	var in decision.ReplicaInputs
	var notes []string
	noted := map[string]bool{}
	for _, c := range calcs {
		algo := c.st.inst.Key()
		for _, spec := range []struct {
			resource string
			dst      *[]decision.SeriesCandidate
			total    bool
		}{
			{model.ResourceReplicas, &in.RPS, false},
			{model.ResourceCPU, &in.CPU, true},
			{model.ResourceMemory, &in.Memory, true},
		} {
			rv, ok := c.res.Resources[spec.resource]
			if !ok || len(rv.Sources) == 0 {
				continue
			}
			if spec.total && !s.allWorkloadLevel(ctx, c.st.inst, rv.Sources) {
				if !noted[spec.resource] {
					noted[spec.resource] = true
					notes = append(notes, fmt.Sprintf("the %s forecast is not used for the replica count: its mappings must be at workload level to give the total demand of all pods", spec.resource))
				}
				continue
			}
			steps := 0
			for _, key := range rv.Sources {
				if n := len(c.values[key]); n > steps {
					steps = n
				}
			}
			if steps < 1 {
				steps = 1
			}
			values := make([]float64, steps)
			for i := range values {
				values[i] = rv.Value
			}
			sc := decision.SeriesCandidate{Algorithm: algo, Values: values}
			if m.ReplicaMode == model.ReplicaModeDynamic {
				if u, source, raw, err := s.cachedUncertainty(ctx, cache, c.st.inst.ID, rv.Sources[0], algo, now); err == nil {
					sc.Uncertainty, sc.UncertaintySource = u, source
					if note := uncertaintyCapNote(algo, raw); note != "" && !noted["cap:"+algo+spec.resource] {
						noted["cap:"+algo+spec.resource] = true
						notes = append(notes, note)
					}
				}
			}
			*spec.dst = append(*spec.dst, sc)
		}
	}
	return in, notes
}

// uncertaintyCache holds measured uncertainties by instance, source and algorithm.
type uncertaintyCache map[string]uncertaintyResult

type uncertaintyResult struct {
	u      *float64
	source string
	raw    float64
	err    error
}

func (s *Service) cachedUncertainty(ctx context.Context, cache uncertaintyCache, instanceID uint, metric, algorithm string, now time.Time) (*float64, string, float64, error) {
	if cache == nil {
		return s.uncertainty(ctx, instanceID, metric, algorithm, now)
	}
	key := fmt.Sprintf("%d/%s/%s", instanceID, metric, algorithm)
	if r, ok := cache[key]; ok {
		return r.u, r.source, r.raw, r.err
	}
	u, source, raw, err := s.uncertainty(ctx, instanceID, metric, algorithm, now)
	cache[key] = uncertaintyResult{u, source, raw, err}
	return u, source, raw, err
}

// solveReplicas runs the dynamic replica solver over the eligible series. Nothing here changes stored state. It also returns the
// solver configuration it used (with the capacity), which the look-ahead windows are solved with.
func (s *Service) solveReplicas(ctx context.Context, m *model.ForecastModule, bs boundSet, in decision.ReplicaInputs, notes []string, now time.Time, heap HeapPressure,
	pressure int, pressureWhy, pressureAlgorithm string, current, ceiling int) (*ReplicaDetail, []decision.ReplicaPlan, decision.SolverConfig, error) {
	detail := &ReplicaDetail{Mode: model.ReplicaModeDynamic, Capacity: CapacityDetail{Source: decision.CapacityNone}, Plans: []decision.ReplicaPlan{}, Notes: notes}
	cfg := decision.SolverConfig{
		PodCPUCores: float64(m.PodCPURequestMilli) / 1000, PodMemoryBytes: float64(m.PodMemoryRequestBytes),
		TargetUtilization: m.TargetUtilization, Headroom: m.HeadroomRatio,
		Bounds:           decision.Bounds{MinReplicas: m.MinReplicas, MaxReplicas: ceiling},
		CurrentReplicas:  current,
		PressureReplicas: pressure, PressureAlgorithm: pressureAlgorithm, PressureReason: pressureWhy, PressureSteps: 3,
	}
	detail.CurrentReplicas = cfg.CurrentReplicas
	if heap.Over {
		cfg.HeapPressure, cfg.HeapThreshold = heap.Ratio, heap.Threshold
		detail.Notes = append(detail.Notes, "JVM heap pressure: "+heap.Reason)
	}

	switch {
	case m.CapacityMode == model.CapacityModeEstimate:
		capacity, err := s.estimateCapacity(ctx, m, bs, now)
		detail.Capacity = capacity
		if err != nil {
			detail.Notes = append(detail.Notes, "pod capacity could not be estimated, so no throughput constraint was produced: "+err.Error())
		} else {
			cfg.CapacityRPS, cfg.CapacitySource = capacity.RPSPerPod, decision.CapacityEstimated
		}
	case m.PodCapacityRPS > 0:
		cfg.CapacityRPS, cfg.CapacitySource = m.PodCapacityRPS, decision.CapacityManual
		detail.Capacity = CapacityDetail{Source: decision.CapacityManual, RPSPerPod: m.PodCapacityRPS, Note: "set by the administrator"}
	default:
		detail.Notes = append(detail.Notes, "no pod capacity is configured, so no throughput constraint was produced")
	}
	cfg.CapacitySource = detail.Capacity.Source

	plans := decision.SolveReplicas(cfg, in)
	detail.Plans = plans
	if len(plans) > 0 {
		detail.Selected = plans[0].Algorithm
	}
	return detail, plans, cfg, nil
}
