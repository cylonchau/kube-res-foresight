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
