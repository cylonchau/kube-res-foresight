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

// FiredTrigger is a trigger whose metric stands on the wrong side of its threshold now and that is in force at this time: what it saw, and what it asks for.
type FiredTrigger struct {
	Name     string  `json:"name"`
	Priority int     `json:"priority"`
	Action   string  `json:"action"`
	Unit     string  `json:"unit"` // replicas or percent
	Amount   float64 `json:"amount"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Why      string  `json:"why"`
}

// TriggerStep is one fired trigger in the outcome: the replica count it asks for and whether it counted.
type TriggerStep struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Priority int    `json:"priority"`
	Proposed int    `json:"proposed"`
	Used     bool   `json:"used"`
	Why      string `json:"why"`
}

// TriggerOutcome is how the triggers and the rest of the strategies were combined: the replicas before, after, and the rule that decided.
type TriggerOutcome struct {
	Base    int           `json:"base"`
	Current int           `json:"current"`
	Result  int           `json:"result"`
	Rule    string        `json:"rule"`
	Steps   []TriggerStep `json:"steps"`
}

// ApplyTriggers combines the metric triggers that fired with the replica count the other strategies decided (f: the forecast, the module's fallback or fixed count),
// by these rules, in this order:
//
//  1. A bypass, a fixed period and a fixed count are the administrator's word: the triggers do not touch them.
//  2. A trigger that sets a count is followed; when several set one, the highest priority wins (equal priorities: the larger count, for safety).
//  3. A trigger that scales up asks for the running count plus its amount; the largest of them is a floor under whatever was decided: a scale-up is never cut by a
//     set, a forecast or a scale-down.
//  4. A trigger that scales down asks for the running count minus its amount. It acts only when nothing above fired, when no protection blocks it (blockDown says
//     why: an OOM kill, a heap under pressure) and when the forecast does not want more replicas than run now; of several, the one that removes the fewest replicas
//     counts.
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
		if t.Unit == TriggerPercent {
			delta = int(math.Ceil(float64(current) * t.Amount / 100))
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
		steps[i] = TriggerStep{Name: t.Name, Action: t.Action, Priority: t.Priority, Proposed: n, Why: why}
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
			if len(sets) > 0 {
				rule += fmt.Sprintf("; raised by the scale-up %q (a scale-up is never cut by a set)", steps[top].Name)
			} else {
				rule = fmt.Sprintf("%q scales up (the largest of the scale-ups)", steps[top].Name)
			}
		}
		for _, i := range ups {
			if !steps[i].Used {
				steps[i].Why = "another scale-up asks for more, or the count already reaches it"
			}
		}
	}
	if len(downs) > 0 {
		reason := ""
		switch {
		case len(ups) > 0 || len(sets) > 0:
			reason = "a scale-up or a set fired as well: a scale-down never acts against them"
		case blockDown != "":
			reason = "blocked by a protection: " + blockDown
		case current > 0 && base > current:
			reason = fmt.Sprintf("the forecast asks for %d replicas, more than the %d that run", base, current)
		}
		if reason != "" {
			for _, i := range downs {
				steps[i].Why = reason
			}
		} else {
			keep := downs[0] // the one that removes the fewest replicas
			for _, i := range downs {
				if steps[i].Proposed > steps[keep].Proposed {
					keep = i
				}
			}
			if steps[keep].Proposed < result {
				result = steps[keep].Proposed
				steps[keep].Used = true
				rule = fmt.Sprintf("%q scales down (the one that removes the fewest replicas)", steps[keep].Name)
			}
			for _, i := range downs {
				if !steps[i].Used {
					steps[i].Why = "another scale-down removes fewer replicas, or the count is already lower"
				}
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
