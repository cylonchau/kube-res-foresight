package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/multimetric"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// System parameters of the scale-ahead decision.
const (
	// ConfigLeadPodReady is the seconds a Pod takes from the scale-up until it is Ready (image, start, probes); used until enough
	// scale-ups of the module were measured.
	ConfigLeadPodReady = "forecast.lead_pod_ready_seconds"
	// ConfigLeadNodeProvision is the seconds the cluster takes to add a node when the Pods do not fit; added to the configured time
	// only (a measured ready time already contains it).
	ConfigLeadNodeProvision = "forecast.lead_node_provision_seconds"
	// ConfigConsensusUp and ConfigConsensusDown are the quantiles of the credibility weight the replica counts of the algorithms must
	// cover for a scale-up and for a scale-down.
	ConfigConsensusUp   = "forecast.consensus_up_quantile"
	ConfigConsensusDown = "forecast.consensus_down_quantile"
	// ConfigEmergencyScaleUp lets the load measured now raise the replicas above the forecast.
	ConfigEmergencyScaleUp = "forecast.emergency_scale_up"
	// ConfigLeadCoverFactor is how many times the lead the forecast has to reach ahead: the lead window is looked at, and then as much
	// again, so a rise that starts when the replicas become Ready is seen too. 1 makes the forecast end where the lead ends.
	ConfigLeadCoverFactor = "forecast.lead_cover_factor"
)

// Defaults of the scale-ahead parameters.
const (
	DefaultLeadPodReady  = 60
	DefaultLeadNode      = 0
	DefaultConsensusUp   = 0.75
	DefaultConsensusDown = 0.9
	DefaultLeadCover     = 2.0
	operatorPollSeconds  = 30 // how often the operator asks for a decision
	outcomeHistory       = 20 // finished scale-ups the lead is learned from
	liveMaxAgeSteps      = 3  // a sample older than this many steps is not the load of now
)

type aheadSettings struct {
	PodReady, Node float64
	Up, Down       float64
	Emergency      bool
	DecisionWait   float64
	LeadCover      float64
	// LeadPct is the level (0..1) of the lead's ready times and lateness margin: the system parameter forecast.lead_percentile.
	LeadPct float64
}

// aheadSettings reads the parameters; a missing or invalid value is replaced by its default.
func (s *Service) aheadSettings(ctx context.Context) aheadSettings {
	out := aheadSettings{PodReady: DefaultLeadPodReady, Node: DefaultLeadNode, Up: DefaultConsensusUp, Down: DefaultConsensusDown, Emergency: true, LeadCover: DefaultLeadCover}
	out.DecisionWait = s.DecisionMaxAge(ctx).Seconds() + operatorPollSeconds
	out.LeadPct = s.Percentile(ctx, timing.LeadPercentile)
	if s.Config == nil {
		return out
	}
	num := func(key string, lo, hi float64, dst *float64) {
		v, err := s.Config.GetValueOrDefault(ctx, key, "")
		if err != nil || v == "" {
			return
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= lo && f <= hi {
			*dst = f
		}
	}
	num(ConfigLeadPodReady, 0, 3600, &out.PodReady)
	num(ConfigLeadNodeProvision, 0, 3600, &out.Node)
	num(ConfigConsensusUp, 0.5, 1, &out.Up)
	num(ConfigConsensusDown, 0.5, 1, &out.Down)
	num(ConfigLeadCoverFactor, 1, 6, &out.LeadCover)
	if b, err := s.Config.GetBool(ctx, ConfigEmergencyScaleUp, true); err == nil {
		out.Emergency = b
	}
	return out
}

// WindowNeed is the replica count one algorithm asks for in a window.
type WindowNeed struct {
	Algorithm string `json:"algorithm"`
	Replicas  int    `json:"replicas"`
}

// WindowDetail is what the algorithms ask for if only the first part of the forecast counts.
type WindowDetail struct {
	Seconds int `json:"seconds"`
	Steps   int `json:"steps"`
	// Lead marks the window of the lead; Horizon the whole forecast.
	Lead      bool         `json:"lead,omitempty"`
	Horizon   bool         `json:"horizon,omitempty"`
	Needs     []WindowNeed `json:"needs"`
	Consensus int          `json:"consensus"`
}

// LiveDetail is the load measured now and the replicas it needs.
type LiveDetail struct {
	OK       bool       `json:"ok"`
	Need     int        `json:"need"`
	Resource string     `json:"resource,omitempty"`
	Value    float64    `json:"value,omitempty"`
	At       *time.Time `json:"at,omitempty"`
	Note     string     `json:"note,omitempty"`
	// Resources is the same measurement for every resource that has one: Resource, Value, Need and At above are the one that needs the most
	// replicas, but the load to read against a forecast is the one of the same resource.
	Resources []LiveResource `json:"resources,omitempty"`
}

// LiveResource is the load of one resource measured now and the replicas it alone needs.
type LiveResource struct {
	Resource string     `json:"resource"`
	Value    float64    `json:"value"`
	Need     int        `json:"need"`
	At       *time.Time `json:"at,omitempty"`
}

// OutcomeSummary counts the finished scale-ups the lead was learned from.
type OutcomeSummary struct {
	Judged   int `json:"judged"`
	Timely   int `json:"timely"`
	Late     int `json:"late"`
	Unneeded int `json:"unneeded"`
	Open     int `json:"open"`
}

// ReachDetail says how far ahead the forecasts of the module reach and why: the forecast period it is configured with, and the longer
// reach the lead asks for.
type ReachDetail struct {
	ConfiguredMinutes int `json:"configured_minutes"`
	EffectiveMinutes  int `json:"effective_minutes"`
	// Extended is true when the configured forecast period is shorter than what the lead needs and the forecasts were made longer.
	Extended bool   `json:"extended"`
	Reason   string `json:"reason"`
}

// AheadDetail is stored with the recommendation: how far ahead it looked, what each algorithm asked for in each window, how they were
// brought to one number, what the load measured now said, and how the earlier scale-ups of the module went.
type AheadDetail struct {
	Reach      ReachDetail         `json:"reach"`
	Lead       decision.Lead       `json:"lead"`
	Windows    []WindowDetail      `json:"windows"`
	Up         decision.Consensus  `json:"up"`
	Down       decision.Consensus  `json:"down"`
	Live       LiveDetail          `json:"live"`
	Settlement decision.Settlement `json:"settlement"`
	Current    int                 `json:"current"`
	Outcomes   OutcomeSummary      `json:"outcomes"`
	Notes      []string            `json:"notes,omitempty"`
	// Leader is the algorithm the replica count is credited to.
	Leader string `json:"leader,omitempty"`
}

// readyAndLate are the measured ready times of the judged scale-ups and how late the late ones were.
func readyAndLate(recent []*model.ScaleOutcome) (ready, late []float64) {
	for _, o := range recent {
		if !o.Final {
			continue
		}
		if o.ReadySeconds > 0 {
			ready = append(ready, o.ReadySeconds)
		}
		if o.Verdict == decision.VerdictLate && o.LateSeconds > 0 {
			late = append(late, o.LateSeconds)
		}
	}
	return ready, late
}

// leadCoverMinutes is how far ahead the forecasts of a module have to reach: the lead (the time from a scale-up decision until the
// replicas take load, learned from the module's own scale-ups) times the cover factor, in whole minutes. 0 when there is no scale-up to
// plan for (a module that does not scale replicas) or the store cannot be read.
func (s *Service) leadCoverMinutes(ctx context.Context, m *model.ForecastModule) int {
	if !m.HasReplicas() || s.Predictions == nil {
		return 0
	}
	set := s.aheadSettings(ctx)
	recent, err := s.Predictions.RecentOutcomes(ctx, m.ID, outcomeHistory, true)
	if err != nil {
		return 0
	}
	ready, late := readyAndLate(recent)
	step := float64(m.StepSeconds)
	if step <= 0 {
		step = 60
	}
	lead := decision.ComputeLead(decision.LeadInputs{ReadySeconds: ready, ConfiguredReadySeconds: set.PodReady, NodeSeconds: set.Node, DecisionSeconds: set.DecisionWait,
		LateSeconds: late, Percentile: set.LeadPct, StepSeconds: step})
	cover := int(math.Ceil(lead.Seconds * set.LeadCover / 60))
	if cover > 24*60 {
		cover = 24 * 60 // the longest forecast period a module may have
	}
	return cover
}

// stepOf is the sampling step of the forecasts: the module's own, or the step of the first forecast source.
func (s *Service) stepOf(ctx context.Context, m *model.ForecastModule, calcs []instanceCalc) time.Duration {
	if m.StepSeconds > 0 {
		return time.Duration(m.StepSeconds) * time.Second
	}
	for _, c := range calcs {
		for _, key := range c.st.inst.SourceKeys() {
			if series, err := s.Samples.Series(ctx, c.st.inst.ID, key); err == nil && series.StepSeconds > 0 {
				return time.Duration(series.StepSeconds) * time.Second
			}
		}
	}
	return 0
}

// truncatedCalc is the calculation of an instance over the first n steps of its forecast; measured (history-basis) sources keep
// their window.
func truncatedCalc(c instanceCalc, n int) instanceCalc {
	values := make(map[string][]float64, len(c.values))
	for key, v := range c.values {
		if _, forecasted := c.st.forecast[key]; forecasted && len(v) > n {
			v = v[:n]
		}
		values[key] = v
	}
	res := multimetric.Compute(multimetric.Config{}, calcInputs(c.st.inst, values, c.why))
	return instanceCalc{st: c.st, res: res, values: values, why: c.why}
}

// windowNeeds is what every algorithm asks for if only the first n steps of its forecast counted: the replica solver run on the
// truncated forecasts. The scale-down confirmation is left out: whether a decline is confirmed is the business of the whole horizon.
func (s *Service) windowNeeds(ctx context.Context, m *model.ForecastModule, calcs []instanceCalc, n int, cfg decision.SolverConfig, now time.Time, cache uncertaintyCache) []decision.ReplicaPlan {
	sub := make([]instanceCalc, len(calcs))
	for i, c := range calcs {
		sub[i] = truncatedCalc(c, n)
	}
	in, _ := s.solverSeries(ctx, m, sub, now, cache)
	cfg.MinDownSteps = 1
	return decision.SolveReplicas(cfg, in)
}

// scaleAhead is the forward-looking decision. The algorithms all vote: each asks for the replicas the forecast of the look-ahead window
// of the lead needs (the capacity that must be Ready when the replicas asked for now start to take load), weighted by how far it is
// trusted. The votes are brought to one count with a credibility-weighted quantile, never the largest or the smallest. A scale-down
// needs the whole horizon to agree at a stricter quantile and the load measured now to fit; a load that the forecast did not
// see raises the count at once (the emergency). Nothing here writes; the caller stores the outcome of an upward decision.
func (s *Service) scaleAhead(ctx context.Context, m *model.ForecastModule, bs boundSet, states []*instanceState, calcs []instanceCalc, plans []decision.ReplicaPlan,
	cfg decision.SolverConfig, current, ceiling int, now time.Time, advance bool) (*AheadDetail, error) {
	set := s.aheadSettings(ctx)
	d := &AheadDetail{Current: current}
	step := s.stepOf(ctx, m, calcs)
	if step <= 0 {
		return nil, nil
	}
	if advance {
		if err := s.evaluateOutcomes(ctx, m, bs, cfg, now); err != nil {
			return nil, err
		}
	}
	recent, err := s.Predictions.RecentOutcomes(ctx, m.ID, outcomeHistory, false)
	if err != nil {
		return nil, err
	}
	ready, late := readyAndLate(recent)
	for _, o := range recent {
		switch {
		case !o.Final:
			d.Outcomes.Open++
			continue
		case o.Verdict == decision.VerdictTimely:
			d.Outcomes.Timely++
		case o.Verdict == decision.VerdictLate:
			d.Outcomes.Late++
		case o.Verdict == decision.VerdictUnneeded:
			d.Outcomes.Unneeded++
		}
		d.Outcomes.Judged++
	}
	horizonSteps := 0
	for _, c := range calcs {
		for key, v := range c.values {
			if _, forecasted := c.st.forecast[key]; forecasted && len(v) > horizonSteps {
				horizonSteps = len(v)
			}
		}
	}
	if horizonSteps == 0 {
		return nil, nil
	}
	horizon := time.Duration(horizonSteps) * step
	d.Lead = decision.ComputeLead(decision.LeadInputs{ReadySeconds: ready, ConfiguredReadySeconds: set.PodReady, NodeSeconds: set.Node, DecisionSeconds: set.DecisionWait,
		LateSeconds: late, Percentile: set.LeadPct, StepSeconds: step.Seconds(), HorizonSeconds: horizon.Seconds()})
	leadSteps := decision.StepsIn(time.Duration(d.Lead.Seconds)*time.Second, step, horizonSteps)
	d.Reach = ReachDetail{ConfiguredMinutes: m.HorizonMinutes, EffectiveMinutes: int(horizon / time.Minute)}
	d.Reach.Extended = d.Reach.EffectiveMinutes > m.HorizonMinutes
	d.Reach.Reason = fmt.Sprintf("the forecasts reach %d min ahead", d.Reach.EffectiveMinutes)
	if d.Reach.Extended {
		d.Reach.Reason = fmt.Sprintf("the forecast period of %d min is shorter than what the lead of %.0fs needs (it is looked at, and as much again): the forecasts reach %d min ahead",
			m.HorizonMinutes, d.Lead.Seconds, d.Reach.EffectiveMinutes)
	}

	credibility := map[string]float64{}
	for _, st := range states {
		credibility[st.inst.Key()] = decision.Credibility(st.score, st.scored, st.tracking.WAPE, st.tracked)
	}
	votesOf := func(plans []decision.ReplicaPlan) []decision.Vote {
		out := make([]decision.Vote, 0, len(plans))
		for _, p := range plans {
			out = append(out, decision.Vote{Algorithm: p.Algorithm, Replicas: p.Replicas, Credibility: credibility[p.Algorithm]})
		}
		return out
	}

	cache := uncertaintyCache{}
	var leadPlans []decision.ReplicaPlan
	seen := map[int]bool{}
	var windows []time.Duration
	for _, w := range decision.Windows(horizon) {
		windows = append(windows, w)
	}
	windows = append(windows, time.Duration(d.Lead.Seconds)*time.Second)
	sort.Slice(windows, func(i, j int) bool { return windows[i] < windows[j] })
	for _, w := range windows {
		n := decision.StepsIn(w, step, horizonSteps)
		if seen[n] {
			continue
		}
		seen[n] = true
		plans := s.windowNeeds(ctx, m, calcs, n, cfg, now, cache)
		wd := WindowDetail{Seconds: int(time.Duration(n) * step / time.Second), Steps: n, Lead: n == leadSteps, Horizon: n == horizonSteps, Needs: []WindowNeed{}}
		for _, p := range plans {
			wd.Needs = append(wd.Needs, WindowNeed{Algorithm: p.Algorithm, Replicas: p.Replicas})
		}
		if c, ok := decision.Reconcile(votesOf(plans), set.Up); ok {
			wd.Consensus = c.Replicas
		}
		if wd.Lead {
			leadPlans = plans
		}
		d.Windows = append(d.Windows, wd)
	}

	var haveUp, haveDown bool
	d.Up, haveUp = decision.Reconcile(votesOf(leadPlans), set.Up)
	d.Down, haveDown = decision.Reconcile(votesOf(plans), set.Down)

	live, liveNote := s.liveNeed(ctx, step, bs, cfg, now)
	d.Live = live
	if liveNote != "" {
		d.Notes = append(d.Notes, liveNote)
	}
	d.Settlement = decision.Settle(decision.SettleInputs{Current: current, Up: d.Up, HaveUp: haveUp, Down: d.Down, HaveDown: haveDown,
		Live: live.Need, LiveOK: live.OK, EmergencyOn: set.Emergency, Max: ceiling})
	if lo := m.MinReplicas; lo > 0 && d.Settlement.Replicas < lo {
		d.Settlement.Replicas = lo
	}
	if ceiling > 0 && d.Settlement.Replicas > ceiling {
		d.Settlement.Replicas = ceiling
	}
	switch {
	case d.Settlement.Emergency:
		d.Leader = "live"
	case d.Settlement.Action == decision.ActionUp:
		d.Leader = d.Up.Leader
	default:
		d.Leader = d.Down.Leader
	}
	return d, nil
}

// demandSource is a measured series of the workload with what one replica carries of it.
type demandSource struct {
	resource string
	ref      *sourceRef
	unit     float64
}

// demandSources are the measured workload totals that give a replica count: CPU and memory (when their mappings are workload level)
// and requests per second (when the capacity of a Pod is known).
func (s *Service) demandSources(ctx context.Context, bs boundSet, cfg decision.SolverConfig) []demandSource {
	util := cfg.TargetUtilization
	if util <= 0 || util > 1 {
		util = 1
	}
	var out []demandSource
	for _, spec := range []struct {
		kind  string
		unit  float64
		total bool
	}{
		{"cpu", cfg.PodCPUCores * util, true},
		{"memory", cfg.PodMemoryBytes * util, true},
		{"rps", cfg.CapacityRPS, false},
	} {
		if !(spec.unit > 0) {
			continue
		}
		ref := bs.firstOfKind(spec.kind)
		if ref == nil || (spec.total && !s.workloadLevel(ctx, ref.Inst, ref.Src.Key)) {
			continue
		}
		out = append(out, demandSource{resource: spec.kind, ref: ref, unit: spec.unit})
	}
	return out
}

// liveNeed is the replica count the load measured now needs: for each resource the lower of the last two samples (one sample above the
// rest is a blip; two in a row are a load), over the replicas one carries at the target utilization; the largest of the resources.
func (s *Service) liveNeed(ctx context.Context, step time.Duration, bs boundSet, cfg decision.SolverConfig, now time.Time) (LiveDetail, string) {
	d := LiveDetail{}
	sources := s.demandSources(ctx, bs, cfg)
	if len(sources) == 0 {
		return d, "the load measured now cannot be turned into replicas: no workload-level CPU, memory or request-rate series with a per-Pod capacity"
	}
	maxAge := liveMaxAgeSteps * step
	for _, src := range sources {
		series, err := s.Samples.Series(ctx, src.ref.Inst.ID, src.ref.Src.Key)
		if err != nil {
			continue
		}
		rows, err := s.Samples.Range(ctx, series.ID, now.Add(-maxAge), now)
		if err != nil || len(rows) == 0 {
			continue
		}
		last := rows[len(rows)-1]
		v := last.Value
		if len(rows) > 1 {
			v = math.Min(v, rows[len(rows)-2].Value)
		}
		if v < 0 || math.IsNaN(v) {
			continue
		}
		need := int(math.Ceil(v / src.unit))
		if need < 1 {
			need = 1
		}
		at := time.Unix(last.TS, 0).UTC()
		d.Resources = append(d.Resources, LiveResource{Resource: src.resource, Value: v, Need: need, At: &at})
		if !d.OK || need > d.Need {
			d.OK, d.Need, d.Resource, d.Value, d.At = true, need, src.resource, v, &at
		}
	}
	if !d.OK {
		return d, "no sample of the load is recent enough to say what it needs now"
	}
	return d, ""
}

// demandNeeds is the replica count the measured load needed at each moment of [from, to], the largest of the resources.
func (s *Service) demandNeeds(ctx context.Context, bs boundSet, cfg decision.SolverConfig, from, to time.Time) []decision.NeedPoint {
	byTS := map[int64]int{}
	for _, src := range s.demandSources(ctx, bs, cfg) {
		series, err := s.Samples.Series(ctx, src.ref.Inst.ID, src.ref.Src.Key)
		if err != nil {
			continue
		}
		rows, err := s.Samples.Range(ctx, series.ID, from, to)
		if err != nil {
			continue
		}
		for _, r := range rows {
			if r.Value < 0 || math.IsNaN(r.Value) {
				continue
			}
			need := int(math.Ceil(r.Value / src.unit))
			if need < 1 {
				need = 1
			}
			if need > byTS[r.TS] {
				byTS[r.TS] = need
			}
		}
	}
	out := make([]decision.NeedPoint, 0, len(byTS))
	for ts, need := range byTS {
		out = append(out, decision.NeedPoint{At: time.Unix(ts, 0).UTC(), Need: need})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// readyAt is the first moment, since from, that at least n replicas were Ready, from the ready-replicas series. ok is false when the
// module has no such series: then the ready time cannot be measured at all.
func (s *Service) readyAt(ctx context.Context, bs boundSet, from, to time.Time, n int) (at *time.Time, ok bool) {
	ref := bs.firstOfKind("replicas")
	if ref == nil {
		return nil, false
	}
	series, err := s.Samples.Series(ctx, ref.Inst.ID, ref.Src.Key)
	if err != nil {
		return nil, false
	}
	rows, err := s.Samples.Range(ctx, series.ID, from, to)
	if err != nil {
		return nil, false
	}
	for _, r := range rows {
		if r.Value >= float64(n)-0.5 {
			t := time.Unix(r.TS, 0).UTC()
			return &t, true
		}
	}
	return nil, true
}

// evaluateOutcomes judges the scale-ups of a module that are still open: when the replicas were Ready, what the load needed, and
// whether the one came before the other. The finished ones are where the next lead comes from.
func (s *Service) evaluateOutcomes(ctx context.Context, m *model.ForecastModule, bs boundSet, cfg decision.SolverConfig, now time.Time) error {
	open, err := s.Predictions.OpenOutcomes(ctx, m.ID)
	if err != nil || len(open) == 0 {
		return err
	}
	for _, o := range open {
		ready, measurable := s.readyAt(ctx, bs, o.DecidedAt, now, o.ToReplicas)
		if !measurable {
			o.Note = "the module has no ready-replicas series, so when the replicas were Ready cannot be measured"
			o.Final = true
			o.Verdict = decision.VerdictPending
			if err := s.Predictions.UpdateOutcome(ctx, o); err != nil {
				return err
			}
			continue
		}
		res := decision.EvaluateOutcome(decision.OutcomeInput{DecidedAt: o.DecidedAt, From: o.FromReplicas, To: o.ToReplicas, LeadSeconds: o.LeadSeconds,
			Needs: s.demandNeeds(ctx, bs, cfg, o.DecidedAt, now), ReadyAt: ready, Now: now})
		t := now
		o.Verdict, o.Final, o.EvaluatedAt, o.ActualNeed, o.NeededAt, o.ReadyAt = res.Verdict, res.Final, &t, res.ActualNeed, res.NeededAt, ready
		o.ReadySeconds, o.LateSeconds, o.SlackSeconds, o.Note = res.ReadySeconds, res.LateSeconds, res.SlackSeconds, res.Reason
		if err := s.Predictions.UpdateOutcome(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

// recordOutcome keeps a scale-up the platform decided, to be judged later. One is open at a time for the same replica count: the
// decision is repeated every poll while the replicas come up, and each repeat is the same scale-up.
func (s *Service) recordOutcome(ctx context.Context, m *model.ForecastModule, rec *model.Recommendation, d *AheadDetail, now time.Time) error {
	if d == nil || d.Settlement.Action != decision.ActionUp || d.Current <= 0 || rec.Replicas <= d.Current {
		return nil
	}
	open, err := s.Predictions.OpenOutcomes(ctx, m.ID)
	if err != nil {
		return err
	}
	for _, o := range open {
		if o.ToReplicas >= rec.Replicas && o.FromReplicas <= d.Current && now.Sub(o.DecidedAt) < decision.ObservationSpan(o.LeadSeconds) {
			return nil
		}
	}
	votes, _ := json.Marshal(d.Up.Votes)
	return s.Predictions.SaveOutcome(ctx, &model.ScaleOutcome{
		ModuleID: m.ID, Version: rec.Version, DecidedAt: now, FromReplicas: d.Current, ToReplicas: rec.Replicas, Emergency: d.Settlement.Emergency,
		LeadSeconds: d.Lead.Seconds, LeadSource: d.Lead.ReadySource, Algorithm: d.Leader, Votes: string(votes), Verdict: decision.VerdictPending,
	})
}

// OutcomesOf lists the scale-ups of a module with their verdicts, newest first.
func (s *Service) OutcomesOf(ctx context.Context, moduleID uint, verdict string, page, pageSize int) ([]*model.ScaleOutcome, int64, error) {
	return s.Predictions.ListOutcomes(ctx, moduleID, verdict, page, pageSize)
}

// describeAhead is the sentence put at the end of the reason of a replica count that came from the look-ahead.
func describeAhead(d *AheadDetail) string {
	return fmt.Sprintf("look-ahead %.0fs (%s)", d.Lead.Seconds, d.Lead.Reason)
}
