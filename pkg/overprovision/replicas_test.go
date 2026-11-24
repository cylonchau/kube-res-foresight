package overprovision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// result is an Analyze result with the figures a plan reads: the Requests of a Pod, the Limits, and the use of all the Pods together.
func result(resource string, request, limit float64, total Usage, status string) *Result {
	return &Result{Resource: resource, Request: request, Limit: limit, Total: &total, Suggested: request, Status: status}
}

func TestReplicasAreWorkedOutFromTheSumOfTheUse(t *testing.T) {
	p := DefaultParams()
	// the figures of the module of the report: 12 Pods of 4 cores and 8 GiB, the sum of the use P90 26.67, P95 33.03 and P99 38.44 cores, and 9.6 / 10.2 / 11 GiB a Pod
	cpu := result(CPU, 4, 8, Usage{Points: 100, P90: 26.674, P95: 33.032, P99: 38.438}, StatusOver)
	gib := float64(1 << 30)
	mem := result(Memory, 8*gib, 16*gib, Usage{Points: 100, P90: 12 * 7.95 * gib, P95: 12 * 8.18 * gib, P99: 12 * 8.69 * gib}, StatusUnder)
	plan := PlanReplicas(12, p, cpu, mem)
	require.NotNil(t, plan)
	byResource := map[string]int{}
	for _, n := range plan.Resources {
		byResource[n.Resource] = n.Needed
	}
	// CPU: P95 33.032 / (4 x 85%) = 9.72 -> 10; memory: P95 98.2 / (8 x 85%) = 14.4 -> 15: the memory decides
	assert.Equal(t, 10, byResource[CPU])
	assert.Equal(t, 15, byResource[Memory])
	assert.Equal(t, 15, plan.Needed)
	assert.Equal(t, Memory, plan.Binding)
	assert.Equal(t, ScaleOut, plan.Direction)
	assert.Equal(t, 3, plan.Delta)
	assert.InDelta(t, 0.25, plan.Share, 1e-9)
	assert.Equal(t, PrimaryReplicas, plan.Primary, "the replicas come before the Pod spec")
	assert.Contains(t, plan.Formula, "P95")
}

func TestAScaleInIsHeldToTheFloorAndTheStep(t *testing.T) {
	p := DefaultParams()
	cpu := result(CPU, 4, 8, Usage{Points: 100, P90: 6, P95: 8, P99: 9}, StatusOver)
	// 8 / (4 x 85%) = 2.35 -> 3 replicas of 12: a step of at most half of them keeps 6
	plan := PlanReplicas(12, p, cpu)
	assert.Equal(t, 3, plan.Raw)
	assert.Equal(t, 6, plan.Needed)
	assert.Equal(t, ScaleIn, plan.Direction)
	assert.Equal(t, -6, plan.Delta)
	assert.Contains(t, plan.Reasons, ReasonReplicasStep)

	// no limit on the step: the floor (2) is what holds it
	p.MaxScaleIn = 1
	plan = PlanReplicas(12, p, result(CPU, 4, 8, Usage{Points: 100, P90: 1, P95: 1, P99: 1.2}, StatusOver))
	assert.Equal(t, 2, plan.Needed)
	assert.Contains(t, plan.Reasons, ReasonReplicasFloor)

	// a module that has one replica is not raised to the floor
	plan = PlanReplicas(1, p, result(CPU, 4, 8, Usage{Points: 100, P90: 1, P95: 1, P99: 1.2}, StatusOver))
	assert.Equal(t, 1, plan.Needed)
	assert.Equal(t, ScaleSame, plan.Direction)
	assert.Equal(t, PrimaryConfig, plan.Primary, "the replicas cannot move: the Pod spec is what is left")
}

func TestNoReplicaIsTakenAwayWhileTheUseUnderstatesTheNeed(t *testing.T) {
	p := DefaultParams()
	cpu := result(CPU, 4, 8, Usage{Points: 100, P90: 6, P95: 8, P99: 9}, StatusLimited)
	plan := PlanReplicas(12, p, cpu)
	assert.Equal(t, 12, plan.Needed)
	assert.Equal(t, ScaleSame, plan.Direction)
	assert.Contains(t, plan.Reasons, ReasonReplicasLimited)
	assert.Equal(t, PrimaryNone, plan.Primary)
}

func TestThePeakOfAPodStaysUnderItsLimits(t *testing.T) {
	p := DefaultParams()
	// the sum P99 of 20 cores over Limits of 4 cores x 90%: 5.56 -> 6 replicas, more than the 4.55 -> 5 the Requests ask
	cpu := result(CPU, 4, 4, Usage{Points: 100, P90: 14, P95: 15, P99: 16}, StatusOver)
	plan := PlanReplicas(10, p, cpu)
	assert.Equal(t, 5, plan.Raw)
	cpu.Total.P99 = 20
	plan = PlanReplicas(10, p, cpu)
	assert.Equal(t, 6, plan.Raw)
	assert.Contains(t, plan.Formula, "limit guard")
}

func TestAPlanNeedsTheUseOfAllThePods(t *testing.T) {
	p := DefaultParams()
	plan := PlanReplicas(12, p, &Result{Resource: CPU, Request: 4, Suggested: 3})
	assert.Equal(t, 0, plan.Needed)
	assert.Equal(t, ScaleUnknown, plan.Direction)
	assert.Contains(t, plan.Reasons, ReasonReplicasNoTotal)
	assert.Nil(t, nilPlanResources(PlanReplicas(12, p)))
}

func TestWithoutTheReplicasNowThePlanSaysHowManyAndNotByHowMany(t *testing.T) {
	p := DefaultParams()
	plan := PlanReplicas(0, p, result(CPU, 4, 8, Usage{Points: 100, P90: 26.674, P95: 33.032, P99: 38.438}, StatusOver))
	assert.Equal(t, 10, plan.Needed)
	assert.Equal(t, ScaleUnknown, plan.Direction)
	assert.Zero(t, plan.Delta)
	assert.Contains(t, plan.Reasons, ReasonReplicasNoCurrent)
}

// nilPlanResources is nil when the plan has no resource at all (a module with neither the CPU nor the memory analysed).
func nilPlanResources(plan *ReplicaPlan) []ReplicaNeed {
	if len(plan.Resources) == 0 {
		return nil
	}
	return plan.Resources
}
