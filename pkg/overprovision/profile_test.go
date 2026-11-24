package overprovision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// a use that is busy for two hours a day: the peak count is for those hours, the normal count for the rest, and a count that follows the hours saves most of the replica-hours
func TestTheLoadProfileSeparatesTheBusyHoursFromTheNormalOnes(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var total []Point
	for ts := start; ts.Before(start.Add(7 * 24 * time.Hour)); ts = ts.Add(5 * time.Minute) {
		v := 5.0
		if h := ts.Hour(); h == 9 || h == 10 {
			v = 20
		}
		total = append(total, Point{TS: ts.Unix(), V: v})
	}
	prof := BuildLoadProfile([]LoadInput{{Resource: CPU, Total: total, Request: 4, StepSeconds: 300}}, 0.85, 95, time.UTC)
	require.NotNil(t, prof)
	assert.Equal(t, 7, prof.Days)
	assert.Equal(t, 2, prof.Normal)
	assert.Equal(t, 6, prof.Peak)
	assert.Equal(t, []int{9, 10}, prof.PeakHours)
	assert.InDelta(t, 1-56.0/144, prof.Saved, 1e-9)
	require.NotNil(t, prof.Above)
	assert.InDelta(t, 2.0/24, prof.Above.Share, 0.01, "the use is above what the normal count carries for 2 hours of 24")
	assert.Equal(t, 7, prof.Above.Events)
	assert.Equal(t, 120, prof.Above.LongestMinutes, "each stretch is two hours")
}

// a flat use asks for the same count in every hour: no busy hours and nothing saved; a period of less than two days is not a profile
func TestAFlatUseHasNoBusyHoursAndAShortPeriodNoProfile(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var flat []Point
	for ts := start; ts.Before(start.Add(4 * 24 * time.Hour)); ts = ts.Add(10 * time.Minute) {
		flat = append(flat, Point{TS: ts.Unix(), V: 60})
	}
	prof := BuildLoadProfile([]LoadInput{{Resource: Memory, Total: flat, Request: 6, StepSeconds: 600}}, 0.85, 95, time.UTC)
	require.NotNil(t, prof)
	assert.Equal(t, prof.Normal, prof.Peak)
	assert.Empty(t, prof.PeakHours)
	assert.Zero(t, prof.Saved)
	assert.Nil(t, BuildLoadProfile([]LoadInput{{Resource: Memory, Total: flat[:100], Request: 6, StepSeconds: 600}}, 0.85, 95, time.UTC))
}
