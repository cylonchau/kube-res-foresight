package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A module of fallback 4 that runs 20 Pods: whatever the state of the outage, the platform being away never takes the workload down to 4.
func TestAFallbackOf4NeverTakesARunningWorkloadOf20Down(t *testing.T) {
	fallback := int32(4)
	for _, state := range []string{"", StateHolding, StateDegraded} {
		for name, decision := range map[string]*Decision{"no answer": nil} {
			p := MakePlan(PlanInput{Now: now, Spec: Spec{MinReplicas: 2, MaxReplicas: 30, FallbackReplicas: &fallback}, Current: 20, Ready: 20,
				Decision: decision, DecisionErr: "platform unreachable", Degrade: state, FallbackCooldown: 5 * time.Minute})
			assert.Equal(t, ActionNone, p.Action, "state %q, %s", state, name)
			assert.EqualValues(t, 20, p.Desired, "state %q, %s", state, name)
		}
	}

	// the decision of the platform that is back is remembered or recovering: it can raise the replicas, never lower them
	remembered := fresh(4)
	remembered.Cached = true
	p := MakePlan(in(20, 20, remembered))
	assert.EqualValues(t, 20, p.Desired, "a remembered decision of 4 does not scale 20 down")
	recovering := fresh(4)
	recovering.Recovering = true
	p = MakePlan(in(20, 20, recovering))
	assert.EqualValues(t, 20, p.Desired, "the first answers of a platform that is back do not scale 20 down")

	// the platform that has been answering for the whole stable period decides: the scale-down is a step of the module and waits for the Pods to be Ready
	steady := in(20, 20, fresh(4))
	steady.Spec.Behavior.ScaleDown = ScaleRule{MaxStep: 2}
	p = MakePlan(steady)
	assert.Equal(t, ActionDown, p.Action)
	assert.EqualValues(t, 18, p.Desired, "a step of 2, not a jump to 4")
	unready := in(20, 17, fresh(4))
	assert.Equal(t, ActionNone, MakePlan(unready).Action, "17 of 20 Ready: the scale-down is held")
}
