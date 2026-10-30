package decision

import (
	"fmt"
	"math"
)

// This file holds the acceptance indicators of the prediction delay: how late the platform asks for capacity, measured on what actually
// happened to the scale-ups it decided. Like the rest of the package it is pure: the facts come in, the verdicts go out.

// Acceptance targets of the delay indicators. A module passes when every indicator that could be measured meets its target.
const (
	// TargetTimelyRate is the share of the scale-ups the load really needed (timely and late) that were Ready before the load needed them.
	TargetTimelyRate = 0.9
	// TargetLateP90Seconds is how late the late scale-ups may be, by their 90th percentile.
	TargetLateP90Seconds = 60.0
	// TargetEmergencyShare is the most scale-ups that may have come from the load measured now instead of the forecast: a forecast that has
	// to be overruled often is behind the load.
	TargetEmergencyShare = 0.2
	// TargetPeakUnder is the most the forecast of the algorithm in use may fall short of the peak of the load, by the 90th percentile of its
	// evaluated runs (a forecast that lags the load misses the peak).
	TargetPeakUnder = 0.15
	// TargetDecisionWaitSeconds is the longest a decision may wait before the operator acts on it: the age the stored decision may have
	// plus the poll of the operator.
	TargetDecisionWaitSeconds = 120.0
	// MinJudgedForVerdict is how many judged scale-ups the rate indicators need before they say pass or fail.
	MinJudgedForVerdict = 5
)

// Indicator verdicts.
const (
	IndicatorPass         = "pass"
	IndicatorFail         = "fail"
	IndicatorInsufficient = "insufficient" // too little to judge
)

// Indicator is one acceptance figure of the prediction delay.
type Indicator struct {
	Key  string `json:"key"`
	Unit string `json:"unit"` // ratio, seconds, minutes
	// Op is how the value is compared with the target: ">=" or "<=".
	Op      string  `json:"op"`
	Target  float64 `json:"target"`
	Value   float64 `json:"value"`
	Samples int     `json:"samples"`
	Verdict string  `json:"verdict"`
	Note    string  `json:"note"`
}

func (i *Indicator) judge(value float64, samples, need int, note string) {
	i.Value, i.Samples, i.Note = value, samples, note
	if math.IsNaN(value) || math.IsInf(value, 0) {
		i.Value = 0 // not a figure that can be written as JSON; the verdict says there is none
	}
	switch {
	case samples < need || math.IsNaN(value):
		i.Verdict = IndicatorInsufficient
	case (i.Op == ">=" && value >= i.Target) || (i.Op == "<=" && value <= i.Target):
		i.Verdict = IndicatorPass
	default:
		i.Verdict = IndicatorFail
	}
}

// DelayInputs are the facts the indicators are made of.
type DelayInputs struct {
	// The judged scale-ups of the window, by verdict, and how late the late ones were.
	Timely, Late, Unneeded int
	LateSeconds            []float64
	// Scaled is every scale-up decided in the window, Emergency how many of them came from the live load.
	Scaled, Emergency int
	// ReachSeconds and LeadSeconds are how far ahead the forecasts reach and the lead of the newest decision; both 0 when unknown.
	ReachSeconds, LeadSeconds float64
	// PeakUnder are the peak under-estimations of the evaluated runs of the algorithm in use.
	PeakUnder []float64
	// DecisionWaitSeconds is the configured wait: the age a stored decision may have plus the operator's poll.
	DecisionWaitSeconds float64
}

// DelayIndicators judges the prediction delay. The first four say whether capacity is asked for in time; the last two whether the
// configuration lets it be.
func DelayIndicators(in DelayInputs) []Indicator {
	var out []Indicator

	needed := in.Timely + in.Late
	rate := Indicator{Key: "timely_rate", Unit: "ratio", Op: ">=", Target: TargetTimelyRate}
	value := math.NaN()
	if needed > 0 {
		value = float64(in.Timely) / float64(needed)
	}
	rate.judge(value, needed, MinJudgedForVerdict, fmt.Sprintf("%d of %d scale-ups the load needed were Ready before it did (%d were not needed)", in.Timely, needed, in.Unneeded))
	out = append(out, rate)

	late := Indicator{Key: "late_p90_seconds", Unit: "seconds", Op: "<=", Target: TargetLateP90Seconds}
	switch {
	case in.Late == 0 && needed >= MinJudgedForVerdict:
		late.judge(0, needed, MinJudgedForVerdict, "no scale-up was late")
	case in.Late == 0:
		late.judge(math.NaN(), needed, MinJudgedForVerdict, "too few scale-ups were judged")
	default:
		late.judge(Percentile(in.LateSeconds, 0.9), in.Late, 1, fmt.Sprintf("%d scale-ups were late; the 90th percentile of how late", in.Late))
	}
	out = append(out, late)

	emergency := Indicator{Key: "emergency_share", Unit: "ratio", Op: "<=", Target: TargetEmergencyShare}
	value = math.NaN()
	if in.Scaled > 0 {
		value = float64(in.Emergency) / float64(in.Scaled)
	}
	emergency.judge(value, in.Scaled, MinJudgedForVerdict, fmt.Sprintf("%d of %d scale-ups were asked for by the load measured now because the forecast was behind it", in.Emergency, in.Scaled))
	out = append(out, emergency)

	peak := Indicator{Key: "peak_under_p90", Unit: "ratio", Op: "<=", Target: TargetPeakUnder}
	value = math.NaN()
	if len(in.PeakUnder) > 0 {
		value = Percentile(in.PeakUnder, 0.9)
	}
	peak.judge(value, len(in.PeakUnder), MinJudgedForVerdict, "how far the forecast of the algorithm in use fell short of the peak of the load, 90th percentile of its evaluated runs")
	out = append(out, peak)

	reach := Indicator{Key: "reach_over_lead", Unit: "ratio", Op: ">=", Target: 1}
	value = math.NaN()
	samples := 0
	if in.LeadSeconds > 0 && in.ReachSeconds > 0 {
		value, samples = in.ReachSeconds/in.LeadSeconds, 1
	}
	reach.judge(value, samples, 1, fmt.Sprintf("the forecasts reach %.0fs ahead, the lead is %.0fs: a forecast that ends before the lead cannot see the load the replicas are for", in.ReachSeconds, in.LeadSeconds))
	out = append(out, reach)

	wait := Indicator{Key: "decision_wait_seconds", Unit: "seconds", Op: "<=", Target: TargetDecisionWaitSeconds}
	samples = 0
	if in.DecisionWaitSeconds > 0 {
		samples = 1
	}
	wait.judge(in.DecisionWaitSeconds, samples, 1, "the age a stored decision may have plus the poll of the operator: the time a decision can wait before it is acted on")
	out = append(out, wait)
	return out
}

// evidenceIndicators are the four that say whether capacity was asked for in time, from what really happened to the scale-ups and to the
// forecasts. The configuration indicators (reach over the lead, the decision wait) can fail a module; they are not evidence of delay.
var evidenceIndicators = map[string]bool{"timely_rate": true, "late_p90_seconds": true, "emergency_share": true, "peak_under_p90": true}

// DelayVerdict is the verdict of the whole set. Fail if any indicator fails: a failed one is evidence even when the rest cannot be judged.
// Pass only when all four evidence indicators could be judged and met their targets: a module whose scale-ups have not been judged yet is
// not "达标" because the one figure that could be read happened to be good. Anything else is insufficient.
func DelayVerdict(indicators []Indicator) string {
	passed, failed := 0, false
	for _, i := range indicators {
		switch i.Verdict {
		case IndicatorFail:
			failed = true
		case IndicatorPass:
			if evidenceIndicators[i.Key] {
				passed++
			}
		}
	}
	switch {
	case failed:
		return IndicatorFail
	case passed < len(evidenceIndicators):
		return IndicatorInsufficient
	}
	return IndicatorPass
}
