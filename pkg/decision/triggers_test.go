package decision

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func algorithmField(n int64) Field {
	return Field{Set: true, Value: n, Source: SourceAlgorithm, Detail: "ets", Reason: "forecast"}
}

func scaleUp(name string, prio int, unit string, amount float64) FiredTrigger {
	return FiredTrigger{Name: name, Priority: prio, Action: TriggerScaleUp, Unit: unit, Amount: amount}
}
func scaleDown(name string, prio int, amount float64) FiredTrigger {
	return FiredTrigger{Name: name, Priority: prio, Action: TriggerScaleDown, Unit: "replicas", Amount: amount}
}
func set(name string, prio int, n float64) FiredTrigger {
	return FiredTrigger{Name: name, Priority: prio, Action: TriggerSet, Unit: "replicas", Amount: n}
}

func TestNoTriggerLeavesTheDecisionAlone(t *testing.T) {
	f := algorithmField(6)
	got, out := ApplyTriggers(f, 5, nil, "", 0, 0)
	assert.Equal(t, f, got)
	assert.Nil(t, out)
}

func TestAdministratorsFixedValuesAreNotChangedByATrigger(t *testing.T) {
	for _, src := range []Source{SourceBypass, SourceFixedPeriod, SourceFixedCount} {
		f := Field{Set: true, Value: 4, Source: src}
		got, out := ApplyTriggers(f, 4, []FiredTrigger{scaleUp("hot", 0, "replicas", 3)}, "", 0, 0)
		assert.Equal(t, f, got, string(src))
		assert.Equal(t, 4, out.Result)
		assert.False(t, out.Steps[0].Used)
	}
}

func TestScaleUpIsAFloorUnderTheForecast(t *testing.T) {
	// 5 run, the forecast says 6, the trigger wants 5 + 3 = 8
	got, out := ApplyTriggers(algorithmField(6), 5, []FiredTrigger{scaleUp("cpu-hot", 0, "replicas", 3)}, "", 0, 0)
	assert.EqualValues(t, 8, got.Value)
	assert.Equal(t, SourceTrigger, got.Source)
	assert.Equal(t, "cpu-hot", got.Detail)
	assert.True(t, out.Steps[0].Used)
	// the forecast already asks for more: the trigger does not lower it, and the forecast stays the source
	got, out = ApplyTriggers(algorithmField(12), 5, []FiredTrigger{scaleUp("cpu-hot", 0, "replicas", 3)}, "", 0, 0)
	assert.EqualValues(t, 12, got.Value)
	assert.Equal(t, SourceAlgorithm, got.Source)
	assert.False(t, out.Steps[0].Used)
}

func TestPercentScaleUpRoundsUpAndNeverUnderOne(t *testing.T) {
	got, _ := ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleUp("p", 0, "percent", 25)}, "", 0, 0)
	assert.EqualValues(t, 13, got.Value, "25% of 10 is 2.5, rounded up to 3")
	got, _ = ApplyTriggers(algorithmField(2), 2, []FiredTrigger{scaleUp("p", 0, "percent", 1)}, "", 0, 0)
	assert.EqualValues(t, 3, got.Value, "at least one replica")
}

func TestSetHighestPriorityWinsAndAScaleUpIsNeverCutBySet(t *testing.T) {
	got, out := ApplyTriggers(algorithmField(6), 5, []FiredTrigger{set("low", 1, 20), set("high", 9, 7)}, "", 0, 0)
	assert.EqualValues(t, 7, got.Value)
	assert.Equal(t, "high", got.Detail)
	assert.False(t, out.Steps[1].Used)
	assert.NotEmpty(t, out.Steps[1].Why)

	got, _ = ApplyTriggers(algorithmField(6), 5, []FiredTrigger{set("high", 9, 7), scaleUp("hot", 0, "replicas", 4)}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value, "5 + 4 is more than the 7 that was set")

	got, _ = ApplyTriggers(algorithmField(6), 5, []FiredTrigger{set("a", 3, 7), set("b", 3, 9)}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value, "equal priorities: the larger count, for safety")
}

func TestScaleDownActsOnlyWhenNothingElseObjects(t *testing.T) {
	// the forecast holds at what runs: the trigger takes two away
	got, out := ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleDown("idle", 0, 2)}, "", 0, 0)
	assert.EqualValues(t, 8, got.Value)
	assert.Equal(t, SourceTrigger, got.Source)
	assert.True(t, out.Steps[0].Used)

	// the one that removes the fewest counts
	got, _ = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleDown("a", 0, 5), scaleDown("b", 0, 1)}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value)

	// a protection blocks it
	got, out = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleDown("idle", 0, 2)}, "an OOM kill within 14 days", 0, 0)
	assert.EqualValues(t, 10, got.Value)
	assert.Contains(t, out.Steps[0].Why, "OOM")

	// the forecast wants more than runs
	got, out = ApplyTriggers(algorithmField(12), 10, []FiredTrigger{scaleDown("idle", 0, 2)}, "", 0, 0)
	assert.EqualValues(t, 12, got.Value)
	assert.Contains(t, out.Steps[0].Why, "more than")

	// a scale-up beside it wins
	got, _ = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleDown("idle", 0, 2), scaleUp("hot", 0, "replicas", 1)}, "", 0, 0)
	assert.EqualValues(t, 11, got.Value)
}

func TestTheResultKeepsToTheBounds(t *testing.T) {
	got, out := ApplyTriggers(algorithmField(4), 4, []FiredTrigger{scaleDown("idle", 0, 3)}, "", 3, 0)
	assert.EqualValues(t, 3, got.Value, "never below the minimum")
	assert.True(t, got.Clamped)
	assert.Contains(t, out.Rule, "minimum")
	got, _ = ApplyTriggers(algorithmField(4), 4, []FiredTrigger{scaleUp("hot", 0, "replicas", 50)}, "", 0, 15)
	assert.EqualValues(t, 15, got.Value, "never above the maximum")
}

func TestARelativeTriggerNeedsTheRunningCount(t *testing.T) {
	got, out := ApplyTriggers(algorithmField(6), 0, []FiredTrigger{scaleUp("hot", 0, "replicas", 2)}, "", 0, 0)
	assert.EqualValues(t, 6, got.Value)
	assert.Contains(t, out.Steps[0].Why, "not known")
	got, _ = ApplyTriggers(algorithmField(6), 0, []FiredTrigger{set("fixed", 0, 9)}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value, "a set needs no running count")
}

func TestATriggerGivesACountWhenNoOtherStrategyDid(t *testing.T) {
	f := Field{Source: SourceOriginal}
	got, _ := ApplyTriggers(f, 5, []FiredTrigger{scaleUp("hot", 0, "replicas", 2)}, "", 0, 0)
	assert.True(t, got.Set)
	assert.EqualValues(t, 7, got.Value)
}

// Scale-ups and scale-downs that fire together are weighed: the weight of a metric and how far past its threshold it is decide the direction.
func TestScaleUpsAndScaleDownsFiringTogetherAreWeighed(t *testing.T) {
	// the container is full (weight 3, 92% against 80%) and the JVM has used little of what it committed (weight 1, 30% against 40%): the container is believed
	hot := FiredTrigger{Name: "container-memory", Action: TriggerScaleUp, Unit: "replicas", Amount: 2, Weight: 3, Value: 0.92, Threshold: 0.8}
	idle := FiredTrigger{Name: "jvm-used", Action: TriggerScaleDown, Unit: "replicas", Amount: 1, Weight: 1, Value: 0.3, Threshold: 0.4}
	got, out := ApplyTriggers(algorithmField(10), 10, []FiredTrigger{hot, idle}, "", 0, 0)
	assert.EqualValues(t, 12, got.Value)
	assert.Equal(t, SourceTrigger, got.Source)
	assert.Greater(t, out.UpScore, out.DownScore)
	assert.Contains(t, out.Steps[1].Why, "outvoted")

	// the other way round: the JVM is trusted three times as much, so the scale-down wins and removes its replica
	hot.Weight, idle.Weight = 1, 3
	got, out = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{hot, idle}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value)
	assert.Less(t, out.UpScore, out.DownScore)
	assert.Contains(t, out.Steps[0].Why, "outvoted")
	assert.True(t, out.Steps[1].Used)

	// how far past the threshold counts too: the same weights, a metric far past its threshold outweighs one just past it
	hot = FiredTrigger{Name: "a", Action: TriggerScaleUp, Unit: "replicas", Amount: 1, Weight: 1, Value: 0.81, Threshold: 0.8}
	idle = FiredTrigger{Name: "b", Action: TriggerScaleDown, Unit: "replicas", Amount: 1, Weight: 1, Value: 0.1, Threshold: 0.4}
	got, _ = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{hot, idle}, "", 0, 0)
	assert.EqualValues(t, 9, got.Value, "the idle metric is far below its threshold, the hot one barely above")

	// equal counts: scaling out wins a tie
	got, _ = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{scaleDown("idle", 0, 2), scaleUp("hot", 0, "replicas", 1)}, "", 0, 0)
	assert.EqualValues(t, 11, got.Value)

	// a protection takes the scale-down out of the vote: the scale-up acts even though the scale-down would have weighed more
	idle.Weight = 10
	got, _ = ApplyTriggers(algorithmField(10), 10, []FiredTrigger{hot, idle}, "the JVM heap is over its threshold", 0, 0)
	assert.EqualValues(t, 11, got.Value)
}

// A proportional trigger works the count out of the metric: the running replicas times the value over the threshold, at most Amount away from what runs; the same metric that is
// still over the threshold at the next decision (the count has not caught up, or the operator is held by its cooldown) is worked out again from the replicas that run then.
func TestAProportionalTriggerSizesTheStepFromTheMetric(t *testing.T) {
	hot := FiredTrigger{Name: "cpu", Action: TriggerScaleUp, Unit: TriggerProportional, Amount: 4, Value: 120, Threshold: 100}
	got, out := ApplyTriggers(Field{}, 10, []FiredTrigger{hot}, "", 0, 0)
	assert.EqualValues(t, 12, got.Value, "10 x 120 / 100")
	assert.Equal(t, SourceTrigger, got.Source)
	assert.Equal(t, 12, out.Result)

	hot.Value = 300
	got, _ = ApplyTriggers(Field{}, 10, []FiredTrigger{hot}, "", 0, 0)
	assert.EqualValues(t, 14, got.Value, "10 x 300 / 100 is 30, but one decision moves at most 4 replicas")

	// still over the threshold next time: the next step is worked out from the 14 that run then
	got, _ = ApplyTriggers(Field{}, 14, []FiredTrigger{hot}, "", 0, 0)
	assert.EqualValues(t, 18, got.Value)
	got, _ = ApplyTriggers(Field{}, 18, []FiredTrigger{hot}, "", 0, 20)
	assert.EqualValues(t, 20, got.Value, "and never beyond the maximum of the module")

	cold := FiredTrigger{Name: "cpu-low", Action: TriggerScaleDown, Unit: TriggerProportional, Amount: 3, Value: 40, Threshold: 80}
	got, _ = ApplyTriggers(Field{}, 10, []FiredTrigger{cold}, "", 0, 0)
	assert.EqualValues(t, 7, got.Value, "10 x 40 / 80 is 5, but at most 3 replicas are taken away")
}
