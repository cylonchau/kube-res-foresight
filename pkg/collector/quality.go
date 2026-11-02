package collector

import (
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
)

// Gap is a run of missing grid steps.
type Gap struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Steps int       `json:"steps"`
}

// Quality describes how complete and plausible a window of samples is. NaN, missing steps and jumps are
// reported, never repaired in the stored data.
type Quality struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	StepSeconds     int       `json:"step_seconds"`
	Expected        int       `json:"expected"`
	Present         int       `json:"present"` // steps with a finite value
	NaN             int       `json:"nan"`
	MissingRatio    float64   `json:"missing_ratio"`
	Gaps            []Gap     `json:"gaps"`
	LongestGapSteps int       `json:"longest_gap_steps"`
	Jumps           int       `json:"jumps"`
	Duplicates      int       `json:"duplicates"` // samples off the grid or repeated
	LatestSample    time.Time `json:"latest_sample"`
	LatestLagSecs   int       `json:"latest_lag_seconds"` // now - latest finite sample, the delay of the source
}

const maxReportedGaps = 200

// Analyze compares points with the expected grid start, start+step, ... end. Samples off the grid are
// counted as Duplicates and ignored, so a shifted source is visible instead of silently accepted.
func Analyze(points []Point, start, end time.Time, step time.Duration, now time.Time) Quality {
	q := Quality{Start: start, End: end, StepSeconds: int(step.Seconds())}
	if step <= 0 || end.Before(start) {
		return q
	}
	q.Expected = int(end.Sub(start)/step) + 1
	present := make([]bool, q.Expected)
	values := make([]float64, q.Expected)
	for _, p := range points {
		off := p.Time.Sub(start)
		if off < 0 || off%step != 0 {
			q.Duplicates++
			continue
		}
		i := int(off / step)
		if i >= q.Expected || present[i] {
			q.Duplicates++
			continue
		}
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			q.NaN++
			continue
		}
		present[i] = true
		values[i] = p.Value
		q.Present++
		if p.Time.After(q.LatestSample) {
			q.LatestSample = p.Time
		}
	}
	if q.Expected > 0 {
		q.MissingRatio = float64(q.Expected-q.Present) / float64(q.Expected)
	}
	for i := 0; i < q.Expected; {
		if present[i] {
			i++
			continue
		}
		j := i
		for j < q.Expected && !present[j] {
			j++
		}
		if j-i > q.LongestGapSteps {
			q.LongestGapSteps = j - i
		}
		if len(q.Gaps) < maxReportedGaps {
			q.Gaps = append(q.Gaps, Gap{From: start.Add(time.Duration(i) * step), To: start.Add(time.Duration(j-1) * step), Steps: j - i})
		}
		i = j
	}
	if !q.LatestSample.IsZero() && now.After(q.LatestSample) {
		q.LatestLagSecs = int(now.Sub(q.LatestSample).Seconds())
	}
	q.Jumps = countJumps(present, values)
	return q
}

// countJumps counts step-to-step changes that are far outside the series' own typical change: more than
// 10 robust standard deviations (median + 10*1.4826*MAD of the absolute differences) and above a floor of
// 5% of the mean level so a flat series with tiny noise does not flag everything.
func countJumps(present []bool, values []float64) int {
	var diffs []float64
	var sum float64
	var n int
	for i := 1; i < len(values); i++ {
		if present[i] && present[i-1] {
			diffs = append(diffs, math.Abs(values[i]-values[i-1]))
		}
	}
	for i, ok := range present {
		if ok {
			sum += math.Abs(values[i])
			n++
		}
	}
	if len(diffs) < 10 || n == 0 {
		return 0
	}
	sorted := append([]float64(nil), diffs...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	devs := make([]float64, len(sorted))
	for i, d := range sorted {
		devs[i] = math.Abs(d - median)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]
	threshold := median + 10*1.4826*mad
	if floor := 0.05 * sum / float64(n); threshold < floor {
		threshold = floor
	}
	jumps := 0
	for _, d := range diffs {
		if d > threshold {
			jumps++
		}
	}
	return jumps
}

// Ways ToInput fills a step without a sample (see model.SourceMissing).
const (
	FillInterpolate = "interpolate"
	FillForward     = "forward_fill"
	FillZero        = "zero"
	FillSkip        = "skip"
)

// ToInput lays points on the grid start..end and returns the longest usable run ending at the latest sample:
// gaps of at most maxFillSteps are filled the way fill says (a straight line, the last sample, zero; skip fills
// nothing), a longer gap ends the run, so the model trains only on contiguous data. The bool is false when nothing
// usable remains. Filled counts the filled steps.
func ToInput(points []Point, start, end time.Time, step time.Duration, maxFillSteps int, fill string) (in forecast.Input, filled int, ok bool) {
	if step <= 0 || end.Before(start) {
		return in, 0, false
	}
	n := int(end.Sub(start)/step) + 1
	vals := make([]float64, n)
	for i := range vals {
		vals[i] = math.NaN()
	}
	for _, p := range points {
		off := p.Time.Sub(start)
		if off < 0 || off%step != 0 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		if i := int(off / step); i < n {
			vals[i] = p.Value
		}
	}
	last := n - 1
	for last >= 0 && math.IsNaN(vals[last]) {
		last--
	}
	if last < 0 {
		return in, 0, false
	}
	// walk back from the newest sample, interpolating short gaps and stopping at a long one
	runStart := last
	i := last
	for i > 0 {
		if !math.IsNaN(vals[i-1]) {
			i--
			runStart = i
			continue
		}
		j := i - 1
		for j >= 0 && math.IsNaN(vals[j]) {
			j--
		}
		gap := i - 1 - j
		if j < 0 || gap > maxFillSteps || fill == FillSkip {
			break
		}
		for k := 1; k <= gap; k++ {
			switch fill {
			case FillForward:
				vals[j+k] = vals[j]
			case FillZero:
				vals[j+k] = 0
			default:
				vals[j+k] = vals[j] + (vals[i]-vals[j])*float64(k)/float64(gap+1)
			}
		}
		filled += gap
		i = j
		runStart = j
	}
	out := vals[runStart : last+1]
	return forecast.Input{Values: out, Step: step, End: start.Add(time.Duration(last) * step)}, filled, true
}
