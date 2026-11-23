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

func TestCPUSuggestionIsTheLargerOfP95WithMarginAndP99(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// 1..100 cores of use: P95 = 95 x 1.2 = 114, P99 = 99, so the suggestion is 114 and the request of 200 is 43% above it
	r := Analyze(input(CPU, 200, ramp(100, 1)), p)
	assert.InDelta(t, 114, r.Suggested, 1e-9)
	assert.InDelta(t, 86, r.Numerator, 1e-9)
	assert.Equal(t, 200.0, r.Denominator)
	assert.InDelta(t, 0.43, r.Ratio, 1e-9)
	assert.Equal(t, StatusOver, r.Status)
	assert.Equal(t, GradeModerate, r.Grade)
	assert.InDelta(t, 86, r.Reclaimable, 1e-9)
	assert.Equal(t, High, r.Confidence)
	assert.Contains(t, r.Formula, "P95")
	assert.Equal(t, FormulaVersion, r.FormulaVersion)

	// a flat use: P95 = P99 = 10, with the margin 12 is above 10
	r = Analyze(input(CPU, 100, flat(100, 10)), p)
	assert.InDelta(t, 12, r.Suggested, 1e-9)
	assert.Equal(t, GradeSevere, r.Grade) // (100-12)/100 = 88%
}

func TestMemorySuggestionIsTheLargerOfP99WithMarginAndTheHighestWithAFactor(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// P99 = 99 x 1.15 = 113.85, max = 100 x 1.05 = 105: the larger is the first
	r := Analyze(input(Memory, 300, ramp(100, 1)), p)
	assert.InDelta(t, 113.85, r.Suggested, 1e-9)
	// a spike that is one point in 200 is not in the P99 but is in the highest
	pts := flat(200, 10)
	pts[100].V = 100
	r = Analyze(input(Memory, 300, pts), p)
	assert.InDelta(t, 105, r.Suggested, 1e-9)
}

func TestTheSuggestionIsRoundedToASpec(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	in := input(CPU, 2, flat(100, 0.41))
	in.Round = func(v float64) float64 { return math.Ceil(v*100) / 100 }
	r := Analyze(in, p)
	assert.InDelta(t, 0.50, r.Suggested, 1e-9) // 0.41 x 1.2 = 0.492, up to the next hundredth
}

func TestTheGradesFollowTheShareThatIsMore(t *testing.T) {
	p := DefaultParams()
	p.BaselineMinutes = 0
	// a flat use of 10 asks for 12
	for _, c := range []struct {
		request float64
		status  string
		grade   string
	}{
		{100, StatusOver, GradeSevere},    // 88%
		{24, StatusOver, GradeSevere},     // 50% exactly
		{20, StatusOver, GradeModerate},   // 40%
		{16, StatusOver, GradeMild},       // 25%
		{14.5, StatusOver, GradeMild},     // 17%
		{13, StatusOK, GradeReasonable},   // 7.7%
		{9, StatusUnder, GradeReasonable}, // below the P95 use of 10
	} {
		r := Analyze(input(CPU, c.request, flat(100, 10)), p)
		assert.Equal(t, c.status, r.Status, "request %v", c.request)
		assert.Equal(t, c.grade, r.Grade, "request %v", c.request)
	}
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
	// max(P95 95 x 1.2, P99 99) = 114; the limit is max(114, Max 100) x 1.2
	assert.InDelta(t, 114.0, cpu.Suggested, 1e-9)
	assert.InDelta(t, 136.8, cpu.SuggestedLimit, 1e-9)
	assert.Equal(t, 0.0, cpu.Ratio)
	assert.Equal(t, "", cpu.Grade)

	mem := Analyze(input(Memory, 0, ramp(100, 1)), p)
	assert.GreaterOrEqual(t, mem.SuggestedLimit, mem.Suggested, "a memory limit is never below the suggested request")
	assert.GreaterOrEqual(t, mem.SuggestedLimit, mem.Usage.P99, "a limit is never below the P99")

	// with a request the limit is the same, and the status is judged
	with := Analyze(input(CPU, 200, ramp(100, 1)), p)
	assert.InDelta(t, cpu.SuggestedLimit, with.SuggestedLimit, 1e-9)
	assert.NotEqual(t, StatusUnknown, with.Status)
}
