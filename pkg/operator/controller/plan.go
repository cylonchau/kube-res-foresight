// Package controller holds the reconciler of kube-resource-fcst-operator. The scaling rules live in Plan, a pure
// function, so every safety rule can be tested without a cluster; the Reconciler only gathers its inputs and
// applies its result.
package controller

import (
	"fmt"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
)

// Decision is the desired replica count read from a decision source.
type Decision struct {
	Replicas   int32
	Source     string
	Version    int64
	ValidUntil *time.Time // nil = never expires (static)
	CreatedAt  *time.Time // when the source made the decision, nil when it does not say
	Reason     string
	// Algorithm and Score are the algorithm instance behind the decision and its score (platform decisions).
	Algorithm string
	Score     float64
	// MinReplicas is the hard floor and HardMaxReplicas the safety ceiling the platform holds the module to; 0 = not
	// stated. A decision outside them is pulled back, whatever the spec allows.
	MinReplicas     int32
	HardMaxReplicas int32
}

// Action is what a reconcile does to the target.
type Action string

const (
	ActionNone Action = "None"
	ActionUp   Action = "ScaleUp"
	ActionDown Action = "ScaleDown"
)

// PlanInput is everything the scaling rules depend on.
type PlanInput struct {
	Now       time.Time
	Spec      v1alpha1.PredictiveScalerSpec
	Current   int32
	Ready     int32
	LastScale *time.Time
	// Decision is nil when the source could not be read; DecisionErr says why.
	Decision    *Decision
	DecisionErr string
	// Conflict is non-empty when another autoscaler controls the same target.
	Conflict string
}

// Plan is the outcome of the rules.
type Plan struct {
	Desired       int32
	Action        Action
	DecisionValid bool
	UsedSource    string // "decision", "fallback" or "frozen"
	Reason        string
	Blocked       string // why a wanted change was held back (cooldown, not ready, conflict)
}

// MakePlan applies the scaling rules, in order:
//  1. A conflicting autoscaler means the operator does nothing.
//  2. A fresh decision is the target. A missing or expired decision uses the fallback, else freezes the
//     current replicas. Anything but a fresh decision never scales down.
//  3. The target is clamped to [min, max]; the bounds also apply when the current count is outside them.
//  4. Scaling down is held while not every replica is Ready, unless the current count exceeds the maximum.
//  5. A direction acts only after its cooldown; bounds enforcement is exempt.
//  6. A direction moves at most MaxStep replicas per action.

func MakePlan(in PlanInput) Plan {
	p := Plan{Desired: in.Current, Action: ActionNone, UsedSource: "frozen"}
	if in.Conflict != "" {
		p.Reason, p.Blocked = "another autoscaler controls this target", in.Conflict
		return p
	}
	spec := in.Spec

	target := in.Current
	switch {
	case in.Decision != nil && (in.Decision.ValidUntil == nil || in.Now.Before(*in.Decision.ValidUntil)):
		target, p.UsedSource, p.DecisionValid = in.Decision.Replicas, "decision", true
		p.Reason = fmt.Sprintf("decision from %s", in.Decision.Source)
	case in.Decision != nil:
		p.Reason = "decision expired"
	case in.DecisionErr != "":
		p.Reason = "decision unavailable: " + in.DecisionErr
	default:
		p.Reason = "no decision"
	}
	if !p.DecisionValid {
		if spec.FallbackReplicas != nil {
			target, p.UsedSource = *spec.FallbackReplicas, "fallback"
			p.Reason += "; using fallback replicas"
		} else {
			p.Reason += "; freezing the current replicas"
		}
		if target < in.Current {
			target = in.Current
			p.Reason += "; scale-down is never done without a fresh decision"
		}
	}

	// the platform's own hard floor and safety ceiling hold even when the spec is wider
	if p.DecisionValid {
		if hm := in.Decision.HardMaxReplicas; hm > 0 && target > hm {
			target = hm
			p.Reason += fmt.Sprintf("; limited to the platform's hard maximum of %d replicas", hm)
		}
		if lo := in.Decision.MinReplicas; lo > 0 && target < lo {
			target = lo
			p.Reason += fmt.Sprintf("; raised to the platform's hard minimum of %d replicas", lo)
		}
	}

	// hard bounds; an out-of-range current count is corrected even without a fresh decision
	boundsForced := false
	if target < spec.MinReplicas {
		target = spec.MinReplicas
	}
	if target > spec.MaxReplicas {
		target = spec.MaxReplicas
	}
	if in.Current > spec.MaxReplicas {
		boundsForced = true
	}
	if in.Current < spec.MinReplicas {
		boundsForced = true
	}

	switch {
	case target > in.Current:
		p.Action = ActionUp
	case target < in.Current:
		p.Action = ActionDown
	default:
		p.Desired = in.Current
		return p
	}

	if p.Action == ActionDown && in.Ready < in.Current && !(boundsForced && in.Current > spec.MaxReplicas) {
		p.Blocked = fmt.Sprintf("only %d of %d replicas are Ready: scale-down is held", in.Ready, in.Current)
		p.Desired, p.Action = in.Current, ActionNone
		return p
	}

	rule := spec.Behavior.ScaleUp
	if p.Action == ActionDown {
		rule = spec.Behavior.ScaleDown
	}
	if !boundsForced && rule.Cooldown != nil && in.LastScale != nil {
		if since := in.Now.Sub(*in.LastScale); since < rule.Cooldown.Duration {
			p.Blocked = fmt.Sprintf("cooldown: %s since the last scaling action, %s required", since.Round(time.Second), rule.Cooldown.Duration)
			p.Desired, p.Action = in.Current, ActionNone
			return p
		}
	}

	diff := target - in.Current
	if diff < 0 {
		diff = -diff
	}
	if rule.MaxStep > 0 && diff > rule.MaxStep {
		if p.Action == ActionUp {
			target = in.Current + rule.MaxStep
		} else {
			target = in.Current - rule.MaxStep
		}
		p.Reason += fmt.Sprintf("; limited to %d replicas per action", rule.MaxStep)
	}
	p.Desired = target
	return p
}
