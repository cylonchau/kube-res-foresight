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
