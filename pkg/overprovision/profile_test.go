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

// the last day in whole hours, each cut into 10-minute cells with the mean of the use; a cell with no points is -1, and the capacity is what the normal count carries
func TestTheLastDayIsCutIntoHoursAndCells(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var total []Point
	end := start.Add(7*24*time.Hour + 30*time.Minute) // the last hour is half over
	for ts := start; ts.Before(end); ts = ts.Add(5 * time.Minute) {
		v := 5.0
		if h := ts.Hour(); h == 9 || h == 10 {
			v = 20
		}
		total = append(total, Point{TS: ts.Unix(), V: v})
	}
	in := []LoadInput{{Resource: CPU, Total: total, Raw: total, Request: 4, StepSeconds: 300}}
	prof := BuildLoadProfile(in, 0.85, 95, time.UTC)
	require.NotNil(t, prof)
	prof.AddRecent(in, 0.85, end, time.UTC)
	require.Len(t, prof.Recent, 1)
	assert.Equal(t, 300, prof.CellSeconds, "a series of 5 minutes is cut into cells of 5 minutes")
	assert.Len(t, prof.RecentHours, 24)
	assert.Equal(t, 1, prof.RecentHours[0], "the 24 hours end with the hour the end is in")
	assert.Equal(t, 0, prof.RecentHours[23])
	load := prof.Recent[0]
	assert.InDelta(t, 2*4*0.85, load.Capacity, 1e-9, "what 2 replicas of 4 cores carry at 85%")
	assert.Len(t, load.Values, 24*12)
	assert.InDelta(t, 5.0, load.Values[0], 1e-9)
	assert.InDelta(t, 20.0, load.Values[(9-1)*12], 1e-9, "the 9 o'clock block is the 9th of the 24 from 1 o'clock")
	assert.InDelta(t, -1.0, load.Values[len(load.Values)-1], 1e-9, "the half of the last hour that has not happened has no use")
	assert.Equal(t, 600, cellSecondsFor(600))
	assert.Equal(t, 900, cellSecondsFor(900))
}

// the replicas of the profile are the ones measured: a median of the samples of each hour, never the estimate; an hour with no sample has none
func TestTheMeasuredReplicasAreKeptApartFromTheEstimate(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	var total, reps []Point
	for ts := start; ts.Before(end); ts = ts.Add(5 * time.Minute) {
		v := 5.0
		if h := ts.Hour(); h == 9 || h == 10 {
			v = 20
		}
		total = append(total, Point{TS: ts.Unix(), V: v})
		// the workload really ran 3 replicas, and 4 in the hours 9 and 10; nothing was measured from 02:00 to 04:00
		if h := ts.Hour(); h == 2 || h == 3 {
			continue
		}
		n := 3.0
		if h := ts.Hour(); h == 9 || h == 10 {
			n = 4
		}
		reps = append(reps, Point{TS: ts.Unix(), V: n})
	}
	prof := BuildLoadProfile([]LoadInput{{Resource: CPU, Total: total, Raw: total, Request: 4, StepSeconds: 300}}, 0.85, 95, time.UTC)
	require.NotNil(t, prof)
	prof.AddRecent([]LoadInput{{Resource: CPU, Total: total, Raw: total, Request: 4, StepSeconds: 300}}, 0.85, end, time.UTC)
	prof.AddActual(reps, end, time.UTC, "ready-replicas")

	assert.Equal(t, 6, prof.Peak, "the estimate is untouched: what the use asks for")
	assert.Equal(t, 4, prof.ActualPeak, "the measured peak is what the workload had")
	assert.Equal(t, 3, prof.ActualNormal)
	assert.Equal(t, 22, prof.ActualHours, "two hours had no sample")
	assert.Equal(t, "ready-replicas", prof.ActualSource)
	assert.Equal(t, 4, prof.Hours[9].Actual)
	assert.Equal(t, 6, prof.Hours[9].Need)
	assert.Zero(t, prof.Hours[2].Actual, "no sample, no figure: the estimate does not fill it")
	assert.Equal(t, 3, prof.Hours[20].Actual)
	assert.Equal(t, 3, prof.Hours[20].ActualMin)
	assert.Equal(t, 3, prof.Hours[20].ActualMax)
	require.Len(t, prof.RecentReplicas, 24)

	// no replicas metric: nothing is measured and nothing is made up
	bare := BuildLoadProfile([]LoadInput{{Resource: CPU, Total: total, Request: 4, StepSeconds: 300}}, 0.85, 95, time.UTC)
	bare.AddActual(nil, end, time.UTC, "")
	assert.Zero(t, bare.ActualPeak)
	assert.Zero(t, bare.ActualHours)
}
