package overprovision

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flat(n int, v float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{TS: int64(i) * 300, V: v}
	}
	return out
}

// ramp is 1..n scaled, so the quantiles are known: the P95 of 1..100 is 95, the P99 is 99 and the highest is 100.
func ramp(n int, scale float64) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{TS: int64(i) * 300, V: float64(i+1) * scale}
	}
	return out
}

func TestTheQuantilesAreTheNearestRank(t *testing.T) {
	u := UsageOf(ramp(100, 1))
	assert.Equal(t, 100, u.Points)
	assert.Equal(t, 50.0, u.P50)
	assert.Equal(t, 90.0, u.P90)
	assert.Equal(t, 95.0, u.P95)
	assert.Equal(t, 99.0, u.P99)
	assert.Equal(t, 100.0, u.Max)
	// not finite and negative values are dropped
	u = UsageOf([]Point{{0, math.NaN()}, {1, -3}, {2, math.Inf(1)}, {3, 4}})
	assert.Equal(t, 1, u.Points)
	assert.Equal(t, 4.0, u.Max)
	assert.Equal(t, Usage{}, UsageOf(nil))
}

// 100 points of 5 minutes cover 500 minutes: a window of 500 minutes is fully covered.
func input(resource string, request float64, points []Point) Input {
	return Input{Resource: resource, Request: request, Points: points, StepSeconds: 300, WindowMinutes: len(points) * 5, Throttle: -1}
}

// use makes a series whose P90, P95 and P99 are the three figures (and the highest is the last): 100 points, so the nearest ranks are the 90th, 95th and 99th.
func use(p90, p95, p99, max float64) []Point {
	out := make([]Point, 100)
	for i := range out {
		v := p90
		switch {
		case i >= 99:
			v = max
		case i >= 95:
			v = p99
		case i >= 90:
			v = p95
		}
		out[i] = Point{TS: int64(i) * 300, V: v}
	}
	// the 90th value has to be the P90 itself: the first 90 are below or at it
	for i := 0; i < 90; i++ {
		out[i].V = p90 * 0.9
	}
	out[89].V = p90
	return out
}

func TestTheRecommendedRequestPutsEveryPercentileUnderItsTarget(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// P90 80, P95 85, P99 110 of the request are the targets: a flat use of 10 asks for 12.5 (10 / 0.8)
	r := Analyze(input(CPU, 100, flat(100, 10)), p)
	assert.InDelta(t, 12.5, r.Needed, 1e-9)
	assert.InDelta(t, 12.5, r.Suggested, 1e-9)
	assert.Contains(t, r.Formula, "P90")
	assert.Contains(t, r.Formula, "P95")
	assert.Contains(t, r.Formula, "P99")
	assert.Equal(t, FormulaVersion, r.FormulaVersion)
	// the P95 asks for the most here: 95 / 0.85 = 111.8 against the P90 90 / 0.8 = 112.5 and the P99 99 / 1.1 = 90
	r = Analyze(input(CPU, 200, ramp(100, 1)), p)
	assert.InDelta(t, 112.5, r.Needed, 1e-9)
	// the highest use takes no part: a spike of one point in 200 changes nothing
	pts := flat(200, 10)
	pts[100].V = 1000
	assert.InDelta(t, 12.5, Analyze(input(Memory, 300, pts), p).Needed, 1e-9)
}

func TestTheSuggestionIsRoundedToASpec(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(CPU, 2, flat(100, 0.41))
	in.Round = func(v float64) float64 { return math.Ceil(v*100) / 100 }
	r := Analyze(in, p)
	assert.InDelta(t, 0.52, r.Suggested, 1e-9) // 0.41 / 0.8 = 0.5125, up to the next hundredth
}

// The verdict is where the percentiles stand against the request: P95 under 80% of it is over-provisioned, 80% to 90% is reasonable, over 90% is under-provisioned.
func TestTheVerdictIsWhereTheP95StandsAgainstTheRequest(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// a flat use of 10: all three percentiles are 10
	for _, c := range []struct {
		request float64
		status  string
		grade   string
	}{
		{100, StatusOver, GradeSevere},       // 10% of the request; the recommendation is 12.5: 87.5% to give back
		{25, StatusOver, GradeSevere},        // 40%; 12.5 of 25 is 50%
		{20, StatusOver, GradeModerate},      // 50%; 7.5 of 20 is 37.5%
		{14, StatusOver, GradeMild},          // 71.4% (under the 80%); 1.5 of 14 is 10.7%
		{12.5, StatusOK, GradeReasonable},    // 80% exactly: in the range, and the recommendation is the request
		{11.8, StatusOK, GradeReasonable},    // 84.7%: under the 85% of the P90
		{10.5, StatusUnder, GradeReasonable}, // 95%: above 90%
		{9, StatusUnder, GradeReasonable},    // the use is above the request
	} {
		r := Analyze(input(CPU, c.request, flat(100, 10)), p)
		assert.Equal(t, c.status, r.Status, "request %v", c.request)
		assert.Equal(t, c.grade, r.Grade, "request %v", c.request)
	}
}

// What is to be given back, and how much of the request it is, is said for a request that is reasonable too: not just "reasonable".
func TestAReasonableRequestSaysHowMuchItCouldStillGiveBack(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// P90 2.92, P95 3.22, P99 4.02 against a request of 4: 73%, 80.5% and 100.5%, all in their ranges
	r := Analyze(input(CPU, 4, use(2.92, 3.22, 4.02, 4.64)), p)
	assert.Equal(t, StatusOK, r.Status)
	assert.Equal(t, GradeReasonable, r.Grade)
	// the P95 asks for 3.22 / 0.85 = 3.79, the P90 for 2.92 / 0.8 = 3.65 and the P99 for 4.02 / 1.1 = 3.65
	assert.InDelta(t, 3.788, r.Suggested, 1e-3)
	assert.InDelta(t, 0.212, r.Reclaimable, 1e-3)
	assert.InDelta(t, 0.053, r.Ratio, 1e-3)
	require.NotNil(t, r.Shares)
	assert.InDelta(t, 0.805, r.Shares.P95, 1e-9)
	// the limit is the P99 with the headroom, never below the recommended request: 4.02 x 1.2
	assert.InDelta(t, 4.824, r.SuggestedLimit, 1e-9)
	// no request is lowered below what the use asks for, and a request above the range is raised (a request of 6 GiB against a P95 of 8.18 GiB)
	m := Analyze(input(Memory, 6, use(7.95, 8.18, 8.69, 9.78)), p)
	assert.Equal(t, StatusUnder, m.Status)
	assert.InDelta(t, 9.9375, m.Suggested, 1e-3)
	assert.Less(t, m.Ratio, 0.0, "a shortage is a negative ratio")
	assert.Equal(t, 0.0, m.Reclaimable)
}

// A steady use that is low with a tail above its range is a burst: the request is sized for the tail and is not called over-provisioned.
func TestABurstOnALowSteadyUseIsNotCalledOverProvisioned(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// P90 and P95 at 30% and 40% of 10, the P99 at 150%
	r := Analyze(input(CPU, 10, use(3, 4, 15, 16)), p)
	assert.Equal(t, StatusOK, r.Status)
	assert.Contains(t, r.Reasons, ReasonBurst)
	assert.Equal(t, 10.0, r.Suggested, "kept as it is")
	assert.Equal(t, 0.0, r.Reclaimable)
	// the same steady use with the tail inside its range is over-provisioned
	r = Analyze(input(CPU, 10, use(3, 4, 8, 9)), p)
	assert.Equal(t, StatusOver, r.Status)
	assert.Less(t, r.Suggested, 10.0)
}

// The coefficients are the system parameters: a different band gives a different verdict.
func TestTheBandsAreTheParametersOfTheVerdict(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(CPU, 14, flat(100, 10)) // 71.4% of the request
	assert.Equal(t, StatusOver, Analyze(in, p).Status)
	p.P95.Low, p.P90.Low, p.P99.Low = 0.60, 0.60, 0.60
	assert.Equal(t, StatusOK, Analyze(in, p).Status)
	p.P95.High = 0.70 // now 71.4% is above the range of the P95
	assert.Equal(t, StatusUnder, Analyze(in, p).Status)
}

func TestThrottledCPUIsNeverCalledOverProvisioned(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(CPU, 100, flat(100, 10))
	in.Throttle = 0.08 // 8% of the time, over the 5% limit
	r := Analyze(in, p)
	assert.Equal(t, StatusLimited, r.Status)
	assert.Equal(t, 100.0, r.Suggested, "not below the request")
	assert.Equal(t, 0.0, r.Reclaimable)
	assert.Contains(t, r.Reasons, ReasonThrottled)
	assert.Equal(t, Medium, r.Risk)
	// under the limit it is judged as usual
	in.Throttle = 0.04
	assert.Equal(t, StatusOver, Analyze(in, p).Status)
	// not measured is not throttled
	in.Throttle = -1
	assert.Equal(t, StatusOver, Analyze(in, p).Status)
}

func TestAnOOMKillKeepsTheMemoryFromBeingLowered(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(Memory, 1000, flat(100, 100))
	in.OOMKills, in.OOMWorkingSet = 1, 700
	r := Analyze(in, p)
	assert.Equal(t, StatusLimited, r.Status)
	assert.Equal(t, High, r.Risk)
	assert.Contains(t, r.Reasons, ReasonOOM)
	assert.InDelta(t, 1000, r.Suggested, 1e-9, "not below the request, and not below the working set x 1.2 = 840")
	assert.Equal(t, 0.0, r.Reclaimable)
	// a working set above the request lifts the suggestion over it
	in.OOMWorkingSet = 900
	r = Analyze(in, p)
	assert.InDelta(t, 1080, r.Suggested, 1e-9)
	assert.Less(t, r.Ratio, 0.0)
}

func TestTooLittleDataGivesNoSuggestion(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// 30 of 100 expected points: a coverage of 30%
	in := input(CPU, 100, flat(30, 10))
	in.WindowMinutes = 500
	r := Analyze(in, p)
	assert.InDelta(t, 0.3, r.Coverage, 1e-9)
	assert.Equal(t, StatusUnknown, r.Status)
	assert.Equal(t, 0.0, r.Suggested)
	assert.Equal(t, Low, r.Confidence)
	assert.Contains(t, r.Reasons, ReasonCoverageLow)
	assert.Equal(t, 30, r.Usage.Points, "the use is still reported")

	// 60 of 100: medium, and a suggestion
	in = input(CPU, 100, flat(60, 10))
	in.WindowMinutes = 500
	r = Analyze(in, p)
	assert.Equal(t, Medium, r.Confidence)
	assert.Equal(t, StatusOver, r.Status)

	// nothing measured, no request
	assert.Contains(t, Analyze(input(CPU, 100, nil), p).Reasons, ReasonNoUsage)
	assert.Contains(t, Analyze(input(CPU, 0, flat(100, 1)), p).Reasons, ReasonNoRequest)
}

func TestAShortWindowAndMissingJVMMetricsLowerTheConfidence(t *testing.T) {
	p := DefaultParams() // baseline 7 days
	in := input(CPU, 100, flat(100, 10))
	r := Analyze(in, p) // a window of 500 minutes is shorter than 7 days
	assert.Equal(t, Medium, r.Confidence)
	assert.Contains(t, r.Reasons, ReasonShortWindow)
	in.JVMExpected = true
	r = Analyze(in, p)
	assert.Equal(t, Low, r.Confidence)
	assert.Contains(t, r.Reasons, ReasonJVMMissing)
	assert.NotEqual(t, StatusUnknown, r.Status, "a low confidence from these reasons still gives a suggestion")
	// a full window of the baseline keeps it high; the JVM metrics present keep it
	in = input(CPU, 100, flat(100, 10))
	in.WindowMinutes = 500
	p.BaselineMinutes = 500
	in.JVMExpected, in.JVMPresent = true, true
	assert.Equal(t, High, Analyze(in, p).Confidence)
}

func TestThePeakNearTheLimitRaisesTheRisk(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(Memory, 300, ramp(100, 1))
	in.Limit = 105 // the highest use is 100: 95% of the limit
	r := Analyze(in, p)
	assert.Equal(t, Medium, r.Risk)
	assert.Contains(t, r.Reasons, ReasonPeakNearLimit)
	in.Limit = 1000
	r = Analyze(in, p)
	assert.Equal(t, Low, r.Risk)
	in.Limit = 0
	assert.Contains(t, Analyze(in, p).Reasons, ReasonNoLimit)
}

func TestTheTotalIsAnotherSeries(t *testing.T) {
	p := DefaultParams()
	in := input(CPU, 100, flat(100, 10))
	in.Total = flat(100, 30)
	r := Analyze(in, p)
	require.NotNil(t, r.Total)
	assert.Equal(t, 30.0, r.Total.P95)
	assert.Equal(t, 10.0, r.Usage.P95)
}

func TestMovingAveragesAndResampling(t *testing.T) {
	pts := []Point{{0, 1}, {60, 2}, {120, 3}, {180, 4}, {240, 5}}
	ma := MovingAverage(pts, 3)
	assert.InDelta(t, 1, ma[0].V, 1e-9) // fewer points at the start
	assert.InDelta(t, 1.5, ma[1].V, 1e-9)
	assert.InDelta(t, 2, ma[2].V, 1e-9)
	assert.InDelta(t, 3, ma[3].V, 1e-9)
	assert.InDelta(t, 4, ma[4].V, 1e-9)
	assert.Equal(t, pts, MovingAverage(pts, 1))

	rs := Resample(pts, 120) // buckets 0-119, 120-239, 240-
	require.Len(t, rs, 3)
	assert.InDelta(t, 1.5, rs[0].V, 1e-9)
	assert.InDelta(t, 3.5, rs[1].V, 1e-9)
	assert.InDelta(t, 5, rs[2].V, 1e-9)

	assert.Equal(t, 60, MAStepFor(15, 0))
	assert.Equal(t, 60, MAStepFor(60, 0))
	assert.Equal(t, 300, MAStepFor(24*60, 0))
	assert.Equal(t, 300, MAStepFor(7*24*60, 0))
	assert.Equal(t, 900, MAStepFor(15*24*60, 0))
	assert.Equal(t, 300, MAStepFor(15, 300), "a series stored every 5 minutes cannot be read every minute")
	assert.Equal(t, "MA5@1m", MALabel(5, 60))
	assert.Equal(t, "MA15@5m", MALabel(15, 300))
	assert.Equal(t, "MA25@1h", MALabel(25, 3600))
}

func TestTheWindowsAreTheSevenOfTheProposal(t *testing.T) {
	require.Len(t, Windows, 7)
	w, ok := WindowByMinutes(7 * 24 * 60)
	assert.True(t, ok)
	assert.Equal(t, "7d", w.Label)
	_, ok = WindowByMinutes(5)
	assert.False(t, ok)
}

// With no request to compare with the use still gives the request and the limit it supports: the status is unknown, there is no ratio or grade.
func TestTheSuggestionDoesNotNeedARequestAndComesWithALimit(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	cpu := Analyze(input(CPU, 0, ramp(100, 1)), p)
	assert.Contains(t, cpu.Reasons, ReasonNoRequest)
	assert.Equal(t, StatusUnknown, cpu.Status)
	// the P90 asks for 90 / 0.8 = 112.5, the most; the limit is the P99 99 x 1.2
	assert.InDelta(t, 112.5, cpu.Suggested, 1e-9)
	assert.InDelta(t, 118.8, cpu.SuggestedLimit, 1e-9)
	assert.Equal(t, 0.0, cpu.Ratio)
	assert.Equal(t, "", cpu.Grade)

	mem := Analyze(input(Memory, 0, ramp(100, 1)), p)
	assert.GreaterOrEqual(t, mem.SuggestedLimit, mem.Suggested, "a limit is never below the recommended request")
	assert.GreaterOrEqual(t, mem.SuggestedLimit, mem.Usage.P99, "a limit is never below the P99")

	// with a request the limit is the one the recommended request gives, and the status is judged
	with := Analyze(input(CPU, 200, ramp(100, 1)), p)
	assert.InDelta(t, cpu.SuggestedLimit, with.SuggestedLimit, 1e-9)
	assert.NotEqual(t, StatusUnknown, with.Status)
}

// The Requests margin and the Limits margin of a resource are the module's own for that resource: the CPU margin does not touch the memory, and the Limits margin falls
// back on the system one where the module sets none.
func TestTheMarginsOfTheRequestsAndOfTheLimitsAreEachResourcesOwn(t *testing.T) {
	p := DefaultParams()
	base := Analyze(input(CPU, 200, ramp(100, 1)), p)
	p.CPURequestHeadroom = 0.1
	cpu := Analyze(input(CPU, 200, ramp(100, 1)), p)
	assert.InDelta(t, base.Needed*1.1, cpu.Needed, 1e-9, "the CPU need carries the CPU margin")
	assert.Contains(t, cpu.Formula, "(1+10%)")
	mem := Analyze(input(Memory, 200, ramp(100, 1)), p)
	assert.InDelta(t, Analyze(input(Memory, 200, ramp(100, 1)), DefaultParams()).Needed, mem.Needed, 1e-9, "the memory has none")

	p = DefaultParams()
	p.MemoryLimitHeadroom = 0.5
	assert.Equal(t, 0.2, p.LimitHeadroomOf(CPU), "no margin of its own: the system one")
	assert.Equal(t, 0.5, p.LimitHeadroomOf(Memory))
	limit := Analyze(input(Memory, 200, ramp(100, 1)), p)
	assert.InDelta(t, 99*1.5, limit.SuggestedLimit, 1e-6)
}
