package decision

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// Constraint names of the replica solver.
const (
	ConstraintThroughput = "throughput"
	ConstraintCPU        = "cpu"
	ConstraintMemory     = "memory"
	ConstraintJVMHeap    = "jvm_heap"
	ConstraintPressure   = "pressure"
	ConstraintBounds     = "bounds"
	ConstraintHold       = "hold"
)

// Capacity sources, shown next to every replica result.
const (
	CapacityManual    = "manual"    // an operator-verified value
	CapacityEstimated = "estimated" // derived from the stored history
	CapacityNone      = "none"      // unavailable: no throughput constraint is produced
)

// Uncertainty sources.
const (
	UncertaintyMeasured = "measured" // P90 of the recent measured peak shortfall of the algorithm
	UncertaintyBacktest = "backtest" // the algorithm's latest backtest peak shortfall
	UncertaintyHeadroom = "headroom" // no measurement exists: the module headroom ratio is used
)

// SolverConfig is the module's static input to the replica solver.
type SolverConfig struct {
	// PodCPUCores and PodMemoryBytes are the per-pod requests; zero disables that constraint.
	PodCPUCores    float64
	PodMemoryBytes float64
	// TargetUtilization is the share of a request the workload may use, in (0, 1]; zero means 1.
	TargetUtilization float64
	// CapacityRPS is the safe requests per second of one Ready pod; zero disables the throughput constraint.
	CapacityRPS    float64
	CapacitySource string
	// Headroom is the multiplier used where no measured uncertainty exists (>= 1).
	Headroom float64
	Bounds   Bounds
	// HeapPressure is the JVM heap used / heap max seen recently (0 = unknown) and HeapThreshold the ratio above
	// which the replica count is raised in proportion; a zero threshold disables the rule.
	HeapPressure  float64
	HeapThreshold float64
	// CurrentReplicas is the last known replica count (0 = unknown). A decline is only recommended once the
	// forecast confirms it for at least MinDownSteps steps.
	CurrentReplicas int
	MinDownSteps    int
	// PressureReplicas, when positive, is the replica count the weighted metric pressure asks for; it is one more
	// constraint beside throughput, CPU and memory. PressureAlgorithm names the algorithm whose forecast it came
	// from, and PressureReason says how it was derived.
	PressureReplicas  int
	PressureAlgorithm string
	PressureReason    string
	// PressureSteps is how many forecast steps the pressure rests on (it confirms a decline like a series does).
	PressureSteps int
}

// SeriesCandidate is one algorithm's forecast of one metric over the horizon, in metric units: workload
// total cores, total bytes, and requests per second.
type SeriesCandidate struct {
	Algorithm string
	Values    []float64
	// Uncertainty is the relative shortfall the forecast is expected to have at its peak (0.2 = the peak may
	// be 20% too low). Nil means nothing was measured.
	Uncertainty       *float64
	UncertaintySource string
}

// ReplicaInputs holds the eligible series per metric, in binding priority order.
type ReplicaInputs struct {
	RPS    []SeriesCandidate
	CPU    []SeriesCandidate
	Memory []SeriesCandidate
}

// ConstraintResult is the number of replicas one constraint asks for and how it was derived.
type ConstraintResult struct {
	Name              string  `json:"name"`
	Metric            string  `json:"metric,omitempty"`
	Needed            int     `json:"needed"`
	PeakDemand        float64 `json:"peak_demand"`
	UnitCapacity      float64 `json:"unit_capacity"`
	Uncertainty       float64 `json:"uncertainty"`
	UncertaintySource string  `json:"uncertainty_source"`
	Algorithm         string  `json:"algorithm"`
	Borrowed          bool    `json:"borrowed,omitempty"`
	Formula           string  `json:"formula"`
}

// ReplicaPlan is the outcome for one algorithm.
type ReplicaPlan struct {
	Algorithm string `json:"algorithm"`
	Replicas  int    `json:"replicas"`
	// Binding is the constraint that decided: the largest of the constraints, or bounds / hold.
	Binding string `json:"binding"`
	// Needed is what the constraints ask for before the module's minimum and maximum are applied (and before a decline is held at the current
	// count): Replicas is what the plan recommends after them.
	Needed         int                `json:"needed"`
	Constraints    []ConstraintResult `json:"constraints"`
	Skipped        []string           `json:"skipped,omitempty"`
	CapacitySource string             `json:"capacity_source"`
	Clamped        bool               `json:"clamped,omitempty"`
	Reason         string             `json:"reason"`
}

func peakOf(values []float64) (float64, bool) {
	peak, ok := 0.0, false
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v > maxValue {
			return 0, false
		}
		if v < 0 {
			v = 0
		}
		if !ok || v > peak {
			peak, ok = v, true
		}
	}
	return peak, ok
}

func factorOf(c SeriesCandidate, headroom float64) (factor, uncertainty float64, source string) {
	if headroom < 1 {
		headroom = 1
	}
	if c.Uncertainty == nil || math.IsNaN(*c.Uncertainty) || *c.Uncertainty < 0 {
		return headroom, headroom - 1, UncertaintyHeadroom
	}
	source = c.UncertaintySource
	if source == "" {
		source = UncertaintyMeasured
	}
	// Whatever algorithm the series came from and whoever measured it, the forecast is scaled by 2 at most: a bigger factor does not
	// make a forecast that misses peaks by more than half safe, it puts the replicas at the maximum (see the cap in forecastsvc).
	u := math.Min(*c.Uncertainty, MaxUncertainty)
	return 1 + u, u, source
}

// MaxUncertainty is the largest relative uncertainty a forecast is scaled by: 1 = twice the forecast.
const MaxUncertainty = 1.0

// SolveReplicas computes, for every algorithm that has an eligible series, how many replicas the forecast
// needs. For each constraint with a usable per-pod capacity it takes the peak of the forecast times
// (1 + uncertainty) and divides by the capacity at the target utilization; the plan is the largest of them,
// clipped to the bounds. Where an algorithm has no series for a metric, the first eligible series of that
// metric is used and marked as borrowed. Plans come back in binding priority order.
//
// Legacy compatibility: with only a manual capacity and no measured uncertainty the result is
// ceil(peak * headroom / capacity), the formula this solver replaces.
func SolveReplicas(cfg SolverConfig, in ReplicaInputs) []ReplicaPlan {
	var algorithms []string
	seen := map[string]bool{}
	for _, list := range [][]SeriesCandidate{in.RPS, in.CPU, in.Memory} {
		for _, c := range list {
			if !seen[c.Algorithm] {
				seen[c.Algorithm] = true
				algorithms = append(algorithms, c.Algorithm)
			}
		}
	}
	if len(algorithms) == 0 && cfg.PressureReplicas > 0 && cfg.PressureAlgorithm != "" {
		algorithms = append(algorithms, cfg.PressureAlgorithm)
	}
	plans := make([]ReplicaPlan, 0, len(algorithms))
	for _, algo := range algorithms {
		if plan, ok := solveFor(cfg, in, algo); ok {
			plans = append(plans, plan)
		}
	}
	return plans
}

func pick(list []SeriesCandidate, algo string) (SeriesCandidate, bool, bool) {
	for _, c := range list {
		if c.Algorithm == algo {
			return c, true, false
		}
	}
	if len(list) > 0 {
		return list[0], true, true
	}
	return SeriesCandidate{}, false, false
}

func solveFor(cfg SolverConfig, in ReplicaInputs, algo string) (ReplicaPlan, bool) {
	util := cfg.TargetUtilization
	if util <= 0 || util > 1 {
		util = 1
	}
	plan := ReplicaPlan{Algorithm: algo, CapacitySource: cfg.CapacitySource, Constraints: []ConstraintResult{}}
	if plan.CapacitySource == "" {
		plan.CapacitySource = CapacityNone
	}
	type spec struct {
		name, metric string
		list         []SeriesCandidate
		unit         float64
		unitText     string
	}
	specs := []spec{
		{ConstraintThroughput, "rps", in.RPS, cfg.CapacityRPS, "rps per pod"},
		{ConstraintCPU, "cpu", in.CPU, cfg.PodCPUCores * util, "cores per pod at target utilization"},
		{ConstraintMemory, "memory", in.Memory, cfg.PodMemoryBytes * util, "bytes per pod at target utilization"},
	}
	steps := 0
	for _, s := range specs {
		cand, ok, borrowed := pick(s.list, algo)
		if !ok {
			continue
		}
		if !(s.unit > 0) {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: no per-pod capacity is configured", s.name))
			continue
		}
		peak, ok := peakOf(cand.Values)
		if !ok {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: the %s forecast is empty or invalid", s.name, cand.Algorithm))
			continue
		}
		factor, u, source := factorOf(cand, cfg.Headroom)
		needed := int(math.Ceil(peak * factor / s.unit))
		if s.name == ConstraintThroughput && needed < 1 {
			needed = 1
		}
		plan.Constraints = append(plan.Constraints, ConstraintResult{
			Name: s.name, Metric: s.metric, Needed: needed, PeakDemand: peak, UnitCapacity: s.unit, Uncertainty: u,
			UncertaintySource: source, Algorithm: cand.Algorithm, Borrowed: borrowed,
			Formula: fmt.Sprintf("ceil(%.4g x (1 + %.3g) / %.4g %s)", peak, u, s.unit, s.unitText),
		})
		if steps == 0 || len(cand.Values) < steps {
			steps = len(cand.Values)
		}
	}
	if cfg.PressureReplicas > 0 && (cfg.PressureAlgorithm == "" || cfg.PressureAlgorithm == algo) {
		plan.Constraints = append(plan.Constraints, ConstraintResult{Name: ConstraintPressure, Metric: "pressure", Needed: cfg.PressureReplicas,
			UncertaintySource: UncertaintyHeadroom, Algorithm: algo, Formula: cfg.PressureReason})
		if steps == 0 || (cfg.PressureSteps > 0 && cfg.PressureSteps < steps) {
			steps = cfg.PressureSteps
		}
	}
	if len(plan.Constraints) == 0 {
		return plan, false
	}
	best := plan.Constraints[0]
	for _, c := range plan.Constraints[1:] {
		if c.Needed > best.Needed {
			best = c
		}
	}
	if cfg.HeapThreshold > 0 && cfg.HeapPressure > cfg.HeapThreshold {
		// the heap is under pressure with the pods we have: spread it over proportionally more of them
		base := best.Needed
		if cfg.CurrentReplicas > base {
			base = cfg.CurrentReplicas
		}
		needed := int(math.Ceil(float64(base) * cfg.HeapPressure / cfg.HeapThreshold))
		jvm := ConstraintResult{Name: ConstraintJVMHeap, Metric: "jvm_heap", Needed: needed, PeakDemand: cfg.HeapPressure, UnitCapacity: cfg.HeapThreshold,
			UncertaintySource: UncertaintyHeadroom, Algorithm: algo,
			Formula: fmt.Sprintf("ceil(%d x heap pressure %.3g / threshold %.3g)", base, cfg.HeapPressure, cfg.HeapThreshold)}
		plan.Constraints = append(plan.Constraints, jvm)
		if needed > best.Needed {
			best = jvm
		}
	}
	plan.Replicas, plan.Binding, plan.Needed = best.Needed, best.Name, best.Needed
	plan.Reason = fmt.Sprintf("%s constraint decides: %s = %d", best.Name, best.Formula, best.Needed)
	if lo := cfg.Bounds.MinReplicas; lo > 0 && plan.Replicas < lo {
		plan.Replicas, plan.Binding, plan.Clamped = lo, ConstraintBounds, true
		plan.Reason += fmt.Sprintf("; raised to the minimum %d", lo)
	}
	if hi := cfg.Bounds.MaxReplicas; hi > 0 && plan.Replicas > hi {
		plan.Replicas, plan.Binding, plan.Clamped = hi, ConstraintBounds, true
		plan.Reason += fmt.Sprintf("; capped at the maximum %d", hi)
	}
	// A decline is only recommended when the forecast confirms it for several steps; a short horizon cannot.
	minDown := cfg.MinDownSteps
	if minDown <= 0 {
		minDown = 3
	}
	if cur := cfg.CurrentReplicas; cur > 0 && plan.Replicas < cur && steps < minDown {
		plan.Reason += fmt.Sprintf("; held at the current %d: %d forecast steps are fewer than the %d needed to confirm a decline", cur, steps, minDown)
		plan.Replicas, plan.Binding = cur, ConstraintHold
	}
	return plan, true
}

// DefaultPercentile is the level the decisions start from; every use of it can be set by an administrator.
const DefaultPercentile = 0.9

// PercentName writes a level as the name people use: 0.9 is "P90", 0.995 is "P99.5".
func PercentName(q float64) string {
	return "P" + strconv.FormatFloat(math.Round(q*1000)/10, 'f', -1, 64)
}

// Percentile returns the q-quantile (0..1) of values by linear interpolation; it does not modify values.
func Percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	if q <= 0 {
		return s[0]
	}
	if q >= 1 {
		return s[len(s)-1]
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	frac := pos - float64(lo)
	if lo+1 >= len(s) {
		return s[lo]
	}
	return s[lo]*(1-frac) + s[lo+1]*frac
}

// Sample is one aligned observation for capacity estimation: workload requests per second, ready pods and the
// total CPU cores the workload used.
type Sample struct {
	TS       time.Time
	RPS      float64
	Replicas float64
	CPUCores float64
}

// CapacityEstimate is the outcome of EstimateCapacity.
type CapacityEstimate struct {
	RPSPerPod float64 `json:"rps_per_pod"`
	Samples   int     `json:"samples"`
	Used      int     `json:"used"`
	Basis     string  `json:"basis"`
}

// MinCapacitySamples is the least number of near-saturation samples an estimate may rest on.
const MinCapacitySamples = 30

// EstimateCapacity estimates the safe requests per second of one pod from history. It looks at moments when
// the pods were busy (CPU at least 70% of the target utilization of the requests), divides the load by the
// pod count to get the per-pod load, and scales it linearly to the target utilization:
// capacity = P50(rps / replicas x target / saturation). With too few busy moments it refuses rather than
// invent a number; a refusal says what is missing.
func EstimateCapacity(samples []Sample, podCPUCores, target float64) (CapacityEstimate, error) {
	if !(podCPUCores > 0) {
		return CapacityEstimate{}, fmt.Errorf("the per-pod CPU request is required to estimate capacity")
	}
	if target <= 0 || target > 1 {
		target = 1
	}
	est := CapacityEstimate{Samples: len(samples)}
	var perPod []float64
	for _, s := range samples {
		if !(s.Replicas >= 1) || !(s.RPS > 0) || !(s.CPUCores > 0) {
			continue
		}
		saturation := s.CPUCores / (s.Replicas * podCPUCores)
		if saturation < 0.7*target {
			continue
		}
		perPod = append(perPod, s.RPS/s.Replicas*target/saturation)
	}
	est.Used = len(perPod)
	if len(perPod) < MinCapacitySamples {
		return est, fmt.Errorf("only %d of %d samples were near saturation (CPU at least %.0f%% of the requests); %d are needed",
			len(perPod), len(samples), 70*target, MinCapacitySamples)
	}
	est.RPSPerPod = Percentile(perPod, 0.5)
	est.Basis = fmt.Sprintf("P50 of rps / replicas x target %.2g / saturation over %d busy samples", target, len(perPod))
	return est, nil
}
