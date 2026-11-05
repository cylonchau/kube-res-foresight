package decision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func windowInputs(now time.Time, periods ...FixedPeriod) Inputs {
	return Inputs{
		Now:     now,
		Periods: periods,
		Policy:  Policy{TTL: time.Hour, PodCapacityRPS: 100, HeadroomRatio: 1, Location: time.UTC},
		RPS:     []Candidate{{Algorithm: "naive", Peak: 700}}, // 7 replicas
	}
}

// 20:00-03:00 every day, crossing midnight.
func night(mode string, replicas int) FixedPeriod {
	return FixedPeriod{Name: "night-" + mode, Weekdays: 127, StartMinute: 20 * 60, EndMinute: 3 * 60, Replicas: replicas, ReplicaMode: mode}
}

func TestAFloorWindowRaisesTheAlgorithmButLetsItGoHigher(t *testing.T) {
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	rec := Decide(windowInputs(at, night(ReplicaWindowMin, 10)))
	assert.EqualValues(t, 7, rec.Replicas.Value, "Decide itself does not apply a floor; the algorithm answer stands")
	assert.Equal(t, SourceAlgorithm, rec.Replicas.Source)

	got := ApplyReplicaWindows(rec.Replicas, ActivePeriods(windowInputs(at, night(ReplicaWindowMin, 10))), 0, 0)
	assert.EqualValues(t, 10, got.Value, "7 from the algorithm is raised to the floor of 10")
	assert.Equal(t, SourceAlgorithm, got.Source, "the algorithm still decided; the window only kept it up")
	assert.Contains(t, got.Reason, "night-min")

	high := Field{Set: true, Value: 14, Source: SourceAlgorithm}
	assert.EqualValues(t, 14, ApplyReplicaWindows(high, ActivePeriods(windowInputs(at, night(ReplicaWindowMin, 10))), 0, 0).Value, "a forecast above the floor is followed")
}

func TestACapWindowKeepsTheCountDownAndOutsideTheWindowNothingHappens(t *testing.T) {
	in := windowInputs(time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC), night(ReplicaWindowMax, 5)) // 01:00 of the next day
	f := Field{Set: true, Value: 7, Source: SourceAlgorithm}
	assert.EqualValues(t, 5, ApplyReplicaWindows(f, ActivePeriods(in), 0, 0).Value)

	out := windowInputs(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), night(ReplicaWindowMax, 5))
	assert.Equal(t, f, ApplyReplicaWindows(f, ActivePeriods(out), 0, 0), "noon is outside 20:00-03:00")
}

func TestAFixedWindowStillPinsTheCountAndABypassIsNotMoved(t *testing.T) {
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	rec := Decide(windowInputs(at, night(ReplicaWindowFixed, 12)))
	assert.EqualValues(t, 12, rec.Replicas.Value)
	assert.Equal(t, SourceFixedPeriod, rec.Replicas.Source)
	rec = Decide(windowInputs(at, night("", 12)))
	assert.Equal(t, SourceFixedPeriod, rec.Replicas.Source, "no mode means fixed, as before")

	cap5 := night(ReplicaWindowMax, 5)
	assert.EqualValues(t, 12, ApplyReplicaWindows(rec.Replicas, []FixedPeriod{cap5}, 0, 0).Value, "a pinned count is not capped")
	bypass := Field{Source: SourceBypass}
	assert.Equal(t, bypass, ApplyReplicaWindows(bypass, []FixedPeriod{night(ReplicaWindowMin, 9)}, 0, 0))
}

func TestFloorsAndCapsAcrossWindowsAndTheBoundsOfTheModule(t *testing.T) {
	f := Field{Set: true, Value: 7, Source: SourceAlgorithm}
	a, b := night(ReplicaWindowMin, 10), night(ReplicaWindowMin, 12)
	b.Name = "other"
	assert.EqualValues(t, 12, ApplyReplicaWindows(f, []FixedPeriod{a, b}, 0, 0).Value, "the largest floor counts")

	capLow := night(ReplicaWindowMax, 4)
	got := ApplyReplicaWindows(f, []FixedPeriod{a, capLow}, 0, 0)
	assert.EqualValues(t, 10, got.Value, "a floor above a cap: the floor wins")
	assert.Contains(t, got.Reason, "the floor wins")

	assert.EqualValues(t, 9, ApplyReplicaWindows(f, []FixedPeriod{a}, 0, 9).Value, "the maximum of the module is the last word")
	assert.EqualValues(t, 8, ApplyReplicaWindows(f, []FixedPeriod{night(ReplicaWindowMax, 5)}, 8, 0).Value, "so is the minimum of the module")
}

func TestAFloorGivesTheCountWhenNothingDecidedOne(t *testing.T) {
	none := Field{Source: SourceOriginal}
	got := ApplyReplicaWindows(none, []FixedPeriod{night(ReplicaWindowMin, 6)}, 0, 0)
	assert.True(t, got.Set)
	assert.EqualValues(t, 6, got.Value)
	assert.Equal(t, SourceFixedPeriod, got.Source)
	assert.False(t, ApplyReplicaWindows(none, []FixedPeriod{night(ReplicaWindowMax, 6)}, 0, 0).Set, "a cap gives nothing")
}

func TestAFixedCountReplacesTheAlgorithmAndAPeriodStillWins(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	in := windowInputs(at)
	in.Policy.FixedReplicas = 4
	in.PressureReplicas = 9
	rec := Decide(in)
	assert.EqualValues(t, 4, rec.Replicas.Value, "the algorithm said 7 and the metric pressure 9; the module keeps 4")
	assert.Equal(t, SourceFixedCount, rec.Replicas.Source)

	in = windowInputs(time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC), night(ReplicaWindowFixed, 12))
	in.Policy.FixedReplicas = 4
	assert.EqualValues(t, 12, Decide(in).Replicas.Value, "a window that pins the count is the more specific word")

	in = windowInputs(time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC), night(ReplicaWindowMin, 10))
	in.Policy.FixedReplicas = 4
	rec = Decide(in)
	assert.EqualValues(t, 10, ApplyReplicaWindows(rec.Replicas, ActivePeriods(in), 0, 0).Value, "4 normally, at least 10 at night")

	in = windowInputs(at)
	in.Policy.FixedReplicas = 4
	in.Policy.Bounds = Bounds{MinReplicas: 6}
	assert.EqualValues(t, 6, Decide(in).Replicas.Value, "the minimum of the module raises it")
}

func TestAPeriodNeedsAValidReplicaMode(t *testing.T) {
	p := night("sometimes", 5)
	assert.Error(t, ValidatePeriod(p))
	p = night(ReplicaWindowMin, 0)
	p.CPUMilli = 500
	assert.Error(t, ValidatePeriod(p), "a floor with no number")
	assert.NoError(t, ValidatePeriod(night(ReplicaWindowMax, 5)))
	assert.NoError(t, ValidatePeriod(night("", 5)))
}
