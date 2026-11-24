package overprovision

import (
	"fmt"
	"math"
	"strings"
)

// The recommendation has two ways to reach a use that fits the Requests, and they are worked out apart:
//
//   - the replicas are kept (what the module has now) and the Requests and Limits of one Pod are changed: this is the recommendation of Analyze;
//   - the Pod is kept (the Requests it has now) and the replicas are changed: this is PlanReplicas.
//
// The replicas come first: a change of the number of replicas touches no Pod spec and the load is shared again by itself, so when it can bring the use to where the
// bands want it, it is what is recommended; the Pod spec is the recommendation only when the replicas cannot do it (they are at their floor, or the use does not
// ask for another number). The plan assumes the load is shared evenly by the Pods and the use of a Pod goes as the replicas go the other way: the sum over the Pods is
// what is divided. A use that has a part every Pod pays (a JVM heap that is the same on a Pod of any size) does not follow it, and the plan says so in its formula.

// Directions of a replica plan.
const (
	ScaleOut     = "out"     // more replicas than now
	ScaleIn      = "in"      // fewer replicas than now
	ScaleSame    = "same"    // the replicas now are what the use asks
	ScaleUnknown = "unknown" // the replicas now are not known, or the use of all the Pods is not
)

// What a recommendation changes first.
const (
	PrimaryReplicas = "replicas" // the number of replicas, the Pod spec stays
	PrimaryConfig   = "config"   // the Requests and Limits of a Pod: the replicas cannot bring the use to its range
	PrimaryNone     = "none"     // nothing needs to change
)

// Codes of the reasons of a replica plan (the page words them).
const (
	ReasonReplicasNoCurrent = "replicas_no_current" // the replicas now are not known: the plan says how many, not by how many
	ReasonReplicasNoTotal   = "replicas_no_total"   // the use of all the Pods together was not measured: no plan
	ReasonReplicasFloor     = "replicas_floor"      // the plan is not below the module's least replicas
	ReasonReplicasCeiling   = "replicas_ceiling"    // the plan is not above the module's most replicas
	ReasonReplicasStep      = "replicas_step"       // the module lets one action add or remove only so many replicas: ThisRound is short of Needed
	ReasonReplicasLimited   = "replicas_limited"    // OOM or throttling: the use understates the need, so no replica is taken away
	ReasonReplicasLimit     = "replicas_limit"      // the P99 use of a Pod would come near its Limits with fewer replicas
)

// ReplicaLimits are the constraints the module itself sets on its replicas (module management): the least and the most replicas and the most one action adds or removes
// (0 = not set). The plan keeps to them and has no numbers of its own for the replicas.
type ReplicaLimits struct {
	Min           int `json:"min"`
	Max           int `json:"max"`
	ScaleUpStep   int `json:"scale_up_step"`
	ScaleDownStep int `json:"scale_down_step"`
}

// ReplicaPart is the replicas one percentile of the use asks of: the sum of the use of all the Pods at that percentile divided by the Requests of a Pod and the share of it
// the percentile is wanted to take.
type ReplicaPart struct {
	Name string `json:"name"`
	// Total is the sum of the use of all the Pods at the percentile as it counts (held for the sustain window, see Sustained); Raw is the same percentile of the sum as it was
	// measured moment by moment. They are one figure when the use is not held.
	Total  float64 `json:"total"`
	Raw    float64 `json:"raw"`
	Target float64 `json:"target"`
	Needed float64 `json:"needed"`
	// Counted is whether the part took part in the count of the plan it is in (a plan of one caliber counts the part of that percentile only).
	Counted bool `json:"counted"`
}

// ReplicaNeed is what one resource asks of the number of replicas when the Pod keeps the Requests it has.
type ReplicaNeed struct {
	Resource string `json:"resource"`
	// Needed is the replicas that put the P90, P95 and P99 of the sum of the use at or under the share of the Requests their bands want, and (with a Limits) the P99
	// under the share of the Limits the risk allows.
	Needed int `json:"needed"`
	// Request and Limit are those of one Pod it was worked out for (Limit 0 = not known), Parts the replicas each percentile asks, and LimitNeeded what the guard of the
	// Limits asks (0 = no Limits).
	Request     float64       `json:"request"`
	Limit       float64       `json:"limit"`
	Parts       []ReplicaPart `json:"parts"`
	LimitNeeded float64       `json:"limit_needed"`
	// Formula writes it out with the numbers put into it.
	Formula string `json:"formula"`
}

// ReplicaPlan is the recommendation of the replicas for the Pod the module has now.
type ReplicaPlan struct {
	// Current is the replicas the cluster has now (0 = not known); Needed is the recommended number (0 = no plan); Raw is the number the use asks for before the
	// floor, the step and the guards of OOM and throttling.
	Current int `json:"current"`
	Needed  int `json:"needed"`
	Raw     int `json:"raw"`
	// Direction is out, in, same or unknown; Delta is Needed - Current (negative: fewer) and Share is |Delta| / Current.
	Direction string  `json:"direction"`
	Delta     int     `json:"delta"`
	Share     float64 `json:"share"`
	// Binding is the resource whose need decides the number.
	Binding string `json:"binding"`
	// Resources are the need of each resource apart.
	Resources []ReplicaNeed `json:"resources"`
	// ThisRound is the count one action of the operator reaches: Needed held to the most replicas one action may add or remove (the pace of the module). It differs from Needed
	// only while the pace is what holds it back (reason replicas_step), and then Needed is reached in the next actions; Needed is the recommendation.
	ThisRound int `json:"this_round"`
	// Primary is what to change first: the replicas, or (when they cannot do it) the Pod spec, or nothing.
	Primary string `json:"primary"`
	// Limits are the constraints of the module the plan was held to.
	Limits  ReplicaLimits `json:"limits"`
	Reasons []string      `json:"reasons"`
	Formula string        `json:"formula"`
	// Caliber says which percentiles the plan was made of: "" for the three together (the largest need decides), or P90, P95 or P99 for that one alone.
	Caliber string `json:"caliber"`
	// ByCaliber is the plan made of one percentile at a time (P90, P95, P99), each held to the same bounds and guards; only the plan of the three together has it.
	ByCaliber map[string]*ReplicaPlan `json:"by_caliber,omitempty"`
}

// Calibers are the percentiles a plan can be made of alone.
var Calibers = []string{"P90", "P95", "P99"}

// PlanReplicas works out how many replicas the module needs if every Pod keeps the Requests it has, within the constraints the module sets (lim). current is the replicas the
// cluster has now (0 = not known); the results are those of Analyze for the CPU and the memory (either may be nil). The use of all the Pods together (Result.Total) is what is divided by the Requests of a Pod.
func PlanReplicas(current int, p Params, lim ReplicaLimits, results ...*Result) *ReplicaPlan {
	plan := planReplicas(current, p, lim, "", results...)
	plan.ByCaliber = map[string]*ReplicaPlan{}
	for _, name := range Calibers {
		plan.ByCaliber[name] = planReplicas(current, p, lim, name, results...)
	}
	return plan
}

// planReplicas is PlanReplicas for the three percentiles together (only = "") or for one of them alone.
func planReplicas(current int, p Params, lim ReplicaLimits, only string, results ...*Result) *ReplicaPlan {
	plan := &ReplicaPlan{Current: max(current, 0), Direction: ScaleUnknown, Primary: PrimaryNone, Resources: []ReplicaNeed{}, Reasons: []string{}, Limits: lim, Caliber: only}
	limited, needsChange := false, false
	var parts []string
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Status == StatusLimited {
			limited = true
		}
		if r.Status == StatusOver || r.Status == StatusUnder {
			needsChange = true
		}
		if !(r.Request > 0) || r.Total == nil || r.Total.Points == 0 || r.Suggested <= 0 {
			continue
		}
		need := replicasFor(r, p, only)
		plan.Resources = append(plan.Resources, need)
		parts = append(parts, need.Formula)
		if need.Needed > plan.Raw {
			plan.Raw, plan.Binding = need.Needed, r.Resource
		}
	}
	if plan.Raw == 0 {
		plan.Reasons = append(plan.Reasons, ReasonReplicasNoTotal)
		return plan
	}
	plan.Formula = strings.Join(parts, "; ")
	plan.Needed = plan.Raw
	// the module's own bounds: not below its least replicas, not above its most
	if lim.Min > 0 && plan.Needed < lim.Min {
		plan.Needed = lim.Min
		plan.Reasons = append(plan.Reasons, ReasonReplicasFloor)
	}
	if lim.Max > 0 && plan.Needed > lim.Max {
		plan.Needed = lim.Max
		plan.Reasons = append(plan.Reasons, ReasonReplicasCeiling)
	}
	if plan.Current <= 0 {
		// the replicas now are not known: the plan is how many the use asks for within the bounds, and not by how many it moves
		plan.Reasons = append(plan.Reasons, ReasonReplicasNoCurrent)
		plan.ThisRound = plan.Needed
		return plan
	}
	switch {
	case plan.Needed < plan.Current && limited:
		// the use understates the need under OOM or throttling: no replica is taken away
		plan.Needed = plan.Current
		plan.Reasons = append(plan.Reasons, ReasonReplicasLimited)
	}
	// the recommendation is the count the use asks for within the bounds and the protections; the pace of the module only says how many actions it takes to get there
	plan.ThisRound = plan.Needed
	switch {
	case plan.Needed < plan.Current:
		if lim.ScaleDownStep > 0 && plan.Current-plan.Needed > lim.ScaleDownStep {
			plan.ThisRound = plan.Current - lim.ScaleDownStep
			plan.Reasons = append(plan.Reasons, ReasonReplicasStep)
		}
	case plan.Needed > plan.Current:
		if lim.ScaleUpStep > 0 && plan.Needed-plan.Current > lim.ScaleUpStep {
			plan.ThisRound = plan.Current + lim.ScaleUpStep
			plan.Reasons = append(plan.Reasons, ReasonReplicasStep)
		}
	}
	for _, need := range plan.Resources {
		if need.LimitNeeded > 0 && int(math.Ceil(need.LimitNeeded-1e-9)) == plan.Raw && need.Needed == plan.Raw && plan.Raw > 0 && plan.Needed == plan.Raw && !containsReason(plan.Reasons, ReasonReplicasLimit) {
			// the Limits is what asked for the most replicas
			plan.Reasons = append(plan.Reasons, ReasonReplicasLimit)
		}
	}
	plan.Delta = plan.Needed - plan.Current
	plan.Share = math.Abs(float64(plan.Delta)) / float64(plan.Current)
	switch {
	case plan.Delta > 0:
		plan.Direction = ScaleOut
	case plan.Delta < 0:
		plan.Direction = ScaleIn
	default:
		plan.Direction = ScaleSame
	}
	switch {
	case plan.Direction != ScaleSame:
		plan.Primary = PrimaryReplicas
	case needsChange:
		// the replicas are what the use asks for, or cannot be moved (the floor, the step, OOM): the Pod spec is what is left to change
		plan.Primary = PrimaryConfig
	}
	return plan
}

// replicasFor is how many replicas one resource asks of: the sum of the use of the Pods at each of P90, P95 and P99 divided by what the band wants of the Requests of
// a Pod, and the P99 under the share of the Limits the peak guard allows.
func replicasFor(r *Result, p Params, only string) ReplicaNeed {
	out := ReplicaNeed{Resource: r.Resource, Request: r.Request, Limit: r.Limit, Parts: []ReplicaPart{}}
	var parts []string
	need := 0.0
	held := r.Total // the sums as they count: held for the sustain window when there is one
	if r.TotalSustained != nil {
		held = r.TotalSustained
	}
	for _, c := range []struct {
		name  string
		total float64
		share float64
		raw   float64
	}{{"P90", held.P90, p.P90.Target, r.Total.P90}, {"P95", held.P95, p.P95.Target, r.Total.P95}, {"P99", held.P99, p.P99.Target, r.Total.P99}} {
		if !(c.share > 0) {
			continue
		}
		n := c.total / (r.Request * c.share)
		counted := only == "" || only == c.name
		if counted {
			need = math.Max(need, n)
		}
		out.Parts = append(out.Parts, ReplicaPart{Name: c.name, Total: c.total, Raw: c.raw, Target: c.share, Needed: n, Counted: counted})
		parts = append(parts, fmt.Sprintf("%s %s / (%s x %.0f%%) = %.2f", c.name, num(r.Resource, c.total), num(r.Resource, r.Request), c.share*100, n))
	}
	out.Formula = r.Resource + " replicas = max(" + strings.Join(parts, ", ") + ")"
	if r.Limit > 0 && p.PeakNearLimit > 0 {
		n := r.Total.P99 / (r.Limit * p.PeakNearLimit)
		out.LimitNeeded = n
		if n > need {
			out.Formula += fmt.Sprintf(", limit guard: P99 %s / (limit %s x %.0f%%) = %.2f", num(r.Resource, r.Total.P99), num(r.Resource, r.Limit), p.PeakNearLimit*100, n)
		}
		need = math.Max(need, n)
	}
	out.Needed = max(int(math.Ceil(need-1e-9)), 1)
	return out
}
