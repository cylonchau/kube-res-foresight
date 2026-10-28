package controller

import (
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var now = time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

func spec() v1alpha1.PredictiveScalerSpec {
	return v1alpha1.PredictiveScalerSpec{MinReplicas: 2, MaxReplicas: 20}
}

func fresh(replicas int32) *Decision {
	valid := now.Add(5 * time.Minute)
	return &Decision{Replicas: replicas, Source: "platform", Version: 3, ValidUntil: &valid}
}

func in(current, ready int32, d *Decision) PlanInput {
	return PlanInput{Now: now, Spec: spec(), Current: current, Ready: ready, Decision: d}
}

func TestFreshDecisionIsFollowedBothWays(t *testing.T) {
	up := MakePlan(in(4, 4, fresh(9)))
	assert.Equal(t, ActionUp, up.Action)
	assert.EqualValues(t, 9, up.Desired)
	assert.True(t, up.DecisionValid)
	down := MakePlan(in(9, 9, fresh(4)))
	assert.Equal(t, ActionDown, down.Action)
	assert.EqualValues(t, 4, down.Desired)
	same := MakePlan(in(5, 5, fresh(5)))
	assert.Equal(t, ActionNone, same.Action)
}

func TestDecisionIsClampedToTheHardBounds(t *testing.T) {
	assert.EqualValues(t, 20, MakePlan(in(10, 10, fresh(500))).Desired)
	assert.EqualValues(t, 2, MakePlan(in(10, 10, fresh(0))).Desired)
}

func TestMissingOrExpiredDecisionFreezesInsteadOfScalingDown(t *testing.T) {
	p := MakePlan(in(10, 10, nil))
	assert.Equal(t, ActionNone, p.Action)
	assert.EqualValues(t, 10, p.Desired)
	assert.Equal(t, "frozen", p.UsedSource)
	assert.False(t, p.DecisionValid)

	past := now.Add(-time.Second)
	p = MakePlan(in(10, 10, &Decision{Replicas: 3, ValidUntil: &past}))
	assert.Equal(t, ActionNone, p.Action, "an expired decision to shrink is ignored")
	assert.Contains(t, p.Reason, "expired")

	pi := in(10, 10, nil)
	pi.DecisionErr = "connection refused"
	assert.Contains(t, MakePlan(pi).Reason, "connection refused")
}

func TestFallbackRaisesButNeverLowersReplicas(t *testing.T) {
	fallback := int32(6)
	pi := in(4, 4, nil)
	pi.Spec.FallbackReplicas = &fallback
	p := MakePlan(pi)
	assert.Equal(t, ActionUp, p.Action)
	assert.EqualValues(t, 6, p.Desired)
	assert.Equal(t, "fallback", p.UsedSource)

	pi = in(10, 10, nil)
	pi.Spec.FallbackReplicas = &fallback
	p = MakePlan(pi)
	assert.Equal(t, ActionNone, p.Action, "a fallback below the current replicas does not scale down")
	assert.EqualValues(t, 10, p.Desired)
}

func TestHardBoundsAreEnforcedEvenWithoutAFreshDecision(t *testing.T) {
	p := MakePlan(in(30, 30, nil))
	assert.Equal(t, ActionDown, p.Action)
	assert.EqualValues(t, 20, p.Desired, "above the maximum is always corrected")
	p = MakePlan(in(1, 1, nil))
	assert.Equal(t, ActionUp, p.Action)
	assert.EqualValues(t, 2, p.Desired)
}

func TestScaleDownIsHeldWhileReplicasAreNotReady(t *testing.T) {
	p := MakePlan(in(10, 7, fresh(4)))
	assert.Equal(t, ActionNone, p.Action)
	assert.EqualValues(t, 10, p.Desired)
	assert.Contains(t, p.Blocked, "7 of 10")
	up := MakePlan(in(10, 7, fresh(14)))
	assert.Equal(t, ActionUp, up.Action, "scaling up is never held by unready pods")

	over := MakePlan(in(30, 20, nil))
	assert.Equal(t, ActionDown, over.Action, "exceeding the maximum overrides the readiness hold")
}

func TestCooldownHoldsAThatDirectionOnly(t *testing.T) {
	last := now.Add(-30 * time.Second)
	pi := in(4, 4, fresh(9))
	pi.LastScale = &last
	pi.Spec.Behavior.ScaleUp.Cooldown = &metav1.Duration{Duration: time.Minute}
	p := MakePlan(pi)
	assert.Equal(t, ActionNone, p.Action)
	assert.Contains(t, p.Blocked, "cooldown")

	pi.Spec.Behavior.ScaleUp.Cooldown = &metav1.Duration{Duration: 10 * time.Second}
	assert.Equal(t, ActionUp, MakePlan(pi).Action, "the cooldown has passed")

	pi = in(9, 9, fresh(4))
	pi.LastScale = &last
	pi.Spec.Behavior.ScaleUp.Cooldown = &metav1.Duration{Duration: time.Hour}
	assert.Equal(t, ActionDown, MakePlan(pi).Action, "the up cooldown does not hold a scale-down")
}

func TestCooldownDoesNotBlockBoundsEnforcement(t *testing.T) {
	last := now.Add(-time.Second)
	pi := in(30, 30, fresh(10))
	pi.LastScale = &last
	pi.Spec.Behavior.ScaleDown.Cooldown = &metav1.Duration{Duration: time.Hour}
	p := MakePlan(pi)
	assert.Equal(t, ActionDown, p.Action)
}

func TestMaxStepLimitsEachAction(t *testing.T) {
	pi := in(4, 4, fresh(20))
	pi.Spec.Behavior.ScaleUp.MaxStep = 3
	p := MakePlan(pi)
	assert.EqualValues(t, 7, p.Desired)
	assert.Contains(t, p.Reason, "3 replicas per action")

	pi = in(12, 12, fresh(2))
	pi.Spec.Behavior.ScaleDown.MaxStep = 4
	assert.EqualValues(t, 8, MakePlan(pi).Desired)

	pi = in(4, 4, fresh(6))
	pi.Spec.Behavior.ScaleUp.MaxStep = 3
	assert.EqualValues(t, 6, MakePlan(pi).Desired, "a smaller change is not stretched")
}

func TestConflictMeansTheOperatorDoesNothing(t *testing.T) {
	pi := in(4, 4, fresh(9))
	pi.Conflict = "HorizontalPodAutoscaler api also targets Deployment api"
	p := MakePlan(pi)
	assert.Equal(t, ActionNone, p.Action)
	assert.EqualValues(t, 4, p.Desired)
	assert.Contains(t, p.Blocked, "HorizontalPodAutoscaler")
}

func TestStaticDecisionNeverExpires(t *testing.T) {
	p := MakePlan(in(4, 4, &Decision{Replicas: 8, Source: "static"}))
	assert.True(t, p.DecisionValid)
	assert.EqualValues(t, 8, p.Desired)
}

func TestThePlatformsHardBoundsHoldEvenWhenTheSpecIsWider(t *testing.T) {
	d := fresh(18)
	d.HardMaxReplicas, d.MinReplicas = 12, 5
	p := MakePlan(in(8, 8, d))
	assert.EqualValues(t, 12, p.Desired, "the safety ceiling of the platform is not passed")
	assert.Contains(t, p.Reason, "hard maximum")

	d = fresh(3)
	d.MinReplicas = 5
	p = MakePlan(in(8, 8, d))
	assert.EqualValues(t, 5, p.Desired, "the platform's floor is kept")
	assert.Contains(t, p.Reason, "hard minimum")

	// an expired decision does not carry the bounds along: it freezes as before
	d = fresh(18)
	d.HardMaxReplicas = 12
	expired := now.Add(-time.Minute)
	d.ValidUntil = &expired
	assert.False(t, MakePlan(in(8, 8, d)).DecisionValid)
}

func TestAnUrgentScaleUpIgnoresTheCooldownAndTheStepLimit(t *testing.T) {
	s := spec()
	s.Behavior.ScaleUp = v1alpha1.ScaleRule{MaxStep: 2, Cooldown: &metav1.Duration{Duration: 5 * time.Minute}}
	last := now.Add(-time.Minute)
	pi := PlanInput{Now: now, Spec: s, Current: 4, Ready: 4, LastScale: &last, Decision: fresh(14)}
	held := MakePlan(pi)
	assert.Equal(t, ActionNone, held.Action, "a normal scale-up waits for its cooldown")

	pi.Decision.Urgent = true
	p := MakePlan(pi)
	assert.Equal(t, ActionUp, p.Action)
	assert.EqualValues(t, 14, p.Desired, "all the way, not by the step")
	assert.Contains(t, p.Reason, "urgent")

	// urgency never loosens a scale-down
	pi.Current, pi.Ready, pi.Decision = 14, 14, &Decision{Replicas: 4, Urgent: true, ValidUntil: fresh(4).ValidUntil}
	pi.Spec.Behavior.ScaleDown = v1alpha1.ScaleRule{MaxStep: 1}
	assert.EqualValues(t, 13, MakePlan(pi).Desired)
}
