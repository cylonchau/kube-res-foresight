package forecast

import (
	"context"
	"fmt"
	"math"
)

// Theta is the Theta method (Assimakopoulos and Nikolopoulos, the winner of the M3 competition) in its simple-exponential
// -smoothing form (Hyndman and Billah): the forecast is the simple exponential smoothing of the series plus a drift of
// half the slope of the linear trend,
//
//	forecast(h) = level + slope/2 * ((h-1) + 1/alpha - (1-alpha)^n / alpha)
//
// so the level follows the recent past and the trend is damped to half, which is why it is hard to beat on short, noisy
// series. A seasonal series is first divided by (or, when it touches zero, has subtracted) its classical seasonal
// indices and the indices are put back on the forecast. The smoothing coefficient is chosen by the in-sample one-step
// error when Alpha is zero. The forecast is floored at zero: a resource is never negative.
type Theta struct {
	Season int     // steps per season; 0 or 1 = non-seasonal
	Alpha  float64 // smoothing coefficient in (0, 1]; 0 = choose automatically
}

func (Theta) ID() string { return "theta" }

func (m Theta) seasonal() bool { return m.Season > 1 }

// MinSamples is two full seasons for the seasonal indices, otherwise four points for a slope.
func (m Theta) MinSamples() int {
	if m.seasonal() {
		return 2 * m.Season
	}
	return 4
}

// seasonalIndices are the classical decomposition indices of y for a season of s steps, indexed by position t%s: the
// ratios (multiplicative) or differences (additive) of the series to its centred moving average, averaged per position and
// normalised to mean 1 (or 0).
func seasonalIndices(y []float64, s int, multiplicative bool) []float64 {
	n := len(y)
	sum := make([]float64, s)
	count := make([]int, s)
	half := s / 2
	for t := half; t < n-half; t++ {
		var ma float64
		if s%2 == 0 {
			for k := -half; k <= half; k++ {
				w := 1.0
				if k == -half || k == half {
					w = 0.5
				}
				ma += w * y[t+k]
			}
			ma /= float64(s)
		} else {
			for k := -half; k <= half; k++ {
				ma += y[t+k]
			}
			ma /= float64(s)
		}
		pos := t % s
		if multiplicative {
			if ma == 0 {
				continue
			}
			sum[pos] += y[t] / ma
		} else {
			sum[pos] += y[t] - ma
		}
		count[pos]++
	}
	idx := make([]float64, s)
	var mean float64
	for i := range idx {
		if count[i] > 0 {
			idx[i] = sum[i] / float64(count[i])
		} else if multiplicative {
			idx[i] = 1
		}
		mean += idx[i]
	}
	mean /= float64(s)
	for i := range idx {
		if multiplicative {
			if mean != 0 {
				idx[i] /= mean
			}
		} else {
			idx[i] -= mean
		}
	}
	return idx
}

// sesLevel runs simple exponential smoothing and returns the final level and the sum of squared one-step errors.
func sesLevel(x []float64, alpha float64) (level, sse float64) {
	level = x[0]
	for _, v := range x[1:] {
		e := v - level
		sse += e * e
		level += alpha * e
	}
	return level, sse
}

func (m Theta) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if m.Season < 0 {
		return nil, fmt.Errorf("season_length_steps must not be negative")
	}
	if m.Alpha < 0 || m.Alpha > 1 {
		return nil, fmt.Errorf("alpha must be in [0, 1]")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	y := in.Values
	n := len(y)

	x := y
	var idx []float64
	multiplicative := false
	if m.seasonal() {
		multiplicative = true
		for _, v := range y {
			if v <= 0 {
				multiplicative = false
				break
			}
		}
		idx = seasonalIndices(y, m.Season, multiplicative)
		x = make([]float64, n)
		for t, v := range y {
			if multiplicative {
				x[t] = v / idx[t%m.Season]
			} else {
				x[t] = v - idx[t%m.Season]
			}
		}
	}

	alpha := m.Alpha
	if alpha == 0 {
		best := math.Inf(1)
		for a := 0.05; a <= 1.0001; a += 0.05 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, sse := sesLevel(x, a); sse < best {
				best, alpha = sse, a
			}
		}
	}
	level, _ := sesLevel(x, alpha)
	slope := olsSlope(x)

	out := make([]float64, horizon)
	decay := math.Pow(1-alpha, float64(n))
	for h := 1; h <= horizon; h++ {
		v := level + slope/2*(float64(h-1)+1/alpha-decay/alpha)
		if m.seasonal() {
			pos := (n + h - 1) % m.Season
			if multiplicative {
				v *= idx[pos]
			} else {
				v += idx[pos]
			}
		}
		if v < 0 {
			v = 0
		}
		out[h-1] = v
	}
	return out, finite(out)
}
