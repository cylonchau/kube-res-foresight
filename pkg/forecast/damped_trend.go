package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// DampedTrend forecasts a slow climb from the newest stretch of the history: the level is an EWMA, the slope is the median of the slopes
// between the points of the last Window steps (Theil-Sen), and the slope is followed for fewer and fewer steps, so the forecast bends
// towards a ceiling instead of running away:
//
//	ŷ(T+h) = level + slope · (φ + φ² + … + φʰ)
//
// which, for φ < 1, never goes further than level + slope·φ/(1−φ). It starts from a few dozen samples, follows the load as it is now, and
// does not need a season: a memory that creeps up for hours, with sudden drops from restarts or garbage collection mixed in.
//
//   - The median slope is not moved by one point: an isolated spike or one sudden fall in the window leaves it where the rest of the window puts it.
//   - A falling slope is followed only DownShare of the way (0 = never; the forecast is flat). Capacity is given back slowly; that is a policy
//     of the decision, which keeps its own hold, and the forecast does not hurry it.
//   - The forecast is never taken above CapRatio times the highest value of the window, so a steep short slope cannot be extrapolated far,
//     and never below 0.
//
// What it does not do: see a peak coming before the load has started to rise, or know a daily cycle. A burst without warning needs capacity in
// reserve; this is a forecast of where a rise that has begun goes.
type DampedTrend struct {
	Alpha     float64 // EWMA coefficient of the level, (0, 1]
	Window    int     // steps the slope is taken over
	Damping   float64 // φ in (0, 1]; 1 = no damping
	DownShare float64 // share of a falling slope that is followed, 0..1
	CapRatio  float64 // the forecast stays at most this times the highest value of the window, >= 1
}

func (DampedTrend) ID() string { return "damped_trend" }

// MinSamples is the window: the slope is not taken from fewer points than it asks for.
func (m DampedTrend) MinSamples() int { return max(m.Window, 3) }

func (m DampedTrend) validate() error {
	switch {
	case m.Alpha <= 0 || m.Alpha > 1:
		return fmt.Errorf("alpha must be in (0, 1]")
	case m.Window < 3:
		return fmt.Errorf("window_steps must be at least 3")
	case m.Damping <= 0 || m.Damping > 1:
		return fmt.Errorf("damping must be in (0, 1]")
	case m.DownShare < 0 || m.DownShare > 1:
		return fmt.Errorf("down_share must be in [0, 1]")
	case m.CapRatio < 1:
		return fmt.Errorf("cap_ratio must be at least 1")
	}
	return nil
}

// theilSen is the median of the slopes between all pairs of points of values (per step).
func theilSen(values []float64) float64 {
	n := len(values)
	slopes := make([]float64, 0, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			slopes = append(slopes, (values[j]-values[i])/float64(j-i))
		}
	}
	sort.Float64s(slopes)
	if len(slopes)%2 == 1 {
		return slopes[len(slopes)/2]
	}
	return (slopes[len(slopes)/2-1] + slopes[len(slopes)/2]) / 2
}

func (m DampedTrend) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	level := in.Values[0]
	for _, v := range in.Values[1:] {
		level = m.Alpha*v + (1-m.Alpha)*level
	}
	recent := in.Values[len(in.Values)-m.Window:]
	slope := theilSen(recent)
	if slope < 0 {
		slope *= m.DownShare
	}
	top := recent[0]
	for _, v := range recent {
		top = math.Max(top, v)
	}
	ceiling := m.CapRatio * top
	out := make([]float64, horizon)
	reach, power := 0.0, 1.0
	for i := range out {
		power *= m.Damping
		reach += power
		v := level + slope*reach
		v = math.Min(v, math.Max(ceiling, level)) // never above the cap (and a level already above it is not pulled down by it)
		out[i] = math.Max(v, 0)
	}
	return out, nil
}
