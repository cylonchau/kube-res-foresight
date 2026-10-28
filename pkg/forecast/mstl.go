package forecast

import (
	"context"
	"fmt"
)

// MSTL is the multiple seasonal-trend decomposition forecast (Bandara, Hyndman and Bergmeir, 2021) for a load that has a
// daily and a weekly cycle at once. The series is split into a trend and one seasonal component for each period, the
// seasonal parts are set aside, the seasonally adjusted series (trend and remainder) is forecast with Theta, and the
// seasonal parts are added back:
//
//	y(t) = trend(t) + S_day(t mod day) + S_week(t mod week) + remainder(t)
//	forecast(T+h) = Theta(y - S_day - S_week)(h) + S_day((T+h) mod day) + S_week((T+h) mod week)
//
// It differs from the published method in how a seasonal component is estimated: STL smooths each period with LOESS so
// that its shape may change slowly, here every seasonal component is a fixed shape, the mean of its position over all the
// cycles of the history (taken after the trend and the other seasonal components are removed, and repeated three times so
// the components agree with each other). That is cheaper and needs no tuning, but a shape that changes within the history
// is averaged, not followed. The periods are given in steps (a day is 86400 seconds divided by the step), so that the
// history a model needs is known without the step: two full cycles of the longest period, because a seasonal position
// seen once cannot be told from the noise. The forecast is floored at zero.
type MSTL struct {
	Daily  int // steps in a day; the shorter period
	Weekly int // steps in a week; 0 = a daily season only
}

func (MSTL) ID() string { return "mstl" }

func (m MSTL) periods() []int {
	if m.Weekly > 0 {
		return []int{m.Daily, m.Weekly}
	}
	return []int{m.Daily}
}

// MinSamples is two full cycles of the longest period.
func (m MSTL) MinSamples() int {
	longest := m.Daily
	if m.Weekly > longest {
		longest = m.Weekly
	}
	return 2 * longest
}

func (m MSTL) validate() error {
	if m.Daily < 2 {
		return fmt.Errorf("daily_season_steps must be at least 2")
	}
	if m.Weekly != 0 && m.Weekly <= m.Daily {
		return fmt.Errorf("weekly_season_steps must be 0 or longer than daily_season_steps")
	}
	return nil
}

// mstlPasses is how many times the seasonal components are re-estimated against each other and the trend.
const mstlPasses = 3

// centredAverage is the moving average of x over a window of w steps centred on each point; near the ends the window is cut
// to the points that exist, so the trend does not drop to zero at the edges.
func centredAverage(x []float64, w int) []float64 {
	n := len(x)
	prefix := make([]float64, n+1)
	for i, v := range x {
		prefix[i+1] = prefix[i] + v
	}
	half := w / 2
	out := make([]float64, n)
	for t := range x {
		lo, hi := t-half, t+half
		if lo < 0 {
			lo = 0
		}
		if hi > n-1 {
			hi = n - 1
		}
		out[t] = (prefix[hi+1] - prefix[lo]) / float64(hi-lo+1)
	}
	return out
}

// decompose returns, for each period, its seasonal shape (the component at each position, mean zero).
func (m MSTL) decompose(y []float64) [][]float64 {
	periods := m.periods()
	n := len(y)
	shapes := make([][]float64, len(periods))
	for i, p := range periods {
		shapes[i] = make([]float64, p)
	}
	trend := centredAverage(y, periods[len(periods)-1])
	rest := make([]float64, n)
	for pass := 0; pass < mstlPasses; pass++ {
		for i, p := range periods {
			sum := make([]float64, p)
			count := make([]int, p)
			for t := 0; t < n; t++ {
				r := y[t] - trend[t]
				for j, q := range periods {
					if j != i {
						r -= shapes[j][t%q]
					}
				}
				sum[t%p] += r
				count[t%p]++
			}
			var mean float64
			for pos := range sum {
				if count[pos] > 0 {
					sum[pos] /= float64(count[pos])
				}
				mean += sum[pos]
			}
			mean /= float64(p)
			for pos := range sum {
				shapes[i][pos] = sum[pos] - mean
			}
		}
		// the trend of what is left once the seasonal parts are out, smoothed over the longest period
		for t := 0; t < n; t++ {
			v := y[t]
			for j, q := range periods {
				v -= shapes[j][t%q]
			}
			rest[t] = v
		}
		trend = centredAverage(rest, periods[len(periods)-1])
	}
	return shapes
}

func (m MSTL) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	y := in.Values
	n := len(y)
	periods := m.periods()
	shapes := m.decompose(y)

	adjusted := make([]float64, n)
	for t, v := range y {
		for j, q := range periods {
			v -= shapes[j][t%q]
		}
		adjusted[t] = v
	}
	base, err := Theta{}.Forecast(ctx, Input{Values: adjusted, End: in.End, Step: in.Step}, horizon)
	if err != nil {
		return nil, err
	}
	out := make([]float64, horizon)
	for h := 1; h <= horizon; h++ {
		v := base[h-1]
		for j, q := range periods {
			v += shapes[j][(n+h-1)%q]
		}
		if v < 0 {
			v = 0
		}
		out[h-1] = v
	}
	return out, finite(out)
}
