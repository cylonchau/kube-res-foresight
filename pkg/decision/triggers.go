package decision

import (
	"fmt"
	"math"
	"sort"
)

// SourceTrigger is a replica count that a metric trigger of the module changed (see ApplyTriggers).
const SourceTrigger Source = "trigger"

// Actions of a trigger (the same words as model.ModuleTrigger).
const (
	TriggerScaleUp   = "scale_up"
	TriggerScaleDown = "scale_down"
	TriggerSet       = "set"
	TriggerPercent   = "percent"
)

// TriggerProportional is the unit of a trigger that works the replica count out of the metric: the running replicas times the value over the threshold (see FiredTrigger.Unit).
const TriggerProportional = "proportional"

// FiredTrigger is a trigger whose metric stands on the wrong side of its threshold now and that is in force at this time: what it saw, and what it asks for.
type FiredTrigger struct {
	Name     string  `json:"name"`
	Priority int     `json:"priority"`
	Action   string  `json:"action"`
	Unit     string  `json:"unit"` // replicas or percent
	Amount   float64 `json:"amount"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	// Threshold is the figure the value was compared with; Weight how much the trigger counts when scale-ups and scale-downs fire together (0 counts as 1).
	Threshold float64 `json:"threshold"`
	Weight    float64 `json:"weight"`
	Why       string  `json:"why"`
}

// weightOf is the weight of a trigger, 1 when it sets none.
func weightOf(t FiredTrigger) float64 {
	if t.Weight <= 0 {
		return 1
	}
	return t.Weight
}

// Score is how much a fired trigger counts in the vote between scaling out and scaling in: its weight times how far the metric is past its threshold, from once (just past it)
// to twice (as far past it again as the threshold itself, or more). The weight says how far the metric is trusted; the depth says what the use really is.
func (t FiredTrigger) Score() float64 {
	w := t.Weight
	if w <= 0 {
		w = 1
	}
	depth := 1.0
	if base := math.Abs(t.Threshold); base > 0 {
		depth += math.Min(math.Abs(t.Value-t.Threshold)/base, 1)
	}
	return w * depth
}

// TriggerStep is one fired trigger in the outcome: the replica count it asks for and whether it counted.
type TriggerStep struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Priority int    `json:"priority"`
	Proposed int    `json:"proposed"`
	Used     bool   `json:"used"`
	Why      string `json:"why"`
	// Weight and Score are what the trigger counted for in the vote of the scale-ups against the scale-downs (see FiredTrigger.Score).
	Weight float64 `json:"weight,omitempty"`
	Score  float64 `json:"score,omitempty"`
}

// TriggerOutcome is how the triggers and the rest of the strategies were combined: the replicas before, after, and the rule that decided.
type TriggerOutcome struct {
	Base    int           `json:"base"`
	Current int           `json:"current"`
	Result  int           `json:"result"`
	Rule    string        `json:"rule"`
	Steps   []TriggerStep `json:"steps"`
	// UpScore and DownScore are the votes of the triggers that scale out and of the ones that scale in; both are set only when both sides fired.
	UpScore   float64 `json:"up_score,omitempty"`
	DownScore float64 `json:"down_score,omitempty"`
}

// ApplyTriggers combines the metric triggers that fired with the replica count the other strategies decided (f: the forecast, the module's fallback or fixed count),
// by these rules, in this order:
//
//  1. A bypass, a fixed period and a fixed count are the administrator's word: the triggers do not touch them.
//  2. A trigger that sets a count is followed; when several set one, the highest priority wins (equal priorities: the larger count, for safety).
//  3. A trigger that scales up asks for the running count plus its amount; the largest of them is a floor under whatever was decided: a scale-up is never cut by a
//     set or a forecast.
//  4. A trigger that scales down asks for the running count minus its amount. It acts only when no set fired, when no protection blocks it (blockDown says
//     why: an OOM kill, a heap under pressure) and when the forecast does not want more replicas than run now; of several, the one that removes the fewest replicas
//     counts.
//  5. When triggers that scale up and triggers that scale down fire together they are weighed: each counts its weight times how far its metric is past its threshold
//     (FiredTrigger.Score), the larger total decides the direction and a tie goes to scaling out. A side that a protection or a set rules out does not take part, so a scale-down
//     that cannot act never costs a scale-up its vote. This is how a container that is full and a JVM that has used little of its committed memory are told apart: the
//     weights say which of the two is believed.
//
// The result stays between lo and hi (0 = no bound) like every other source: the periods and the module bounds are applied after this, and the pace limits of the
// operator on top of them. current is the replicas that run now (0 = not known: a trigger that is relative to it is then left out).
func ApplyTriggers(f Field, current int, fired []FiredTrigger, blockDown string, lo, hi int) (Field, *TriggerOutcome) {
	if len(fired) == 0 {
		return f, nil
	}
	out := &TriggerOutcome{Current: current}
	switch f.Source {
	case SourceBypass, SourceFixedPeriod, SourceFixedCount:
		for _, t := range fired {
			out.Steps = append(out.Steps, TriggerStep{Name: t.Name, Action: t.Action, Priority: t.Priority, Why: "the replicas are fixed by the administrator (bypass, a fixed period or the module's fixed count): a trigger does not change them"})
		}
		out.Base, out.Result, out.Rule = int(f.Value), int(f.Value), "fixed by the administrator"
		return f, out
	}
	base := current
	if f.Set {
		base = int(f.Value)
	}
	out.Base = base

	order := append([]FiredTrigger(nil), fired...)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Priority != order[j].Priority {
			return order[i].Priority > order[j].Priority
		}
		return order[i].Name < order[j].Name
	})
	steps := make([]TriggerStep, len(order))
	proposed := func(t FiredTrigger) (int, string) {
		switch t.Action {
		case TriggerSet:
			return int(math.Round(t.Amount)), ""
		}
		if current <= 0 {
			return 0, "the replicas that run are not known, so a change relative to them cannot be worked out"
		}
		delta := int(math.Ceil(t.Amount))
		switch t.Unit {
		case TriggerPercent:
			delta = int(math.Ceil(float64(current) * t.Amount / 100))
		case TriggerProportional:
			// the count the metric calls for: running x value / threshold, rounded up for a scale-up and for a scale-down alike (a scale-down then removes the fewer replicas); at most Amount away
			if t.Threshold <= 0 {
				return 0, "the threshold of a proportional trigger must be above 0"
			}
			need := int(math.Ceil(float64(current) * t.Value / t.Threshold))
			delta = need - current
			if t.Action == TriggerScaleDown {
				delta = current - need
			}
			if limit := int(math.Ceil(t.Amount)); limit > 0 && delta > limit {
				delta = limit
			}
		}
		if delta < 1 {
			delta = 1
		}
		if t.Action == TriggerScaleDown {
			return current - delta, ""
		}
		return current + delta, ""
	}
	var sets, ups, downs []int
	for i, t := range order {
		n, why := proposed(t)
		steps[i] = TriggerStep{Name: t.Name, Action: t.Action, Priority: t.Priority, Proposed: n, Why: why, Weight: weightOf(t), Score: t.Score()}
		if why != "" {
			continue
		}
		switch t.Action {
		case TriggerSet:
			sets = append(sets, i)
		case TriggerScaleUp:
			ups = append(ups, i)
		case TriggerScaleDown:
			downs = append(downs, i)
		}
	}

	result := base
	rule := "no trigger changed the count"
	if len(sets) > 0 {
		win := sets[0]
		for _, i := range sets {
			if steps[i].Priority == steps[win].Priority && steps[i].Proposed > steps[win].Proposed {
				win = i
			}
		}
		result, rule = steps[win].Proposed, fmt.Sprintf("%q sets the count (highest priority of the triggers that set one)", steps[win].Name)
		steps[win].Used = true
		for _, i := range sets {
			if i != win {
				steps[i].Why = fmt.Sprintf("%q sets it with a higher priority (or an equal one and a larger count)", steps[win].Name)
			}
		}
	}

	// which side may act: a scale-down never acts against a set, a protection or a forecast that wants more replicas than run
	downBlocked := ""
	switch {
	case len(sets) > 0:
		downBlocked = "a set fired as well: a scale-down never acts against it"
	case blockDown != "":
		downBlocked = "blocked by a protection: " + blockDown
	case current > 0 && base > current:
		downBlocked = fmt.Sprintf("the forecast asks for %d replicas, more than the %d that run", base, current)
	}
	if downBlocked != "" {
		for _, i := range downs {
			steps[i].Why = downBlocked
		}
		downs = nil
	}
	// scale-ups and scale-downs that fire together are weighed: the larger total of weight x depth decides the direction, a tie changes nothing
	if len(ups) > 0 && len(downs) > 0 {
		var up, down float64
		for _, i := range ups {
			up += steps[i].Score
		}
		for _, i := range downs {
			down += steps[i].Score
		}
		out.UpScore, out.DownScore = up, down
		if up >= down {
			// a tie goes to scaling out: too few replicas is the riskier mistake
			for _, i := range downs {
				steps[i].Why = fmt.Sprintf("outvoted: the scale-ups weigh %.2f against %.2f of the scale-downs (a tie goes to scaling out)", up, down)
			}
			downs = nil
		} else {
			for _, i := range ups {
				steps[i].Why = fmt.Sprintf("outvoted: the scale-downs weigh %.2f against %.2f of the scale-ups", down, up)
			}
			ups = nil
		}
	}
	if len(ups) > 0 {
		top := ups[0]
		for _, i := range ups {
			if steps[i].Proposed > steps[top].Proposed {
				top = i
			}
		}
		if steps[top].Proposed > result {
			result = steps[top].Proposed
			steps[top].Used = true
			switch {
			case len(sets) > 0:
				rule += fmt.Sprintf("; raised by the scale-up %q (a scale-up is never cut by a set)", steps[top].Name)
			case out.DownScore > 0:
				rule = fmt.Sprintf("%q scales up (the scale-ups outweigh the scale-downs, %.2f to %.2f)", steps[top].Name, out.UpScore, out.DownScore)
			default:
				rule = fmt.Sprintf("%q scales up (the largest of the scale-ups)", steps[top].Name)
			}
		}
		for _, i := range ups {
			if !steps[i].Used && steps[i].Why == "" {
				steps[i].Why = "another scale-up asks for more, or the count already reaches it"
			}
		}
	}
	if len(downs) > 0 {
		keep := downs[0] // the one that removes the fewest replicas
		for _, i := range downs {
			if steps[i].Proposed > steps[keep].Proposed {
				keep = i
			}
		}
		if steps[keep].Proposed < result {
			result = steps[keep].Proposed
			steps[keep].Used = true
			if out.UpScore > 0 {
				rule = fmt.Sprintf("%q scales down (the scale-downs outweigh the scale-ups, %.2f to %.2f; the one that removes the fewest replicas)", steps[keep].Name, out.DownScore, out.UpScore)
			} else {
				rule = fmt.Sprintf("%q scales down (the one that removes the fewest replicas)", steps[keep].Name)
			}
		}
		for _, i := range downs {
			if !steps[i].Used && steps[i].Why == "" {
				steps[i].Why = "another scale-down removes fewer replicas, or the count is already lower"
			}
		}
	}
	if result < 1 {
		result = 1
	}
	clamped := ""
	if lo > 0 && result < lo {
		result, clamped = lo, fmt.Sprintf("; raised to the minimum %d", lo)
	}
	if hi > 0 && result > hi {
		result, clamped = hi, fmt.Sprintf("; capped at the maximum %d", hi)
	}
	out.Steps, out.Result, out.Rule = steps, result, rule+clamped
	used := ""
	for _, s := range steps {
		if s.Used {
			used = s.Name
			break
		}
	}
	if used == "" || (f.Set && result == base) {
		// nothing was asked for that changes the count: what the other strategies decided stands
		out.Result = base
		if used == "" {
			out.Rule = "no trigger changed the count"
		}
		return f, out
	}
	return Field{Set: true, Value: int64(result), Source: SourceTrigger, Detail: used, Clamped: clamped != "",
		Reason: fmt.Sprintf("metric trigger: %s (replicas before %d, running %d)%s", rule, base, current, clamped)}, out
}
