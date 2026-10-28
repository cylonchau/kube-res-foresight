package decision

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// This file holds the pure rules of the scale-ahead decision: how far ahead capacity has to be asked for (the lead), how the
// replica counts of several algorithms are brought to one (the consensus), what the load measured now overrules (the live check and
// the emergency), and whether a scale-up came in time (the outcome). Nothing here reads a store or the clock.

// Lead sources.
const (
	LeadMeasured   = "measured"   // the time scale-ups of this module took to become Ready, from its own outcomes
	LeadConfigured = "configured" // the configured Pod and node time, while too few scale-ups were measured
)

// MinLeadSamples is how many finished scale-ups the measured ready time rests on; fewer, and the configured time is used.
const MinLeadSamples = 3

// LeadInputs are the facts the lead is made of.
type LeadInputs struct {
	// ReadySeconds are the measured times from a scale-up decision until the replicas were Ready (Pod start and, when the cluster had
	// to add a node, the node as well: the measure contains both).
	ReadySeconds []float64
	// ConfiguredReadySeconds and NodeSeconds are used while fewer than MinLeadSamples were measured; the node time is added only then,
	// since a measured time already contains it.
	ConfiguredReadySeconds float64
	NodeSeconds            float64
	// DecisionSeconds is how long a decision may wait before it is acted on: the age the stored decision may have plus the poll of the
	// operator.
	DecisionSeconds float64
	// LateSeconds are how late the recent scale-ups were (only the late ones); their P90 is added as a margin, so a module whose
	// scale-ups came late asks for capacity earlier next time.
	LateSeconds []float64
	StepSeconds float64
	// HorizonSeconds is how far the algorithms forecast; the lead is never longer.
	HorizonSeconds float64
}

// Lead is how far ahead of the load the capacity has to be asked for, and what it is made of.
type Lead struct {
	Seconds        float64 `json:"seconds"`
	ReadySeconds   float64 `json:"ready_seconds"`
	ReadySource    string  `json:"ready_source"`
	ReadySamples   int     `json:"ready_samples"`
	NodeSeconds    float64 `json:"node_seconds"`
	DecisionSecond float64 `json:"decision_seconds"`
	MarginSeconds  float64 `json:"margin_seconds"`
	// Capped is true when the sum was longer than the forecast horizon and was cut to it.
	Capped bool   `json:"capped"`
	Reason string `json:"reason"`
}

// ComputeLead is the time the replicas need from the decision until they take load: the Ready time (measured, else configured plus the
// node), the wait before a decision is acted on, and a margin for the lateness seen lately; rounded up to whole steps, at least two
// steps, at most the horizon.
func ComputeLead(in LeadInputs) Lead {
	l := Lead{DecisionSecond: math.Max(in.DecisionSeconds, 0)}
	var ready []float64
	for _, v := range in.ReadySeconds {
		if v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			ready = append(ready, v)
		}
	}
	l.ReadySamples = len(ready)
	if len(ready) >= MinLeadSamples {
		l.ReadySeconds, l.ReadySource = Percentile(ready, 0.9), LeadMeasured
	} else {
		l.ReadySeconds, l.ReadySource, l.NodeSeconds = math.Max(in.ConfiguredReadySeconds, 0), LeadConfigured, math.Max(in.NodeSeconds, 0)
	}
	var late []float64
	for _, v := range in.LateSeconds {
		if v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			late = append(late, v)
		}
	}
	if len(late) > 0 {
		l.MarginSeconds = Percentile(late, 0.9)
	}
	total := l.ReadySeconds + l.NodeSeconds + l.DecisionSecond + l.MarginSeconds
	step := in.StepSeconds
	if step > 0 {
		total = math.Ceil(total/step) * step
		if total < 2*step {
			total = 2 * step
		}
	}
	if in.HorizonSeconds > 0 && total > in.HorizonSeconds {
		total, l.Capped = in.HorizonSeconds, true
	}
	l.Seconds = total
	source := "configured Pod ready time"
	if l.ReadySource == LeadMeasured {
		source = fmt.Sprintf("P90 of %d measured ready times", l.ReadySamples)
	}
	l.Reason = fmt.Sprintf("%.0fs = %s %.0fs + node %.0fs + decision wait %.0fs + margin for lateness %.0fs", l.Seconds, source, l.ReadySeconds, l.NodeSeconds, l.DecisionSecond, l.MarginSeconds)
	if l.Capped {
		l.Reason += ", cut to the forecast horizon"
	}
	return l
}

// Windows are the look-ahead windows a decision reports, in minutes, up to the horizon; the horizon itself is always the last one.
func Windows(horizon time.Duration) []time.Duration {
	var out []time.Duration
	for _, m := range []int{5, 10, 15, 30, 60} {
		if d := time.Duration(m) * time.Minute; d < horizon {
			out = append(out, d)
		}
	}
	return append(out, horizon)
}

// StepsIn is how many forecast steps a window covers: rounded up, at least one, at most limit (the steps the forecast has).
func StepsIn(window, step time.Duration, limit int) int {
	if step <= 0 {
		return limit
	}
	n := int(math.Ceil(float64(window) / float64(step)))
	if n < 1 {
		n = 1
	}
	if limit > 0 && n > limit {
		n = limit
	}
	return n
}

// Vote is the replica count one algorithm asks for and how far it can be trusted.
type Vote struct {
	Algorithm   string  `json:"algorithm"`
	Replicas    int     `json:"replicas"`
	Credibility float64 `json:"credibility"`
}

// Credibility is how far an algorithm is believed, in (0, 1]: its score (0..100; 50 when it has none) times how closely its newest
// forecasts followed the load (1 - WAPE; 0.8 when there is nothing to compare). It is a weight, not a probability.
func Credibility(score float64, scored bool, wape float64, tracked bool) float64 {
	if !scored || math.IsNaN(score) {
		score = 50
	}
	base := math.Min(math.Max(score/100, 0.05), 1)
	track := 0.8
	if tracked {
		track = math.Min(math.Max(1-wape, 0.05), 1)
	}
	return base * track
}

// Consensus is the replica count the votes agree on.
type Consensus struct {
	Replicas int `json:"replicas"`
	// Quantile is the share of the credibility weight that has to be covered: the higher it is, the more the capacity risk counts
	// against the cost of an idle replica.
	Quantile float64 `json:"quantile"`
	Min      int     `json:"min"`
	Max      int     `json:"max"`
	// Leader is the algorithm at the consensus value that is trusted most; its plan explains the number.
	Leader string `json:"leader"`
	Votes  []Vote `json:"votes"`
	Reason string `json:"reason"`
}

// Reconcile brings the votes of the algorithms to one replica count without taking the largest or the smallest: the credibility-weighted
// quantile. With the quantile q, the result is the smallest count that at least a share q of the trust votes for or below. A low-trust
// algorithm that asks for much more does not move it; a count asked for by most of the trust does. Where q is high (0.75) a count that
// a quarter of the trust asks for beyond the rest still wins, which is the capacity-first side of the loss; scaling down uses a
// higher q than scaling up. ok is false when no vote has any credibility.
func Reconcile(votes []Vote, q float64) (Consensus, bool) {
	if q <= 0 || q > 1 || math.IsNaN(q) {
		q = 0.75
	}
	var use []Vote
	total := 0.0
	for _, v := range votes {
		if v.Credibility > 0 && v.Replicas >= 0 {
			use = append(use, v)
			total += v.Credibility
		}
	}
	c := Consensus{Quantile: q, Votes: append([]Vote{}, votes...)}
	if len(use) == 0 {
		return c, false
	}
	sort.SliceStable(use, func(i, j int) bool {
		if use[i].Replicas != use[j].Replicas {
			return use[i].Replicas < use[j].Replicas
		}
		return use[i].Credibility > use[j].Credibility
	})
	c.Min, c.Max = use[0].Replicas, use[len(use)-1].Replicas
	cum := 0.0
	for _, v := range use {
		cum += v.Credibility
		if cum >= q*total-1e-9 {
			c.Replicas = v.Replicas
			break
		}
	}
	best := -1.0
	for _, v := range use {
		if v.Replicas == c.Replicas && v.Credibility > best {
			c.Leader, best = v.Algorithm, v.Credibility
		}
	}
	if c.Min == c.Max {
		c.Reason = fmt.Sprintf("all %d algorithms ask for %d", len(use), c.Replicas)
	} else {
		c.Reason = fmt.Sprintf("%d algorithms ask for %d to %d; %.0f%% of the trust is covered at %d", len(use), c.Min, c.Max, q*100, c.Replicas)
	}
	return c, true
}

// Actions of a settlement.
const (
	ActionUp   = "up"
	ActionDown = "down"
	ActionHold = "hold"
)

// SettleInputs are the two consensuses and the load measured now.
type SettleInputs struct {
	// Current is the replica count in force (0 = unknown).
	Current int
	// Up is the consensus over the look-ahead window of the lead: the capacity that has to be Ready when the replicas asked for now
	// start to take load. Down is the consensus over the whole horizon at the stricter quantile: the capacity a scale-down must
	// still keep.
	Up, Down Consensus
	HaveUp   bool
	HaveDown bool
	// Live is the replica count the load measured now needs (LiveOK: it could be measured and is recent).
	Live   int
	LiveOK bool
	// EmergencyOn lets the live load raise the replicas above the forecast.
	EmergencyOn bool
	Max         int
}

// Settlement is the replica count that came of the consensuses and the live load.
type Settlement struct {
	Replicas int    `json:"replicas"`
	Action   string `json:"action"`
	// Emergency is true when the live load needed more than the forecast and the current replicas gave: the forecast failed or the
	// traffic jumped, and the operator should not wait for a step or a cooldown.
	Emergency bool   `json:"emergency"`
	Reason    string `json:"reason"`
}

// Settle decides what the platform asks for:
//   - the capacity of the look-ahead window decides a scale-up, so replicas are asked for the lead before the load needs them, and not
//     for a peak that is further away than the replicas need to start;
//   - a scale-down needs the whole horizon to agree at the stricter quantile and the load measured now to fit in the lower count;
//   - the live load can raise the count above the forecast when it needs more than the replicas in force and the forecast gave.
func Settle(in SettleInputs) Settlement {
	cur := in.Current
	s := Settlement{Replicas: cur, Action: ActionHold}
	switch {
	case in.HaveUp && cur > 0 && in.Up.Replicas > cur:
		s.Replicas, s.Action = in.Up.Replicas, ActionUp
		s.Reason = fmt.Sprintf("the look-ahead window needs %d replicas, %d are in force: asked for now, so they are Ready when the load comes (%s)", in.Up.Replicas, cur, in.Up.Reason)
	case !in.HaveUp && !in.HaveDown:
		s.Reason = "no forecast to decide on"
	default:
		target := 0
		if in.HaveUp {
			target = in.Up.Replicas
		}
		if in.HaveDown && in.Down.Replicas > target {
			target = in.Down.Replicas
		}
		switch {
		case cur == 0:
			s.Replicas, s.Action = target, ActionUp
			s.Reason = "the replica count in force is not known; the forecast of the whole horizon is followed"
		case target < cur:
			s.Replicas, s.Action = target, ActionDown
			s.Reason = fmt.Sprintf("the whole horizon agrees on %d at the %.0f%% quantile, below the %d in force", target, in.Down.Quantile*100, cur)
			if in.LiveOK && in.Live > target {
				s.Replicas = in.Live
				if s.Replicas >= cur {
					s.Replicas, s.Action = cur, ActionHold
					s.Reason = fmt.Sprintf("a lower count was forecast but the load measured now needs %d: not scaled down", in.Live)
				} else {
					s.Reason += fmt.Sprintf("; not below %d, which the load measured now needs", in.Live)
				}
			}
		default:
			s.Reason = fmt.Sprintf("%d replicas cover the look-ahead window; nothing to ask for ahead", cur)
		}
	}
	if in.EmergencyOn && in.LiveOK && in.Live > s.Replicas && in.Live > cur && cur > 0 {
		live := in.Live
		if in.Max > 0 && live > in.Max {
			live = in.Max
		}
		if live > s.Replicas {
			planned := s.Replicas
			s.Replicas, s.Action, s.Emergency = live, ActionUp, true
			s.Reason = fmt.Sprintf("emergency: the load measured now needs %d replicas, more than the forecast gave (%d) and the %d in force", in.Live, planned, cur)
		}
	}
	return s
}

// Verdicts of a scale-up outcome.
const (
	VerdictPending  = "pending"  // not enough time has passed to say
	VerdictTimely   = "timely"   // the replicas were Ready before the load needed them
	VerdictLate     = "late"     // the load needed them before they were Ready
	VerdictUnneeded = "unneeded" // the load never needed the replicas that were added in the time observed
)

// NeedPoint is the replica count the load measured at a moment needed.
type NeedPoint struct {
	At   time.Time `json:"at"`
	Need int       `json:"need"`
}

// OutcomeInput is one scale-up, as it was decided and as the load and the replicas went after it.
type OutcomeInput struct {
	DecidedAt time.Time
	From, To  int
	// LeadSeconds is the look-ahead that was used; Needs the replica count the load needed at each measured moment since.
	LeadSeconds float64
	Needs       []NeedPoint
	// ReadyAt is when at least To replicas were Ready, nil if that has not been seen.
	ReadyAt *time.Time
	Now     time.Time
}

// Outcome says whether a scale-up came in time.
type Outcome struct {
	Verdict string `json:"verdict"`
	// NeededAt is the first moment the load needed more than From replicas, nil if it did not in the time observed.
	NeededAt *time.Time `json:"needed_at,omitempty"`
	// ActualNeed is the most replicas the load needed within the lead after the decision; the decision predicted To.
	ActualNeed int `json:"actual_need"`
	// ReadySeconds is from the decision until To replicas were Ready (0 = not seen). LateSeconds is how long the load waited for
	// them (only for late), SlackSeconds how long before the load they were Ready (only for timely).
	ReadySeconds float64 `json:"ready_seconds"`
	LateSeconds  float64 `json:"late_seconds"`
	SlackSeconds float64 `json:"slack_seconds"`
	// Final is false while the verdict may still change.
	Final  bool   `json:"final"`
	Reason string `json:"reason"`
}

// ObservationSpan is how long a scale-up is watched before it is judged: three leads, at least ten minutes.
func ObservationSpan(leadSeconds float64) time.Duration {
	d := time.Duration(3*leadSeconds) * time.Second
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// EvaluateOutcome judges one scale-up: it was timely when the replicas were Ready no later than the moment the load first needed more
// than the replicas that were in force; late when the load needed them first; unneeded when the load did not come in the time observed.
func EvaluateOutcome(in OutcomeInput) Outcome {
	o := Outcome{Verdict: VerdictPending}
	lead := time.Duration(in.LeadSeconds) * time.Second
	for _, p := range in.Needs {
		if p.At.Before(in.DecidedAt) {
			continue
		}
		if p.At.Sub(in.DecidedAt) <= lead && p.Need > o.ActualNeed {
			o.ActualNeed = p.Need
		}
		if o.NeededAt == nil && p.Need > in.From {
			at := p.At
			o.NeededAt = &at
		}
	}
	if in.ReadyAt != nil {
		o.ReadySeconds = in.ReadyAt.Sub(in.DecidedAt).Seconds()
	}
	span := ObservationSpan(in.LeadSeconds)
	over := !in.Now.Before(in.DecidedAt.Add(span))
	switch {
	case o.NeededAt != nil && in.ReadyAt != nil && !in.ReadyAt.After(*o.NeededAt):
		o.Verdict, o.Final = VerdictTimely, true
		o.SlackSeconds = o.NeededAt.Sub(*in.ReadyAt).Seconds()
		o.Reason = fmt.Sprintf("Ready %.0fs before the load needed more than %d replicas", o.SlackSeconds, in.From)
	case o.NeededAt != nil && in.ReadyAt != nil:
		o.Verdict, o.Final = VerdictLate, true
		o.LateSeconds = in.ReadyAt.Sub(*o.NeededAt).Seconds()
		o.Reason = fmt.Sprintf("the load needed more than %d replicas %.0fs before they were Ready", in.From, o.LateSeconds)
	case o.NeededAt != nil:
		o.Verdict = VerdictLate
		o.LateSeconds = in.Now.Sub(*o.NeededAt).Seconds()
		o.Final = over
		o.Reason = fmt.Sprintf("the load needed more than %d replicas and the %d were not Ready %.0fs later", in.From, in.To, o.LateSeconds)
	case over:
		o.Verdict, o.Final = VerdictUnneeded, true
		o.Reason = fmt.Sprintf("the load did not need more than %d replicas within %s", in.From, span)
	default:
		o.Reason = "still being observed"
	}
	return o
}
