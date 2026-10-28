package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// PeakEnvelope forecasts the upper envelope of a load, for a resource whose limit is set by its peaks and not by its average:
// a memory working set is a saw (it grows, the garbage collector or a cache eviction drops it) on top of a slow rise (a leak, a
// cache that fills). The mean of such a series is far below what the process needs, and a model that predicts the mean
// recommends a limit the next peak breaks.
//
// The envelope is the rolling maximum over the last EnvelopeWindow steps (only the steps before each point). A straight line is
// fitted to it with the Theil-Sen estimator, the median of the slopes between every pair of points (and the median of the
// intercepts), which a few odd peaks or a collapse after a restart cannot drag, unlike a least-squares line. The forecast is
// that line carried forward. Unless AllowDecline is set the line never falls below the envelope of now (a load that has just
// peaked is not forecast to be smaller than that peak) and a falling slope is read as flat: it is a recommendation for
// protection, not an estimate of the average. The forecast is never below zero.
//
// TimeToLimit says when the line reaches a limit (the memory limit of the container): the forecast of a leak is also a date.
type PeakEnvelope struct {
	EnvelopeWindow int // steps of the rolling maximum
	FitSteps       int // the newest steps of the envelope that are fitted; 0 = all of it
	AllowDecline   bool
	SafetyMargin   float64
}

func (PeakEnvelope) ID() string { return "peak_envelope" }

// MinSamples is a window of the envelope and room for a line through it.
func (m PeakEnvelope) MinSamples() int { return m.EnvelopeWindow + 10 }

// theilSenPoints is the most points the pairwise slopes are taken over: 300 points are 45,000 pairs.
const theilSenPoints = 300

func (m PeakEnvelope) validate() error {
	if m.EnvelopeWindow < 2 {
		return fmt.Errorf("envelope_window_steps must be at least 2")
	}
	if m.FitSteps < 0 {
		return fmt.Errorf("fit_steps must not be negative")
	}
	if m.SafetyMargin < 0 {
		return fmt.Errorf("safety_margin must not be negative")
	}
	return nil
}

// envelope is the rolling maximum of y over the trailing window w, from index w-1 on (earlier points have a shorter window and
// would sit low).
func envelope(y []float64, w int) (values []float64, first int) {
	n := len(y)
	if n < w {
		return nil, 0
	}
	values = make([]float64, 0, n-w+1)
	var deque []int // indices, their values decreasing
	for i := 0; i < n; i++ {
		for len(deque) > 0 && y[deque[len(deque)-1]] <= y[i] {
			deque = deque[:len(deque)-1]
		}
		deque = append(deque, i)
		if deque[0] <= i-w {
			deque = deque[1:]
		}
		if i >= w-1 {
			values = append(values, y[deque[0]])
		}
	}
	return values, w - 1
}

func medianOf(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// trendLine is the Theil-Sen line (value at x = intercept + slope*x, x the index in the input) of the envelope, and the
// envelope value now.
func (m PeakEnvelope) trendLine(in Input) (slope, intercept, now float64, err error) {
	if err := m.validate(); err != nil {
		return 0, 0, 0, err
	}
	if err := checkInput(m, in, 1); err != nil {
		return 0, 0, 0, err
	}
	env, first := envelope(in.Values, m.EnvelopeWindow)
	now = env[len(env)-1]
	from := 0
	if m.FitSteps > 0 && m.FitSteps < len(env) {
		from = len(env) - m.FitSteps
	}
	idx := make([]int, 0, theilSenPoints)
	count := len(env) - from
	stride := (count + theilSenPoints - 1) / theilSenPoints
	if stride < 1 {
		stride = 1
	}
	for i := len(env) - 1; i >= from; i -= stride { // anchored at the newest point
		idx = append(idx, i)
	}
	if len(idx) < 3 {
		return 0, now, now, fmt.Errorf("%w: peak_envelope needs at least 3 points of the envelope to fit", ErrInsufficientHistory)
	}
	slopes := make([]float64, 0, len(idx)*(len(idx)-1)/2)
	for a := 0; a < len(idx); a++ {
		for b := a + 1; b < len(idx); b++ {
			dx := float64(idx[a] - idx[b]) // idx is descending: a is the newer
			slopes = append(slopes, (env[idx[a]]-env[idx[b]])/dx)
		}
	}
	slope = medianOf(slopes)
	if !m.AllowDecline && slope < 0 {
		slope = 0
	}
	resid := make([]float64, len(idx))
	for k, i := range idx {
		resid[k] = env[i] - slope*float64(i+first)
	}
	return slope, medianOf(resid), now, nil
}

func (m PeakEnvelope) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slope, intercept, now, err := m.trendLine(in)
	if err != nil {
		return nil, err
	}
	n := len(in.Values)
	out := make([]float64, horizon)
	for h := 1; h <= horizon; h++ {
		v := intercept + slope*float64(n-1+h)
		if !m.AllowDecline && v < now {
			v = now // the envelope of now is the least the next peak is
		}
		if v < 0 {
			v = 0
		}
		out[h-1] = v * (1 + m.SafetyMargin)
	}
	return out, finite(out)
}

// TimeToLimit is how long, from the newest sample, until the trend line of the envelope reaches limit: 0 when the envelope is
// already there, and false when it never will (the line is flat or falling and below the limit) or not within ten times the length
// of the history, which is as far as a trend fitted to that history can honestly look.
func (m PeakEnvelope) TimeToLimit(in Input, limit float64) (time.Duration, bool, error) {
	slope, intercept, now, err := m.trendLine(in)
	if err != nil {
		return 0, false, err
	}
	if now >= limit {
		return 0, true, nil
	}
	if slope <= 0 {
		return 0, false, nil
	}
	n := float64(len(in.Values))
	steps := (limit-intercept)/slope - (n - 1)
	// a slope that is noise takes forever: a date further away than ten times the history says nothing a history can support
	if steps < 0 || math.IsNaN(steps) || math.IsInf(steps, 0) || steps > 10*n {
		return 0, false, nil
	}
	return time.Duration(steps * float64(in.Step)), true, nil
}
