package decision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var t0 = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

// A request is over when it asked for more than the peak the load needed while it stood; a request with no measured load is not judged.
func TestWasteComparesEachRequestWithThePeakNeedWhileItStood(t *testing.T) {
	needs := []NeedPoint{{at(0), 2}, {at(30), 4}, {at(60), 3}, {at(90), 3}}
	held := []HeldReplicas{
		{at(0), at(60), 6},    // peak need 4 -> over by 2 for an hour
		{at(60), at(120), 3},  // peak need 3 -> exact
		{at(120), at(180), 5}, // no measured load -> not judged
		{at(0), at(40), 3},    // peak need 4 -> under by 1
	}
	w := WasteOf(held, needs)
	assert.Equal(t, 3, w.Judged)
	assert.Equal(t, 1, w.Over)
	assert.Equal(t, 1, w.Under)
	assert.Equal(t, 2.0, w.OverReplicas)
	assert.Equal(t, 1.0, w.UnderReplicas)
	assert.InDelta(t, 2.0, w.SurplusReplicaHours, 1e-9)
}

func TestWasteWithoutMeasuredLoadJudgesNothing(t *testing.T) {
	w := WasteOf([]HeldReplicas{{at(0), at(60), 3}}, nil)
	assert.Zero(t, w.Judged)
}

// A scale-down within the window of a scale-up is a reversal; one long after is not.
func TestChurnCountsReversalsWithinTheWindowOnly(t *testing.T) {
	events := []ScaleEvent{{at(0), true}, {at(10), false}, {at(20), true}, {at(200), false}, {at(210), false}}
	c := ChurnOf(events, 30*time.Minute)
	assert.Equal(t, Churn{Ups: 2, Downs: 3, Reversals: 2}, c)

	unsorted := []ScaleEvent{events[3], events[0], events[2], events[1], events[4]}
	assert.Equal(t, c, ChurnOf(unsorted, 30*time.Minute), "the order the events arrive in does not matter")
}

// Pod-hours and the capacity SLO are measured on what ran: the Ready replicas against the replicas the load needed, moment by moment.
func TestCapacityCountsPodHoursSurplusShortfallAndTheTimeBelowTheNeed(t *testing.T) {
	// a five minute step for two hours; the load needs 4 Pods, then 6 for the second hour
	var needs []NeedPoint
	var ready []ReplicaSample
	for i := 0; i < 24; i++ {
		need, have := 4, 5 // one surplus Pod in the first hour
		if i >= 12 {
			need, have = 6, 6
		}
		if i >= 18 {
			have = 4 // the last half hour runs two Pods short
		}
		needs = append(needs, NeedPoint{At: at(i * 5), Need: need})
		ready = append(ready, ReplicaSample{At: at(i * 5), Replicas: have})
	}
	c := CapacityOf(ready, needs)
	assert.Equal(t, 24, c.Points)
	assert.InDelta(t, 2.0, c.CoveredHours, 1e-9)
	assert.InDelta(t, 5.0*1+6.0*0.5+4.0*0.5, c.ActualPodHours, 1e-9)
	assert.InDelta(t, 4.0*1+6.0*1, c.NeededPodHours, 1e-9)
	assert.InDelta(t, 1.0, c.SurplusPodHours, 1e-9, "one Pod too many for the first hour")
	assert.InDelta(t, 2.0*0.5, c.ShortfallPodHours, 1e-9, "two Pods too few for half an hour")
	assert.InDelta(t, 30.0, c.ShortMinutes, 1e-9)
	assert.InDelta(t, 1-30.0/120, c.Attainment, 1e-9)
}

// A hole in the data is not measured time, and a need point without a recent replica sample is left out, not counted as short.
func TestCapacityDoesNotCountTheHolesOfTheData(t *testing.T) {
	needs := []NeedPoint{{at(0), 3}, {at(5), 3}, {at(10), 3}, {at(300), 3}, {at(305), 3}}
	ready := []ReplicaSample{{at(0), 3}, {at(5), 3}, {at(10), 3}, {at(300), 3}, {at(305), 3}}
	c := CapacityOf(ready, needs)
	assert.Equal(t, 5, c.Points)
	assert.InDelta(t, (5.0+5+10+5+5)/60, c.CoveredHours, 1e-9, "the five hours between the two stretches stand for at most two steps, not five hours")

	stale := CapacityOf([]ReplicaSample{{at(0), 3}}, []NeedPoint{{at(0), 3}, {at(5), 3}, {at(60), 3}})
	assert.Equal(t, 2, stale.Points, "the point an hour after the only replica sample has no replicas to compare with")

	assert.Zero(t, CapacityOf(nil, needs).Points)
	assert.Zero(t, CapacityOf(ready, nil).Points)
}
