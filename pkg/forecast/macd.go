package forecast

import (
	"context"
	"fmt"
	"math"
)

// MACDTrend extrapolates a resource series from the structure of the moving average convergence/divergence
// indicator: the difference of a fast and a slow exponential moving average is a smoothed slope, and a third
// moving average of that difference (the signal line) says whether the slope is being confirmed. It is used as a
// trend correction for a resource forecast, never as a trading signal, and it has no notion of price.
//
// The forecast starts at the fast average, adds the slope for the steps ahead, and is capped to MaxAdjustment of
// the current level and floored at zero (a resource is never negative).
type MACDTrend struct {
	Fast, Slow, Signal int
	MaxAdjustment      float64
}

func (MACDTrend) ID() string { return "macd_trend" }

// MinSamples needs the slow average to settle and the signal line to form.
func (m MACDTrend) MinSamples() int { return m.Slow + m.Signal }

func emaSeries(values []float64, span int) []float64 {
	alpha := 2 / (float64(span) + 1)
	out := make([]float64, len(values))
	out[0] = values[0]
	for i := 1; i < len(values); i++ {
		out[i] = alpha*values[i] + (1-alpha)*out[i-1]
	}
	return out
}

func (m MACDTrend) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	if m.Fast < 2 || m.Slow <= m.Fast || m.Signal < 2 {
		return nil, fmt.Errorf("macd_trend needs 2 <= fast_span < slow_span and signal_span >= 2")
	}
	if m.MaxAdjustment < 0 {
		return nil, fmt.Errorf("max_adjustment must not be negative")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	fast := emaSeries(in.Values, m.Fast)
	slow := emaSeries(in.Values, m.Slow)
	line := make([]float64, len(in.Values))
	for i := range line {
		line[i] = fast[i] - slow[i]
	}
	signal := emaSeries(line, m.Signal)
	last := len(in.Values) - 1
	macd, histogram := line[last], line[last]-signal[last]

	// an EMA of span k lags the series by (k-1)/2 steps, so the line is about slope x (slowLag - fastLag)
	lagGap := float64(m.Slow-m.Fast) / 2
	slope := macd / lagGap
	if macd*histogram < 0 {
		slope /= 2 // the signal line does not confirm the move: take half of it
	}
	level := fast[last]
	limit := m.MaxAdjustment * math.Abs(level)
	out := make([]float64, horizon)
	for h := range out {
		adjust := slope * (float64(h+1) + float64(m.Fast-1)/2)
		adjust = math.Max(-limit, math.Min(limit, adjust))
		out[h] = math.Max(0, level+adjust)
	}
	return out, finite(out)
}
