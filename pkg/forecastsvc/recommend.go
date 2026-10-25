package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/multimetric"
)

// Considered records what happened to one algorithm instance on one source while a decision was made, eligible or not.
type Considered struct {
	Metric         string    `json:"metric"`
	AlgorithmID    string    `json:"algorithm_id"`
	Eligible       bool      `json:"eligible"`
	Reason         string    `json:"reason"`
	PredictionTime time.Time `json:"prediction_time,omitempty"`
	Peak           float64   `json:"peak,omitempty"`
}

// SelectionRow is one algorithm instance in the ranking behind a recommendation.
type SelectionRow struct {
	Algorithm string  `json:"algorithm"`
	Catalog   string  `json:"catalog"`
	Score     float64 `json:"score"`
	Scored    bool    `json:"scored"`
	Weight    float64 `json:"weight"`
	// Value is Score x Weight, what the ranking compares.
	Value    float64 `json:"value"`
	Rank     int     `json:"rank"`
	Eligible bool    `json:"eligible"`
	Reason   string  `json:"reason"`
	Chosen   bool    `json:"chosen"`
}

// ExpansionDetail says what the dynamic replica ceiling did.
type ExpansionDetail struct {
	Mode      string `json:"mode"`
	Base      int    `json:"base"`      // the module maximum
	Hard      int    `json:"hard"`      // the safety ceiling nothing may exceed
	Wanted    int    `json:"wanted"`    // what the forecast asked for before the ceiling
	Effective int    `json:"effective"` // the ceiling in force
	Expanded  bool   `json:"expanded"`
	Reason    string `json:"reason"`
}

// RecommendDetail is stored with every recommendation so it can be explained and replayed.
type RecommendDetail struct {
	Bypass        bool                            `json:"bypass"`
	BypassReason  string                          `json:"bypass_reason,omitempty"`
	CPU           decision.Field                  `json:"cpu"`
	Memory        decision.Field                  `json:"memory"`
	Replicas      decision.Field                  `json:"replicas"`
	GPU           decision.Field                  `json:"gpu"`
	Considered    []Considered                    `json:"considered"`
	ActivePeriods []string                        `json:"active_periods"`
	Policy        map[string]any                  `json:"policy"`
	Inputs        map[string][]decision.Candidate `json:"inputs"`
	// Selection ranks the algorithm instances; Algorithm and Score are the one that decided, Multi its calculation
	// source by source (the figures behind every recommended value).
	Selection []SelectionRow      `json:"selection,omitempty"`
	Algorithm string              `json:"algorithm,omitempty"`
	Score     float64             `json:"score,omitempty"`
	Multi     *multimetric.Result `json:"multi,omitempty"`
	Expansion *ExpansionDetail    `json:"expansion,omitempty"`
	// Replica explains the dynamic replica solve; nil in fixed mode.
	Replica *ReplicaDetail `json:"replica,omitempty"`
	// Degraded maps a field that fell back (module fallback or the original value) to the reason code, and
	// DegradeNotes to the sentence behind it. Empty when every field came from a usable forecast.
	Degraded     map[string]string `json:"degraded,omitempty"`
	DegradeNotes map[string]string `json:"degrade_notes,omitempty"`
	// OOM and Heap say what the OOM protection and the JVM heap rule saw, whether or not they acted.
	OOM  *OOMFloor     `json:"oom,omitempty"`
	Heap *HeapPressure `json:"heap,omitempty"`
}

// ErrObserveOnly is returned when a recommendation is requested for a module in observe mode.
var ErrObserveOnly = errors.New("module is in observe mode and produces no recommendations")

func toPeriod(p *model.ModulePeriod) decision.FixedPeriod {
	return decision.FixedPeriod{
		Name: p.Name, Priority: p.Priority, Weekdays: uint8(p.Weekdays), StartMinute: p.StartMinute, EndMinute: p.EndMinute,
		AbsStart: p.AbsStart, AbsEnd: p.AbsEnd, CPUMilli: p.CPUMilli, MemoryBytes: p.MemoryBytes, Replicas: p.Replicas,
	}
}

// sourceForecast is what one instance offers for one source.
type sourceForecast struct {
	values []float64
	reason string // why it is unusable; empty when usable
}

func (f sourceForecast) usable() bool { return f.reason == "" }

// instanceState is one algorithm after the eligibility checks.
type instanceState struct {
	inst     *model.AlgorithmRun
	bm       *boundModel
	forecast map[string]sourceForecast // by source key
	score    float64
	scored   bool
	value    float64
	eligible bool
	reason   string
}

func (st *instanceState) hasUsableForecast() bool {
	for _, f := range st.forecast {
		if f.usable() {
			return true
		}
	}
	return false
}

// instanceCalc is the multi-metric calculation of one usable instance.
type instanceCalc struct {
	st  *instanceState
	res multimetric.Result
	// values are the values each source was calculated from; their count is how many forecast steps it rests on.
	values map[string][]float64
}

// weight is what the algorithm's config multiplies its score by when the module ranks the algorithms.
func (st *instanceState) weight() float64 {
	if st.inst.Weight <= 0 {
		return 1
	}
	return st.inst.Weight
}

// evalInstance checks what an instance offers for each of its forecast-basis sources. A forecast is usable when the
// instance is a baseline or its latest backtest on that source was promoted for the same parameters and version, and
// its newest forecast is still fresh (made within the TTL) and covers a step in the future.
func (s *Service) evalInstance(ctx context.Context, inst *model.AlgorithmRun, now time.Time, ttl time.Duration) (*instanceState, []Considered, error) {
	st := &instanceState{inst: inst, forecast: map[string]sourceForecast{}}
	var considered []Considered
	bm, err := bind(inst)
	if err != nil {
		st.reason = "invalid instance: " + err.Error()
		return st, []Considered{{AlgorithmID: inst.Key(), Reason: st.reason}}, nil
	}
	st.bm = bm
	for _, key := range inst.SourceKeys() {
		c := Considered{Metric: key, AlgorithmID: inst.Key()}
		fail := func(reason string) {
			c.Reason = reason
			considered = append(considered, c)
			st.forecast[key] = sourceForecast{reason: reason}
		}
		if !bm.def.Baseline {
			run, err := s.Predictions.LatestBacktest(ctx, inst.ID, key, inst.Key())
			if err != nil {
				return nil, nil, err
			}
			switch {
			case run == nil:
				fail("not eligible: never backtested")
				continue
			case !run.Promoted:
				fail("not eligible: " + run.Reason)
				continue
			case run.Params != bm.params || run.ModelVersion != bm.version:
				fail("not eligible: parameters or version changed since the promoting backtest")
				continue
			}
		}
		rows, err := s.Predictions.LatestRun(ctx, inst.ID, key, inst.Key())
		if err != nil {
			return nil, nil, err
		}
		if len(rows) == 0 {
			fail("no prediction yet")
			continue
		}
		c.PredictionTime = rows[0].PredictionTime
		if age := now.Sub(rows[0].PredictionTime); age > ttl {
			fail(fmt.Sprintf("stale prediction made %s ago (ttl %s)", age.Round(time.Second), ttl))
			continue
		}
		peak, any := math.Inf(-1), false
		var values []float64
		for _, r := range rows {
			if r.TargetTime.Before(now) { // only the part of the horizon that is still ahead matters
				continue
			}
			peak, any = math.Max(peak, r.Value), true
			values = append(values, r.Value)
		}
		if !any {
			fail("prediction horizon has already passed")
			continue
		}
		c.Eligible, c.Peak, c.Reason = true, peak, "eligible"
		considered = append(considered, c)
		st.forecast[key] = sourceForecast{values: values}
	}
	return st, considered, nil
}

// historyValues are the measured values of a history-basis source over its window.
func (s *Service) historyValues(ctx context.Context, inst *model.AlgorithmRun, src *model.MetricSource, now time.Time) ([]float64, string) {
	series, err := s.Samples.Series(ctx, inst.ID, src.Key)
	if err != nil {
		return nil, "no collected history"
	}
	hours := src.WindowHours
	if hours <= 0 {
		hours = 24
	}
	rows, err := s.Samples.Range(ctx, series.ID, now.Add(-time.Duration(hours)*time.Hour), now)
	if err != nil {
		return nil, err.Error()
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("no samples in the last %d hours", hours)
	}
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = r.Value
	}
	return out, ""
}

// rankInstances orders the eligible instances by score x weight and applies the switching rule: the instance in use
// keeps its place until another has led it by more than SwitchMargin for SwitchCycles evaluations in a row.
func rankInstances(m *model.ForecastModule, states []*instanceState, run *model.ModuleRunState) (ordered []*instanceState, selected, challenger string, cycles int) {
	var eligible []*instanceState
	for _, st := range states {
		if st.eligible {
			eligible = append(eligible, st)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].value != eligible[j].value {
			return eligible[i].value > eligible[j].value
		}
		return eligible[i].inst.Key() < eligible[j].inst.Key()
	})
	if len(eligible) == 0 {
		return nil, "", "", 0
	}
	top := eligible[0]
	selected = top.inst.Key()
	if run == nil || run.Selected == "" || run.Selected == top.inst.Key() {
		return eligible, selected, "", 0
	}
	for i, st := range eligible {
		if st.inst.Key() != run.Selected {
			continue
		}
		// the instance in use is still eligible: the leader must win by the margin, repeatedly
		if top.value-st.value <= model.DefaultSwitchMargin {
			break
		}
		challenger, cycles = top.inst.Key(), 1
		if run.Challenger == challenger {
			cycles = run.ChallengerCycles + 1
		}
		if cycles >= model.DefaultSwitchCycles {
			return eligible, selected, "", 0
		}
		rest := append([]*instanceState{st}, append(append([]*instanceState{}, eligible[:i]...), eligible[i+1:]...)...)
		return rest, st.inst.Key(), challenger, cycles
	}
	return eligible, selected, "", 0
}

// Recommend decides the module's resources and replicas from fresh, eligible predictions and stores a new
// recommendation version. It does not apply anything.
func (s *Service) Recommend(ctx context.Context, moduleID uint) (*model.Recommendation, *RecommendDetail, error) {
	return s.recommend(ctx, moduleID, nil, false)
}

// recommend is Recommend with what the same pipeline cycle learned: failed maps a source whose algorithms failed in
// this cycle to why, so the fallback reason is the real one rather than "no forecast". advance records the choice
// of the algorithm in the run state (the scheduled pipeline does; a manual request only looks).
func (s *Service) recommend(ctx context.Context, moduleID uint, failed map[string]string, advance bool) (*model.Recommendation, *RecommendDetail, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, nil, err
	}
	if !m.Enabled {
		return nil, nil, fmt.Errorf("%w: module is disabled", ErrNotConfigured)
	}
	if m.Mode == model.ModeObserve {
		return nil, nil, ErrObserveOnly
	}
	now := s.now()
	ttl := time.Duration(m.TTLSeconds) * time.Second
	loc, err := time.LoadLocation(m.Timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("timezone %q: %w", m.Timezone, err)
	}
	ceiling := m.MaxReplicas
	if m.MaxReplicasMode == model.MaxReplicasDynamic && m.HardMaxReplicas > 0 {
		ceiling = m.HardMaxReplicas // the decision is bounded by the safety ceiling; the effective one is applied below
	}
	in := decision.Inputs{
		Now: now, Bypass: m.Bypass, BypassReason: m.BypassReason,
		Policy: decision.Policy{
			Bounds: decision.Bounds{MinCPUMilli: m.MinCPUMilli, MaxCPUMilli: m.MaxCPUMilli, MinMemoryBytes: m.MinMemoryBytes,
				MaxMemoryBytes: m.MaxMemoryBytes, MinReplicas: m.MinReplicas, MaxReplicas: ceiling, MinGPU: m.MinGPU, MaxGPU: m.MaxGPU},
			Fallback:      decision.Fallback{CPUMilli: m.FallbackCPUMilli, MemoryBytes: m.FallbackMemoryBytes, Replicas: m.FallbackReplicas, GPU: m.FallbackGPU},
			HeadroomRatio: m.HeadroomRatio, PodCapacityRPS: m.PodCapacityRPS, Location: loc, TTL: ttl,
		},
	}
	periods, err := s.Modules.Periods(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	detail := &RecommendDetail{Bypass: m.Bypass, BypassReason: m.BypassReason, ActivePeriods: []string{},
		Inputs: map[string][]decision.Candidate{}}
	for _, p := range periods {
		if !p.Enabled {
			continue
		}
		fp := toPeriod(p)
		in.Periods = append(in.Periods, fp)
		if decision.ValidatePeriod(fp) == nil && fp.Active(now, loc) {
			detail.ActivePeriods = append(detail.ActivePeriods, fp.Name)
		}
	}
	run, err := s.RunStates.RunState(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	held := heldFields(run, m)
	detail.Degraded, detail.DegradeNotes = map[string]string{}, map[string]string{}

	// ---- which algorithm instances can be used, and in what order
	bs, err := s.boundSet(ctx, m)
	if err != nil {
		return nil, nil, err
	}
	scores, err := s.Predictions.LatestScores(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	var states []*instanceState
	for _, b := range bs {
		st, considered, err := s.evalInstance(ctx, b, now, ttl)
		if err != nil {
			return nil, nil, err
		}
		detail.Considered = append(detail.Considered, considered...)
		if sc, ok := scores[b.Key()]; ok {
			st.score, st.scored = sc.Score, true
		}
		st.value = st.score * st.weight()
		switch {
		case st.bm == nil:
			// reason already set
		case st.hasUsableForecast():
			st.eligible, st.reason = true, "eligible"
			switch {
			case m.MinScore > 0 && !st.scored:
				st.eligible, st.reason = false, fmt.Sprintf("not scored yet; the module needs a score of at least %.0f", m.MinScore)
			case m.MinScore > 0 && st.score < m.MinScore:
				st.eligible, st.reason = false, fmt.Sprintf("score %.1f is below the module minimum %.0f", st.score, m.MinScore)
			}
		default:
			st.reason = "no usable forecast for any of its sources"
			for _, key := range b.SourceKeys() {
				if f, ok := st.forecast[key]; ok {
					st.reason = key + ": " + f.reason
					break
				}
			}
		}
		states = append(states, st)
	}
	ordered, selected, challenger, cycles := rankInstances(m, states, run)
	rank := map[string]int{}
	for i, st := range ordered {
		rank[st.inst.Key()] = i + 1
	}
	for _, st := range states {
		detail.Selection = append(detail.Selection, SelectionRow{Algorithm: st.inst.Key(), Catalog: st.inst.AlgorithmID, Score: st.score, Scored: st.scored,
			Weight: st.weight(), Value: st.value, Rank: rank[st.inst.Key()], Eligible: st.eligible, Reason: st.reason, Chosen: st.eligible && st.inst.Key() == selected})
	}
	sort.SliceStable(detail.Selection, func(i, j int) bool {
		a, b := detail.Selection[i], detail.Selection[j]
		if (a.Rank == 0) != (b.Rank == 0) {
			return a.Rank != 0
		}
		return a.Rank < b.Rank
	})
	if advance {
		run.Selected, run.Challenger, run.ChallengerCycles = selected, challenger, cycles
	}

	// ---- the multi-metric calculation of each usable instance, best first: each on the sources it owns
	cfg := multimetric.Config{}
	var calcs []instanceCalc
	for _, st := range ordered {
		values := map[string][]float64{}
		why := map[string]string{}
		for _, src := range st.inst.ActiveSources() {
			if src.Basis == model.BasisHistory {
				if v, reason := s.historyValues(ctx, st.inst, src, now); len(v) > 0 {
					values[src.Key] = v
				} else {
					why[src.Key] = reason
				}
				continue
			}
			if f, ok := st.forecast[src.Key]; ok && f.usable() {
				values[src.Key] = f.values
				continue
			}
			if f, ok := st.forecast[src.Key]; ok {
				why[src.Key] = f.reason
			} else {
				why[src.Key] = "this algorithm does not forecast the source"
			}
		}
		res := multimetric.Compute(cfg, calcInputs(st.inst, values, why))
		calcs = append(calcs, instanceCalc{st, res, values})
	}

	// the sources a resource would need, to tell "no forecast expected" from "a forecast failed"
	expected := map[string]bool{}
	for field, resource := range resourceOfField {
		if len(bs.forecastOfResource(resource)) > 0 {
			expected[field] = true
		}
	}
	current := s.currentReplicas(ctx, m.ID, now)
	if current == 0 {
		if rs := bs.firstOfKind("replicas"); rs != nil {
			if v, ok := s.latestValue(ctx, rs.Inst, rs.Src.Key); ok && v > 0 {
				current = int(math.Round(v))
			}
		}
	}
	for _, c := range calcs {
		algo := c.st.inst.Key()
		if v, ok := c.res.Resources[multimetric.ResourceCPU]; ok {
			in.CPUCores = append(in.CPUCores, decision.Candidate{Algorithm: algo, Peak: v.Value})
		}
		if v, ok := c.res.Resources[multimetric.ResourceMemory]; ok {
			in.MemoryBytes = append(in.MemoryBytes, decision.Candidate{Algorithm: algo, Peak: v.Value})
		}
		if v, ok := c.res.Resources[multimetric.ResourceGPU]; ok {
			in.GPU = append(in.GPU, decision.Candidate{Algorithm: algo, Peak: v.Value})
		}
		if v, ok := c.res.Resources[multimetric.ResourceReplicas]; ok {
			in.RPS = append(in.RPS, decision.Candidate{Algorithm: algo, Peak: v.Value})
		}
	}
	// a field whose recovery is still being held back gets no forecast candidates: it stays on the fallback chain
	for field := range held {
		switch field {
		case "cpu":
			in.CPUCores = nil
		case "memory":
			in.MemoryBytes = nil
		case "gpu":
			in.GPU = nil
		case "replicas":
			in.RPS = nil
		}
		if expected[field] {
			detail.Degraded[field] = DegradeRecovering
			detail.DegradeNotes[field] = fmt.Sprintf("the forecast is usable again; held back until %d healthy cycles in a row", m.RecoverCycles)
		}
	}
	detail.Inputs["cpu"], detail.Inputs["memory"], detail.Inputs["gpu"], detail.Inputs["replicas"] = in.CPUCores, in.MemoryBytes, in.GPU, in.RPS
	var top *instanceCalc
	if len(calcs) > 0 {
		top = &calcs[0]
		if !held["replicas"] && (top.res.Pressure > 0 || top.res.MaxRatio > 0) {
			if n, why := top.res.ReplicasFor(current); n > 0 {
				in.PressureReplicas, in.PressureReason = n, why
			}
		}
		detail.Algorithm, detail.Score = top.st.inst.Key(), top.st.score
		multi := top.res
		detail.Multi = &multi
	}

	oom, err := s.oomFloor(ctx, m, bs, now)
	if err != nil {
		return nil, nil, err
	}
	detail.OOM = &oom
	if oom.Active {
		in.Policy.OOMFloorBytes, in.Policy.OOMFloorReason = oom.FloorBytes, oom.Reason
	}
	heap, err := s.heapPressure(ctx, m, bs, now)
	if err != nil {
		return nil, nil, err
	}
	detail.Heap = &heap
	rec := decision.Decide(in)

	if m.HasReplicas() && m.ReplicaMode == model.ReplicaModeDynamic && top != nil && !held["replicas"] {
		series, notes := s.solverSeries(ctx, m, calcs, now)
		replica, plans, err := s.solveReplicas(ctx, m, bs, series, notes, now, heap, in.PressureReplicas, in.PressureReason, top.st.inst.Key(), current, ceiling)
		if err != nil {
			return nil, nil, err
		}
		detail.Replica = replica
		// bypass and an administrator's fixed period outrank any computed value
		if len(plans) > 0 && rec.Replicas.Source != decision.SourceBypass && rec.Replicas.Source != decision.SourceFixedPeriod {
			plan := plans[0]
			rec.Replicas = decision.Field{Set: true, Value: int64(plan.Replicas), Source: decision.SourceAlgorithm, Detail: plan.Algorithm,
				Reason: plan.Reason, Clamped: plan.Clamped}
		}
	}

	// ---- the replica ceiling: the module maximum, raised stepwise under dynamic expansion, never above the hard ceiling
	effectiveMax := m.MaxReplicas
	if !m.HasReplicas() {
		// a DaemonSet runs one pod per node: only its requests are adjusted
		rec.Replicas = decision.Field{Set: false, Source: decision.SourceOriginal, Reason: "a DaemonSet has no replica count to set"}
	} else if m.MaxReplicasMode == model.MaxReplicasDynamic {
		if rec.Replicas.Set && rec.Replicas.Source == decision.SourceAlgorithm && top != nil {
			exp := expandCeiling(m, run, rec.Replicas.Value, top.st)
			detail.Expansion, effectiveMax = exp, exp.Effective
			if rec.Replicas.Value > int64(exp.Effective) {
				rec.Replicas.Value, rec.Replicas.Clamped = int64(exp.Effective), true
				rec.Replicas.Reason += fmt.Sprintf("; capped at the effective maximum %d (%s)", exp.Effective, exp.Reason)
			} else if exp.Expanded {
				rec.Replicas.Reason += "; the ceiling was raised: " + exp.Reason
			}
		} else {
			detail.Expansion = &ExpansionDetail{Mode: m.MaxReplicasMode, Base: m.MaxReplicas, Hard: m.HardMaxReplicas, Effective: m.MaxReplicas,
				Reason: "no forecast-based replica count to expand for"}
			if rec.Replicas.Set && rec.Replicas.Source != decision.SourceBypass && rec.Replicas.Source != decision.SourceFixedPeriod &&
				m.MaxReplicas > 0 && rec.Replicas.Value > int64(m.MaxReplicas) {
				rec.Replicas.Value, rec.Replicas.Clamped = int64(m.MaxReplicas), true
			}
		}
	}
	if advance {
		run.EffectiveMaxReplicas = effectiveMax
		if err := s.RunStates.SaveRunState(ctx, run); err != nil {
			return nil, nil, err
		}
	}

	// a field that came from the fallback chain although a forecast was expected is a degradation, with the reason
	fields := map[string]decision.Field{"cpu": rec.CPUMilli, "memory": rec.MemoryBytes, "replicas": rec.Replicas, "gpu": rec.GPU}
	for field, f := range fields {
		if !expected[field] || (f.Source != decision.SourceFallback && f.Source != decision.SourceOriginal && f.Source != decision.SourceOOMFloor) {
			if f.Source == decision.SourceAlgorithm {
				delete(detail.Degraded, field)
			}
			continue
		}
		if _, held := detail.Degraded[field]; held {
			continue // already marked as recovering
		}
		code, note := s.fieldDegradeCode(ctx, bs, field, detail, failed, run, states)
		detail.Degraded[field], detail.DegradeNotes[field] = code, note
	}
	detail.CPU, detail.Memory, detail.Replicas, detail.GPU = rec.CPUMilli, rec.MemoryBytes, rec.Replicas, rec.GPU
	detail.Policy = map[string]any{"headroom_ratio": m.HeadroomRatio, "pod_capacity_rps": m.PodCapacityRPS, "ttl_seconds": m.TTLSeconds,
		"timezone": m.Timezone, "min_score": m.MinScore}

	row := &model.Recommendation{
		ModuleID: m.ID, Mode: m.Mode, ValidUntil: rec.ValidUntil,
		CPUSet: rec.CPUMilli.Set, CPUMilli: rec.CPUMilli.Value, CPUSource: string(rec.CPUMilli.Source),
		MemorySet: rec.MemoryBytes.Set, MemoryBytes: rec.MemoryBytes.Value, MemorySource: string(rec.MemoryBytes.Source),
		ReplicasSet: rec.Replicas.Set, Replicas: int(rec.Replicas.Value), ReplicasSource: string(rec.Replicas.Source),
		GPUSet: rec.GPU.Set, GPU: int(rec.GPU.Value), GPUSource: string(rec.GPU.Source),
		Algorithm: detail.Algorithm, Score: detail.Score, EffectiveMaxReplicas: effectiveMax,
		Detail: marshal(detail), Degraded: len(detail.Degraded) > 0, OOMFloor: rec.MemoryBytes.Floored,
	}
	if len(detail.Degraded) > 0 {
		row.DegradeReasons = marshal(detail.Degraded)
	}
	s.Telemetry.recordRecommend(row.Degraded, row.OOMFloor)
	for field, code := range detail.Degraded {
		s.Telemetry.recordFallback(code, field)
	}
	if row.OOMFloor {
		s.Telemetry.recordFallback("oom_floor", "memory")
	}
	if err := s.Predictions.SaveRecommendation(ctx, row); err != nil {
		// a concurrent run may have taken the version; one retry resolves it
		if err2 := s.Predictions.SaveRecommendation(ctx, row); err2 != nil {
			return nil, nil, err
		}
	}
	return row, detail, nil
}

// expandCeiling decides the replica ceiling under dynamic expansion. The forecast may raise it above the module
// maximum when the algorithm that produced the number scored well enough, by at most ExpandStepRatio of the ceiling
// already in force per cycle, and never above the hard ceiling. It comes straight back down when the forecast does.
func expandCeiling(m *model.ForecastModule, run *model.ModuleRunState, wanted int64, by *instanceState) *ExpansionDetail {
	exp := &ExpansionDetail{Mode: m.MaxReplicasMode, Base: m.MaxReplicas, Hard: m.HardMaxReplicas, Wanted: int(wanted), Effective: m.MaxReplicas}
	if wanted <= int64(m.MaxReplicas) {
		exp.Reason = "the forecast is within the module maximum"
		return exp
	}
	if m.MinScore > 0 && (by == nil || !by.scored || by.score < m.MinScore) {
		score := 0.0
		if by != nil {
			score = by.score
		}
		exp.Reason = fmt.Sprintf("the algorithm's score %.1f is below the %.0f needed to raise the ceiling", score, m.MinScore)
		return exp
	}
	base := m.MaxReplicas
	if run != nil && run.EffectiveMaxReplicas > base {
		base = run.EffectiveMaxReplicas
	}
	allowed := int(math.Ceil(float64(base) * (1 + m.ExpandStepRatio)))
	if allowed <= base {
		allowed = base + 1
	}
	if m.HardMaxReplicas > 0 && allowed > m.HardMaxReplicas {
		allowed = m.HardMaxReplicas
	}
	eff := int(wanted)
	if eff > allowed {
		eff = allowed
	}
	if eff < m.MaxReplicas {
		eff = m.MaxReplicas
	}
	exp.Effective, exp.Expanded = eff, eff > m.MaxReplicas
	exp.Reason = fmt.Sprintf("the forecast wants %d; the step limit of %.0f%% on %d allows %d; the hard ceiling is %d", wanted, m.ExpandStepRatio*100, base, allowed, m.HardMaxReplicas)
	return exp
}

// PipelineResult is what one pipeline run did and what went wrong. Each step reports its own error; a failing step
// does not stop the following ones that can still work from stored data.
type PipelineResult struct {
	Collect   []CollectResult       `json:"collect,omitempty"`
	Predict   []PredictOutcome      `json:"predict,omitempty"`
	Recommend *model.Recommendation `json:"recommend,omitempty"`
	Errors    []string              `json:"errors,omitempty"`
}

// RunData performs the scheduled data work of one module: collect the history of its sources, then run every
// enabled algorithm on it. It reports the algorithm forecasts that failed in failed, keyed by algorithm and source.
func (s *Service) RunData(ctx context.Context, moduleID uint, failed map[string]string) (*PipelineResult, error) {
	a, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	res := &PipelineResult{}
	if collected, err := s.Collect(ctx, a.ID); err != nil {
		res.Errors = append(res.Errors, a.Name+" collect: "+err.Error())
	} else {
		res.Collect = collected
		for _, c := range collected {
			if c.Error != "" {
				res.Errors = append(res.Errors, fmt.Sprintf("%s collect %s: %s", a.Name, c.Metric, c.Error))
			}
		}
	}
	if predicted, err := s.Predict(ctx, a.ID); err != nil {
		res.Errors = append(res.Errors, a.Name+" predict: "+err.Error())
	} else {
		res.Predict = predicted
		for _, p := range predicted {
			if p.Status == StatusFailed {
				res.Errors = append(res.Errors, fmt.Sprintf("%s predict %s %s: %s", a.Name, p.AlgorithmID, p.Metric, p.Reason))
				if failed != nil {
					failed[pairKey(p.AlgorithmID, p.Metric)] = p.Reason
				}
			}
		}
	}
	return res, nil
}

// RunModule performs the scheduled work of one module once its data has run: outside observe mode, decide a
// recommendation, then fold the cycle into the run state. failed says which algorithm forecasts failed in this cycle.
func (s *Service) RunModule(ctx context.Context, moduleID uint, failed map[string]string, cycleErrors []string) (*PipelineResult, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	res := &PipelineResult{Errors: append([]string(nil), cycleErrors...)}
	health := cycleHealth{Errors: res.Errors}
	if m.Mode != model.ModeObserve {
		if rec, detail, err := s.recommend(ctx, m.ID, failed, true); err != nil {
			res.Errors = append(res.Errors, "recommend: "+err.Error())
			health.Errors = res.Errors
		} else {
			res.Recommend = rec
			health.Degraded = detail.Degraded
		}
	}
	s.updateRunState(ctx, m.ID, s.now(), health)
	return res, nil
}

// RunPipeline runs a module end to end: its data and every algorithm, then the module itself.
func (s *Service) RunPipeline(ctx context.Context, moduleID uint) (*PipelineResult, error) {
	failed := map[string]string{}
	data, err := s.RunData(ctx, moduleID, failed)
	if err != nil {
		return nil, err
	}
	out := &PipelineResult{Collect: data.Collect, Predict: data.Predict, Errors: data.Errors}
	mod, err := s.RunModule(ctx, moduleID, failed, out.Errors)
	if err != nil {
		return nil, err
	}
	out.Recommend, out.Errors = mod.Recommend, mod.Errors
	return out, nil
}

// resourceOfField maps a recommendation field to the resource that drives it.
var resourceOfField = map[string]string{"cpu": model.ResourceCPU, "memory": model.ResourceMemory, "gpu": model.ResourceGPU, "replicas": model.ResourceReplicas}

// latestValue is the newest stored sample of a source.
func (s *Service) latestValue(ctx context.Context, a *model.AlgorithmRun, key string) (float64, bool) {
	series, err := s.Samples.Series(ctx, a.ID, key)
	if err != nil {
		return 0, false
	}
	at, ok, err := s.Samples.Latest(ctx, series.ID)
	if err != nil || !ok {
		return 0, false
	}
	rows, err := s.Samples.Range(ctx, series.ID, at, at)
	if err != nil || len(rows) == 0 {
		return 0, false
	}
	return rows[len(rows)-1].Value, true
}
