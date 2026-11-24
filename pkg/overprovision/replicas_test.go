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
	plan := PlanReplicas(12, p, ReplicaLimits{}, cpu, mem)
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

func TestTheReplicasKeepToTheConstraintsOfTheModule(t *testing.T) {
	p := DefaultParams()
	use := Usage{Points: 100, P90: 6, P95: 8, P99: 9}
	// 8 / (4 x 85%) = 2.35 -> 3 replicas of 12; the module sets no bound: the plan is what the use asks
	plan := PlanReplicas(12, p, ReplicaLimits{}, result(CPU, 4, 8, use, StatusOver))
	assert.Equal(t, 3, plan.Raw)
	assert.Equal(t, 3, plan.Needed)
	assert.Equal(t, ScaleIn, plan.Direction)
	assert.Equal(t, -9, plan.Delta)

	// the module's least replicas
	plan = PlanReplicas(12, p, ReplicaLimits{Min: 5}, result(CPU, 4, 8, use, StatusOver))
	assert.Equal(t, 5, plan.Needed)
	assert.Contains(t, plan.Reasons, ReasonReplicasFloor)

	// the most one action may remove
	plan = PlanReplicas(12, p, ReplicaLimits{ScaleDownStep: 2}, result(CPU, 4, 8, use, StatusOver))
	assert.Equal(t, 10, plan.Needed)
	assert.Contains(t, plan.Reasons, ReasonReplicasStep)

	// the most replicas, and the most one action may add
	big := Usage{Points: 100, P90: 60, P95: 80, P99: 90}
	plan = PlanReplicas(12, p, ReplicaLimits{Max: 20}, result(CPU, 4, 8, big, StatusUnder))
	assert.Equal(t, 20, plan.Needed)
	assert.Contains(t, plan.Reasons, ReasonReplicasCeiling)
	plan = PlanReplicas(12, p, ReplicaLimits{ScaleUpStep: 3}, result(CPU, 4, 8, big, StatusUnder))
	assert.Equal(t, 15, plan.Needed)
	assert.Equal(t, ScaleOut, plan.Direction)

	// nothing of it is the platform's own: no bound is set, none is applied (a module that has one replica stays at one when the use asks for one)
	plan = PlanReplicas(1, p, ReplicaLimits{}, result(CPU, 4, 8, Usage{Points: 100, P90: 1, P95: 1, P99: 1.2}, StatusOver))
	assert.Equal(t, 1, plan.Needed)
	assert.Equal(t, ScaleSame, plan.Direction)
	assert.Equal(t, PrimaryConfig, plan.Primary, "the replicas cannot move: the Pod spec is what is left")
	assert.Equal(t, 5, PlanReplicas(12, p, ReplicaLimits{Min: 5, Max: 5}, result(CPU, 4, 8, use, StatusOver)).Needed, "a module kept at a fixed number has no room")
}

func TestNoReplicaIsTakenAwayWhileTheUseUnderstatesTheNeed(t *testing.T) {
	p := DefaultParams()
	cpu := result(CPU, 4, 8, Usage{Points: 100, P90: 6, P95: 8, P99: 9}, StatusLimited)
	plan := PlanReplicas(12, p, ReplicaLimits{}, cpu)
	assert.Equal(t, 12, plan.Needed)
	assert.Equal(t, ScaleSame, plan.Direction)
	assert.Contains(t, plan.Reasons, ReasonReplicasLimited)
	assert.Equal(t, PrimaryNone, plan.Primary)
}

func TestThePeakOfAPodStaysUnderItsLimits(t *testing.T) {
	p := DefaultParams()
	// the sum P99 of 20 cores over Limits of 4 cores x 90%: 5.56 -> 6 replicas, more than the 4.55 -> 5 the Requests ask
	cpu := result(CPU, 4, 4, Usage{Points: 100, P90: 14, P95: 15, P99: 16}, StatusOver)
	plan := PlanReplicas(10, p, ReplicaLimits{}, cpu)
	assert.Equal(t, 5, plan.Raw)
	cpu.Total.P99 = 20
	plan = PlanReplicas(10, p, ReplicaLimits{}, cpu)
	assert.Equal(t, 6, plan.Raw)
	assert.Contains(t, plan.Formula, "limit guard")
}

func TestAPlanNeedsTheUseOfAllThePods(t *testing.T) {
	p := DefaultParams()
	plan := PlanReplicas(12, p, ReplicaLimits{}, &Result{Resource: CPU, Request: 4, Suggested: 3})
	assert.Equal(t, 0, plan.Needed)
	assert.Equal(t, ScaleUnknown, plan.Direction)
	assert.Contains(t, plan.Reasons, ReasonReplicasNoTotal)
	assert.Nil(t, nilPlanResources(PlanReplicas(12, p, ReplicaLimits{})))
}

func TestWithoutTheReplicasNowThePlanSaysHowManyAndNotByHowMany(t *testing.T) {
	p := DefaultParams()
	plan := PlanReplicas(0, p, ReplicaLimits{}, result(CPU, 4, 8, Usage{Points: 100, P90: 26.674, P95: 33.032, P99: 38.438}, StatusOver))
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

// Each percentile is a caliber of its own: the plan of one percentile counts only that percentile (and the Limits guard), the three together take the largest, and every
// one is held to the same bounds.
func TestEveryCaliberHasAPlanOfItsOwn(t *testing.T) {
	p := DefaultParams()
	use := Usage{Points: 100, P90: 6, P95: 8, P99: 9}
	plan := PlanReplicas(12, p, ReplicaLimits{Min: 2}, result(CPU, 4, 0, use, StatusOver))
	// P90 6 / (4 x 80%) = 1.88 -> 2; P95 8 / (4 x 85%) = 2.35 -> 3; P99 9 / (4 x 110%) = 2.05 -> 3
	assert.Equal(t, 3, plan.Needed, "the three together: the largest")
	require.Len(t, plan.ByCaliber, 3)
	assert.Equal(t, 2, plan.ByCaliber["P90"].Needed)
	assert.Equal(t, 3, plan.ByCaliber["P95"].Needed)
	assert.Equal(t, 3, plan.ByCaliber["P99"].Needed)
	assert.Equal(t, "P90", plan.ByCaliber["P90"].Caliber)
	assert.Equal(t, ScaleIn, plan.ByCaliber["P90"].Direction)
	assert.Nil(t, plan.ByCaliber["P90"].ByCaliber, "only the plan of the three together has the others")
	for _, part := range plan.ByCaliber["P90"].Resources[0].Parts {
		assert.Equal(t, part.Name == "P90", part.Counted, part.Name)
	}
	// the same bounds hold for every caliber
	bounded := PlanReplicas(12, p, ReplicaLimits{Min: 5}, result(CPU, 4, 0, use, StatusOver))
	for name, one := range bounded.ByCaliber {
		assert.Equal(t, 5, one.Needed, name)
	}
}

// The request each percentile asks for alone is listed, and the one that asks for the most is the one the recommendation rests on.
func TestEveryPercentileHasItsOwnRequest(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	r := Analyze(input(CPU, 4, ramp(100, 0.05)), p)
	require.Len(t, r.Candidates, 3)
	// the use of the ramp: P90 4.5, P95 4.75, P99 4.95 cores: 4.5 / 0.8 = 5.6, 4.75 / 0.85 = 5.59, 4.95 / 1.1 = 4.5
	assert.Equal(t, "P90", r.Candidates[0].Name)
	assert.True(t, r.Candidates[0].Decides)
	assert.False(t, r.Candidates[1].Decides)
	assert.InDelta(t, 4.5/0.8, r.Candidates[0].Needed, 1e-9)
	assert.InDelta(t, 4.95/1.1, r.Candidates[2].Needed, 1e-9)
	assert.GreaterOrEqual(t, r.Candidates[0].SuggestedLimit, r.Candidates[0].Suggested)
}
