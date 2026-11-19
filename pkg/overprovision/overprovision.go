// Package overprovision is the over-provisioning analysis (P10 A2): from what a Pod really used over a window, whether the request it was given is
// more than it needs, and what it could be lowered to. It is a pure function of the measured values, the request and what is known of throttling,
// OOM kills and the JVM; it reads nothing, predicts nothing and changes nothing. It is not given a forecast: a forecast takes no part in the
// verdict (P10 boundary).
//
// The formulas and the limits are the proposal of P10 A2. They have not been tested on real data, which is why every number is a parameter of
// the platform (Params) and every result carries the formula, the numbers put into it and its version.
package overprovision

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// FormulaVersion changes whenever a formula or a rule below changes, so a stored result says which one it was made with.
const FormulaVersion = "op-1"

// The resources of the analysis.
const (
	CPU    = "cpu"
	Memory = "memory"
)

// Statuses of a result.
const (
	// StatusOver: the request is more than the suggestion by at least the mildest grade.
	StatusOver = "over"
	// StatusOK: the request is near what the use asks for.
	StatusOK = "ok"
	// StatusUnder: the request is below the P95 use (a request to raise, not to lower).
	StatusUnder = "under"
	// StatusLimited: the use understates the need (the CPU was throttled, or the memory was OOM-killed), so no lowering is suggested.
	StatusLimited = "limited"
	// StatusUnknown: not enough data (or no request) to say anything.
	StatusUnknown = "unknown"
)

// Grades of an over-provisioned request, by the share of it that is more than the suggestion.
const (
	GradeSevere   = "severe"
	GradeModerate = "moderate"
	GradeMild     = "mild"
	// GradeReasonable is a request within the mildest share of the suggestion.
	GradeReasonable = "reasonable"
)

// Levels of confidence and risk.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Codes of the reasons a result gives (the page words them).
const (
	ReasonNoRequest     = "no_request"      // the request of the Pod is not known
	ReasonNoUsage       = "no_usage"        // nothing was measured in the window
	ReasonCoverageLow   = "coverage_low"    // too small a share of the window has data: no suggestion
	ReasonCoverageMid   = "coverage_medium" // part of the window has no data
	ReasonShortWindow   = "window_short"    // the window is shorter than the baseline window
	ReasonJVMMissing    = "jvm_missing"     // the module has JVM metrics set up but none was measured
	ReasonThrottled     = "throttled"       // the CPU was throttled: the use is lower than the need
	ReasonOOM           = "oom"             // the memory was OOM-killed in the window
	ReasonPeakNearLimit = "peak_near_limit" // the peak came near the limit
	ReasonBelowP95      = "request_below_p95"
	ReasonNoLimit       = "no_limit" // the limit is not known: the distance to it is not judged
)

// Window is one of the windows of the analysis.
type Window struct {
	Label   string `json:"label"`
	Minutes int    `json:"minutes"`
}

// Windows are the windows analysed regularly: 15 and 30 minutes, 1 hour, 1, 7, 15 and 30 days. A custom range is analysed only when it is asked for.
var Windows = []Window{
	{"15m", 15}, {"30m", 30}, {"1h", 60}, {"1d", 24 * 60}, {"7d", 7 * 24 * 60}, {"15d", 15 * 24 * 60}, {"30d", 30 * 24 * 60},
}

// WindowByMinutes finds a window of the list.
func WindowByMinutes(minutes int) (Window, bool) {
	for _, w := range Windows {
		if w.Minutes == minutes {
			return w, true
		}
	}
	return Window{}, false
}

// Params are the numbers of the formulas and the limits of the verdict.
type Params struct {
	// CPUMargin is added to the P95 CPU use (0.2 = 20%); MemoryMargin to the P99 working set; MemoryPeakFactor multiplies the highest working set.
	CPUMargin        float64 `json:"cpu_margin"`
	MemoryMargin     float64 `json:"memory_margin"`
	MemoryPeakFactor float64 `json:"memory_peak_factor"`
	// OOMUplift multiplies the working set an OOM kill was seen at: the suggestion is never below it.
	OOMUplift float64 `json:"oom_uplift"`
	// ThrottleLimit is the share of time the CPU was throttled above which the use is said to understate the need.
	ThrottleLimit float64 `json:"throttle_limit"`
	// BaselineMinutes is the window a result is a baseline for: a shorter window lowers the confidence by one level.
	BaselineMinutes int `json:"baseline_minutes"`
	// Severe, Moderate and Mild are the shares of the request that are more than the suggestion for each grade.
	Severe   float64 `json:"severe"`
	Moderate float64 `json:"moderate"`
	Mild     float64 `json:"mild"`
	// CoverageHigh and CoverageMid are the shares of the window that must have data for a high and a medium confidence; under CoverageMid no
	// suggestion is made.
	CoverageHigh float64 `json:"coverage_high"`
	CoverageMid  float64 `json:"coverage_mid"`
	// PeakNearLimit is the share of the limit the highest use may reach before the risk goes up.
	PeakNearLimit float64 `json:"peak_near_limit"`
}

// DefaultParams are the proposals of P10 A2.
func DefaultParams() Params {
	return Params{CPUMargin: 0.20, MemoryMargin: 0.15, MemoryPeakFactor: 1.05, OOMUplift: 1.2, ThrottleLimit: 0.05, BaselineMinutes: 7 * 24 * 60,
		Severe: 0.50, Moderate: 0.30, Mild: 0.15, CoverageHigh: 0.80, CoverageMid: 0.50, PeakNearLimit: 0.90}
}

// Point is one measured value of a Pod at a moment (unix seconds).
type Point struct {
	TS int64
	V  float64
}

// Usage is the quantiles of the measured values of a window by the nearest-rank rule; values that are not finite or are negative are dropped.
type Usage struct {
	Points int     `json:"points"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

// UsageOf takes the quantiles of the values.
func UsageOf(points []Point) Usage {
	vals := make([]float64, 0, len(points))
	for _, p := range points {
		if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) && p.V >= 0 {
			vals = append(vals, p.V)
		}
	}
	if len(vals) == 0 {
		return Usage{}
	}
	sort.Float64s(vals)
	return Usage{Points: len(vals), P50: rank(vals, 50), P90: rank(vals, 90), P95: rank(vals, 95), P99: rank(vals, 99), Max: vals[len(vals)-1]}
}

func rank(sorted []float64, p float64) float64 {
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// Input is what one resource of one workload is analysed from. Points are the values of one Pod (the average Pod of the workload when the stored
// series is a workload total divided by its replicas); Total, when there is one, is the sum over all the Pods at each moment.
type Input struct {
	Resource      string
	Request       float64
	Limit         float64 // 0 = not known
	LimitFrom     string
	Points        []Point
	Total         []Point
	StepSeconds   int
	WindowMinutes int
	// Throttle is the share of time the CPU was throttled in the window (seconds throttled per second); a negative value says it was not measured.
	Throttle float64
	// OOMKills is how many OOM kills the window saw; OOMWorkingSet the highest working set measured when one happened (0 = not known).
	OOMKills      float64
	OOMWorkingSet float64
	// JVMExpected is true when the module has JVM metrics set up; JVMPresent when some were measured in the window.
	JVMExpected bool
	JVMPresent  bool
	// Round makes a value one a Pod spec can say (nil leaves it).
	Round func(float64) float64
}

// Result is the analysis of one resource over one window. CPU is in cores, memory in bytes, per Pod unless it says Total.
type Result struct {
	Resource      string  `json:"resource"`
	WindowMinutes int     `json:"window_minutes"`
	Request       float64 `json:"request"`
	Limit         float64 `json:"limit"`
	// LimitFrom says where the limit comes from: metric (a limits source of the module), module (the request times the limit ratio the module sets for admission) or empty (not known).
	LimitFrom string `json:"limit_from"`
	Usage     Usage  `json:"usage"`
	// Total is the quantiles of the sum over all the Pods (nil when it could not be made).
	Total *Usage `json:"total,omitempty"`
	// Coverage is the share of the window that has data (0 to 1).
	Coverage       float64 `json:"coverage"`
	ExpectedPoints int     `json:"expected_points"`
	// Suggested is the request the use supports; 0 when no suggestion is made (see Reasons).
	Suggested float64 `json:"suggested"`
	// Ratio is (Request - Suggested) / Request, with Numerator and Denominator as put into it; it is negative when the suggestion is above the request.
	Ratio       float64 `json:"ratio"`
	Numerator   float64 `json:"numerator"`
	Denominator float64 `json:"denominator"`
	// Reclaimable is what a Pod could give back (Request - Suggested, never below zero).
	Reclaimable float64 `json:"reclaimable"`
	Status      string  `json:"status"`
	Grade       string  `json:"grade"`
	Confidence  string  `json:"confidence"`
	Risk        string  `json:"risk"`
	// Throttle, OOMKills and OOMWorkingSet echo what was put in, so the page can show it.
	Throttle      float64 `json:"throttle"`
	OOMKills      float64 `json:"oom_kills"`
	OOMWorkingSet float64 `json:"oom_working_set"`
	// Reasons are codes of why the result is what it is (the page words them).
	Reasons []string `json:"reasons"`
	// Formula is the suggestion written out with the numbers put into it.
	Formula        string `json:"formula"`
	FormulaVersion string `json:"formula_version"`
}

func down(level string) string {
	switch level {
	case High:
		return Medium
	case Medium:
		return Low
	}
	return Low
}

func up(level string) string {
	switch level {
	case Low:
		return Medium
	case Medium:
		return High
	}
	return High
}

func num(resource string, v float64) string {
	if resource == Memory {
		return fmt.Sprintf("%.0f MiB", v/(1<<20))
	}
	return fmt.Sprintf("%.3f", v)
}

// Analyze analyses one resource. It never panics on missing data: it says unknown, with the reason.
func Analyze(in Input, p Params) Result {
	r := Result{Resource: in.Resource, WindowMinutes: in.WindowMinutes, Request: in.Request, Limit: in.Limit, LimitFrom: in.LimitFrom, Usage: UsageOf(in.Points), Reasons: []string{},
		Confidence: Low, Risk: Low, Status: StatusUnknown, Grade: "", Throttle: in.Throttle, OOMKills: in.OOMKills, OOMWorkingSet: in.OOMWorkingSet,
		FormulaVersion: FormulaVersion}
	if r.Throttle < 0 {
		r.Throttle = 0
	}
	if len(in.Total) > 0 {
		t := UsageOf(in.Total)
		r.Total = &t
	}
	if in.StepSeconds > 0 && in.WindowMinutes > 0 {
		r.ExpectedPoints = in.WindowMinutes * 60 / in.StepSeconds
		if r.ExpectedPoints < 1 {
			r.ExpectedPoints = 1
		}
		r.Coverage = math.Min(1, float64(r.Usage.Points)/float64(r.ExpectedPoints))
	}
	switch {
	case !(in.Request > 0):
		r.Reasons = append(r.Reasons, ReasonNoRequest)
		return r
	case r.Usage.Points == 0:
		r.Reasons = append(r.Reasons, ReasonNoUsage)
		return r
	}

	// Confidence: the coverage of the window, one level down for a window shorter than the baseline and one for the JVM metrics that are missing.
	switch {
	case r.Coverage >= p.CoverageHigh:
		r.Confidence = High
	case r.Coverage >= p.CoverageMid:
		r.Confidence, r.Reasons = Medium, append(r.Reasons, ReasonCoverageMid)
	default:
		r.Confidence, r.Reasons = Low, append(r.Reasons, ReasonCoverageLow)
	}
	if p.BaselineMinutes > 0 && in.WindowMinutes < p.BaselineMinutes {
		r.Confidence, r.Reasons = down(r.Confidence), append(r.Reasons, ReasonShortWindow)
	}
	if in.JVMExpected && !in.JVMPresent {
		r.Confidence, r.Reasons = down(r.Confidence), append(r.Reasons, ReasonJVMMissing)
	}
	if r.Coverage < p.CoverageMid {
		// not enough of the window to say what the use is: no suggestion
		return r
	}

	// The suggestion.
	u := r.Usage
	var suggested float64
	switch in.Resource {
	case Memory:
		a, b := u.P99*(1+p.MemoryMargin), u.Max*p.MemoryPeakFactor
		suggested = math.Max(a, b)
		r.Formula = fmt.Sprintf("max(P99 %s x (1+%.0f%%) = %s, Max %s x %.2f = %s)", num(Memory, u.P99), p.MemoryMargin*100, num(Memory, a),
			num(Memory, u.Max), p.MemoryPeakFactor, num(Memory, b))
	default:
		a := u.P95 * (1 + p.CPUMargin)
		suggested = math.Max(a, u.P99)
		r.Formula = fmt.Sprintf("max(P95 %s x (1+%.0f%%) = %s, P99 %s)", num(CPU, u.P95), p.CPUMargin*100, num(CPU, a), num(CPU, u.P99))
	}
	limited := false
	if in.Resource == Memory && in.OOMKills > 0 {
		// an OOM kill: the memory was too little, so it is never advised lower than what it was killed at, and never lower than it is now
		floor := math.Max(in.OOMWorkingSet*p.OOMUplift, in.Request)
		suggested = math.Max(suggested, floor)
		r.Reasons = append(r.Reasons, ReasonOOM)
		r.Risk = High
		limited = true
		r.Formula += fmt.Sprintf("; OOM: not below max(working set %s x %.2f, request %s)", num(Memory, in.OOMWorkingSet), p.OOMUplift, num(Memory, in.Request))
	}
	if in.Resource == CPU && in.Throttle > p.ThrottleLimit {
		// throttled: the use is what the CPU was allowed, not what it wanted
		suggested = math.Max(suggested, in.Request)
		r.Reasons = append(r.Reasons, ReasonThrottled)
		r.Risk = up(r.Risk)
		limited = true
		r.Formula += fmt.Sprintf("; throttled %.1f%% of the time (limit %.0f%%): not below the request", in.Throttle*100, p.ThrottleLimit*100)
	}
	if in.Round != nil {
		suggested = in.Round(suggested)
	}
	r.Suggested = suggested
	r.Numerator, r.Denominator = in.Request-suggested, in.Request
	r.Ratio = r.Numerator / r.Denominator
	r.Reclaimable = math.Max(0, r.Numerator)

	switch {
	case limited:
		r.Status = StatusLimited
	case in.Request < u.P95:
		r.Status = StatusUnder
		r.Reasons = append(r.Reasons, ReasonBelowP95)
		r.Risk = up(r.Risk)
	case r.Ratio >= p.Mild:
		r.Status = StatusOver
	default:
		r.Status = StatusOK
	}
	switch {
	case r.Ratio >= p.Severe:
		r.Grade = GradeSevere
	case r.Ratio >= p.Moderate:
		r.Grade = GradeModerate
	case r.Ratio >= p.Mild:
		r.Grade = GradeMild
	default:
		r.Grade = GradeReasonable
	}
	if r.Status != StatusOver {
		// the grade is of an over-provisioned request; a request that is not one is reasonable, and says so
		r.Grade = GradeReasonable
	}
	if in.Limit > 0 {
		if u.Max >= p.PeakNearLimit*in.Limit {
			r.Risk = up(r.Risk)
			r.Reasons = append(r.Reasons, ReasonPeakNearLimit)
		}
	} else {
		r.Reasons = append(r.Reasons, ReasonNoLimit)
	}
	return r
}

// MovingAverage is the mean of the last n points at each point (fewer at the start). It is written MA<n>@<step>: n points of the step of the series.
func MovingAverage(points []Point, n int) []Point {
	if n <= 1 || len(points) == 0 {
		return append([]Point(nil), points...)
	}
	out := make([]Point, len(points))
	sum := 0.0
	for i, p := range points {
		sum += p.V
		if i >= n {
			sum -= points[i-n].V
		}
		count := n
		if i+1 < n {
			count = i + 1
		}
		out[i] = Point{TS: p.TS, V: sum / float64(count)}
	}
	return out
}

// Resample averages the points into buckets of step seconds (a step not above the series' own step returns it as it is).
func Resample(points []Point, step int) []Point {
	if step <= 0 || len(points) < 2 {
		return append([]Point(nil), points...)
	}
	var out []Point
	var bucket int64 = -1
	sum, count := 0.0, 0
	flush := func() {
		if count > 0 {
			out = append(out, Point{TS: bucket * int64(step), V: sum / float64(count)})
		}
	}
	for _, p := range points {
		b := p.TS / int64(step)
		if b != bucket {
			flush()
			bucket, sum, count = b, 0, 0
		}
		sum += p.V
		count++
	}
	flush()
	return out
}

// MAStepFor is the step the moving averages of a window are read in: 1 minute for a window of up to an hour, 5 minutes for up to 7 days, 15 for the
// longer ones. A series that was stored more coarsely is read in its own step; the label says which step it was.
func MAStepFor(windowMinutes, storedStep int) int {
	step := 60
	switch {
	case windowMinutes > 7*24*60:
		step = 15 * 60
	case windowMinutes > 60:
		step = 5 * 60
	}
	if storedStep > step {
		step = storedStep
	}
	return step
}

// MALabel writes a moving average the way the page shows it, with the step after it: MA5@1m is the mean of the last 5 minutes.
func MALabel(n, stepSeconds int) string {
	var step string
	switch {
	case stepSeconds%3600 == 0:
		step = fmt.Sprintf("%dh", stepSeconds/3600)
	case stepSeconds%60 == 0:
		step = fmt.Sprintf("%dm", stepSeconds/60)
	default:
		step = fmt.Sprintf("%ds", stepSeconds)
	}
	return fmt.Sprintf("MA%d@%s", n, step)
}

// JoinReasons writes the reason codes in one string for a column.
func JoinReasons(reasons []string) string { return strings.Join(reasons, ",") }
