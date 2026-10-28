package decision

import (
	"testing"
	"time"
)

func TestLeadIsMeasuredWhenEnoughScaleUpsWereSeenAndConfiguredBefore(t *testing.T) {
	cfg := LeadInputs{ConfiguredReadySeconds: 60, NodeSeconds: 120, DecisionSeconds: 90, StepSeconds: 60, HorizonSeconds: 900}
	l := ComputeLead(cfg)
	if l.ReadySource != LeadConfigured || l.Seconds != 300 { // 60 + 120 + 90 = 270 -> 5 steps of 60s
		t.Fatalf("before any measurement the configured time and the node are used: %+v", l)
	}
	cfg.ReadySeconds = []float64{100, 110, 130, 150}
	l = ComputeLead(cfg)
	if l.ReadySource != LeadMeasured || l.NodeSeconds != 0 {
		t.Fatalf("a measured time already holds the node: %+v", l)
	}
	if l.Seconds != 240 { // P90 of the four ~144 + 90 = 234 -> 240
		t.Fatalf("lead %v", l.Seconds)
	}
}

func TestLeadGrowsWithTheLatenessSeenAndNeverPassesTheHorizon(t *testing.T) {
	base := LeadInputs{ReadySeconds: []float64{60, 60, 60}, DecisionSeconds: 60, StepSeconds: 60, HorizonSeconds: 900}
	before := ComputeLead(base)
	base.LateSeconds = []float64{120, 180}
	after := ComputeLead(base)
	if after.Seconds <= before.Seconds || after.MarginSeconds < 120 {
		t.Fatalf("a module whose scale-ups were late asks earlier: %v -> %v", before.Seconds, after.Seconds)
	}
	base.ReadySeconds = []float64{2000, 2000, 2000}
	if l := ComputeLead(base); l.Seconds != 900 || !l.Capped {
		t.Fatalf("the lead is cut to the horizon: %+v", l)
	}
	if l := ComputeLead(LeadInputs{StepSeconds: 60, HorizonSeconds: 900}); l.Seconds != 120 {
		t.Fatalf("at least two steps: %v", l.Seconds)
	}
}

func TestReconcileFollowsTheTrustAndNotTheLargestOrSmallest(t *testing.T) {
	// the trusted algorithms agree on 1; the distrusted ones ask for 9
	c, ok := Reconcile([]Vote{{"holt_winters", 1, 0.9}, {"theta", 1, 0.8}, {"seasonal_naive", 9, 0.1}, {"naive", 9, 0.1}}, 0.75)
	if !ok || c.Replicas != 1 {
		t.Fatalf("the trusted votes decide: %+v", c)
	}
	// the other way round: the trusted one asks for more
	c, _ = Reconcile([]Vote{{"a", 1, 0.2}, {"b", 9, 0.9}, {"c", 1, 0.2}}, 0.75)
	if c.Replicas != 9 || c.Leader != "b" {
		t.Fatalf("a trusted algorithm that sees the rise is followed: %+v", c)
	}
	// a split vote goes to the capacity side at a high quantile and to the middle at a low one
	split := []Vote{{"a", 2, 0.5}, {"b", 8, 0.5}}
	if c, _ := Reconcile(split, 0.75); c.Replicas != 8 {
		t.Fatalf("capacity first: %+v", c)
	}
	if c, _ := Reconcile(split, 0.5); c.Replicas != 2 {
		t.Fatalf("median: %+v", c)
	}
	if _, ok := Reconcile([]Vote{{"a", 3, 0}}, 0.75); ok {
		t.Fatal("a vote without credibility counts for nothing")
	}
}

func TestCredibilityFallsWithTheTrackingErrorAndTheMissingScore(t *testing.T) {
	good := Credibility(90, true, 0.1, true)
	bad := Credibility(90, true, 0.45, true)
	unseen := Credibility(90, true, 0, false)
	unscored := Credibility(0, false, 0.1, true)
	if !(good > unseen && unseen > bad) || unscored >= good {
		t.Fatalf("good %v unseen %v bad %v unscored %v", good, unseen, bad, unscored)
	}
}

func up(n int) Consensus   { return Consensus{Replicas: n, Quantile: 0.75} }
func down(n int) Consensus { return Consensus{Replicas: n, Quantile: 0.9} }

func TestSettleAsksForTheLookAheadWindowAndNotForAPeakFurtherAway(t *testing.T) {
	s := Settle(SettleInputs{Current: 4, Up: up(7), HaveUp: true, Down: down(12), HaveDown: true})
	if s.Replicas != 7 || s.Action != ActionUp || s.Emergency {
		t.Fatalf("scale up for the window of the lead: %+v", s)
	}
	// the peak is beyond the lead: nothing is asked for yet, and nothing is taken away
	s = Settle(SettleInputs{Current: 4, Up: up(4), HaveUp: true, Down: down(12), HaveDown: true})
	if s.Replicas != 4 || s.Action != ActionHold {
		t.Fatalf("hold: %+v", s)
	}
}

func TestSettleScalesDownOnlyWhereTheLiveLoadFits(t *testing.T) {
	s := Settle(SettleInputs{Current: 9, Up: up(1), HaveUp: true, Down: down(1), HaveDown: true, Live: 1, LiveOK: true})
	if s.Replicas != 1 || s.Action != ActionDown {
		t.Fatalf("down: %+v", s)
	}
	s = Settle(SettleInputs{Current: 9, Up: up(1), HaveUp: true, Down: down(1), HaveDown: true, Live: 5, LiveOK: true})
	if s.Replicas != 5 || s.Action != ActionDown {
		t.Fatalf("not below what the load needs now: %+v", s)
	}
	s = Settle(SettleInputs{Current: 9, Up: up(1), HaveUp: true, Down: down(1), HaveDown: true, Live: 9, LiveOK: true})
	if s.Replicas != 9 || s.Action != ActionHold {
		t.Fatalf("the load fills what is there: %+v", s)
	}
}

func TestSettleRaisesOnTheLiveLoadWhenTheForecastFailed(t *testing.T) {
	in := SettleInputs{Current: 3, Up: up(3), HaveUp: true, Down: down(3), HaveDown: true, Live: 8, LiveOK: true, EmergencyOn: true, Max: 10}
	s := Settle(in)
	if s.Replicas != 8 || !s.Emergency || s.Action != ActionUp {
		t.Fatalf("emergency: %+v", s)
	}
	in.Live = 30
	if s = Settle(in); s.Replicas != 10 {
		t.Fatalf("capped at the maximum: %+v", s)
	}
	in.EmergencyOn = false
	if s = Settle(in); s.Replicas != 3 || s.Emergency {
		t.Fatalf("emergency off: %+v", s)
	}
	// a forecast that already asks for more is not an emergency
	in.EmergencyOn, in.Up, in.Live = true, up(9), 8
	if s = Settle(in); s.Replicas != 9 || s.Emergency {
		t.Fatalf("forecast ahead of the load: %+v", s)
	}
}

func TestOutcomeIsTimelyLateOrUnneeded(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	needs := []NeedPoint{{at(1), 3}, {at(3), 3}, {at(5), 6}, {at(7), 8}}
	ready := func(m int) *time.Time { r := at(m); return &r }

	o := EvaluateOutcome(OutcomeInput{DecidedAt: t0, From: 3, To: 8, LeadSeconds: 360, Needs: needs, ReadyAt: ready(4), Now: at(12)})
	if o.Verdict != VerdictTimely || o.SlackSeconds != 60 || o.ActualNeed != 6 || !o.Final {
		t.Fatalf("ready a minute before the load: %+v", o)
	}
	o = EvaluateOutcome(OutcomeInput{DecidedAt: t0, From: 3, To: 8, LeadSeconds: 360, Needs: needs, ReadyAt: ready(8), Now: at(12)})
	if o.Verdict != VerdictLate || o.LateSeconds != 180 {
		t.Fatalf("ready three minutes after: %+v", o)
	}
	o = EvaluateOutcome(OutcomeInput{DecidedAt: t0, From: 3, To: 8, LeadSeconds: 360, Needs: needs, Now: at(6)})
	if o.Verdict != VerdictLate || o.Final {
		t.Fatalf("not Ready yet, the load is already there: %+v", o)
	}
	flat := []NeedPoint{{at(1), 3}, {at(5), 3}, {at(9), 2}}
	if o = EvaluateOutcome(OutcomeInput{DecidedAt: t0, From: 3, To: 5, LeadSeconds: 360, Needs: flat, ReadyAt: ready(3), Now: at(5)}); o.Verdict != VerdictPending {
		t.Fatalf("too early to say: %+v", o)
	}
	if o = EvaluateOutcome(OutcomeInput{DecidedAt: t0, From: 3, To: 5, LeadSeconds: 360, Needs: flat, ReadyAt: ready(3), Now: at(30)}); o.Verdict != VerdictUnneeded || !o.Final {
		t.Fatalf("the load did not come: %+v", o)
	}
}

func TestWindowsAndSteps(t *testing.T) {
	w := Windows(15 * time.Minute)
	if len(w) != 3 || w[0] != 5*time.Minute || w[2] != 15*time.Minute {
		t.Fatalf("%v", w)
	}
	if w = Windows(45 * time.Minute); w[len(w)-1] != 45*time.Minute || len(w) != 5 {
		t.Fatalf("%v", w)
	}
	if n := StepsIn(5*time.Minute, time.Minute, 15); n != 5 {
		t.Fatalf("%d", n)
	}
	if n := StepsIn(5*time.Minute, 90*time.Second, 15); n != 4 {
		t.Fatalf("round up: %d", n)
	}
	if n := StepsIn(time.Hour, time.Minute, 15); n != 15 {
		t.Fatalf("limited to the forecast: %d", n)
	}
}
