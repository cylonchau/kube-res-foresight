package forecast

import (
	"context"
	"fmt"
	"math"
)

// HoltWinters is exponential smoothing (the ETS family): level, optional damped trend and optional seasonality, additive
// or multiplicative. A zero smoothing parameter means "choose from a small grid by in-sample one-step error".
//
// Additive seasonality adds a fixed amount per step of the season; multiplicative seasonality scales the level, which is
// what a resource that peaks higher when the load is higher needs (a CPU series whose daily swing grows with the level).
// A multiplicative model needs strictly positive history: a series that touches zero is refused, not quietly changed.
type HoltWinters struct {
	Season int // steps per season; 0 or 1 means non-seasonal
	Trend  bool
	Damped bool
	Alpha  float64
	Beta   float64
	Gamma  float64
	// Multiplicative selects multiplicative seasonality (it needs Season > 1).
	Multiplicative bool
	// DampingPhi is the damping factor of the trend when Damped; zero means the default 0.98.
	DampingPhi float64
	// NonNegative floors the forecast at zero: a resource is never negative, and an additive trend can run below it.
	NonNegative bool
}

func (HoltWinters) ID() string { return "holt_winters" }

func (m HoltWinters) seasonal() bool { return m.Season > 1 }

func (m HoltWinters) MinSamples() int {
	if m.seasonal() {
		return 2 * m.Season
	}
	return 4
}

const dampingPhi = 0.98

func (m HoltWinters) phi() float64 {
	if !m.Damped {
		return 1
	}
	if m.DampingPhi > 0 {
		return m.DampingPhi
	}
	return dampingPhi
}

type hwState struct {
	level, trend float64
	season       []float64
}

type hwParams struct{ alpha, beta, gamma float64 }

func (m HoltWinters) validate() error {
	for name, v := range map[string]float64{"alpha": m.Alpha, "beta": m.Beta, "gamma": m.Gamma} {
		if v < 0 || v > 1 {
			return fmt.Errorf("%s must be in [0, 1]", name)
		}
	}
	if m.Season < 0 {
		return fmt.Errorf("season_length_steps must not be negative")
	}
	if m.DampingPhi < 0 || m.DampingPhi > 1 {
		return fmt.Errorf("damping_phi must be in [0, 1]")
	}
	if m.Multiplicative && !m.seasonal() {
		return fmt.Errorf("multiplicative seasonality needs season_length_steps of at least 2")
	}
	return nil
}

// olsSlope is the least-squares slope of y over its index.
func olsSlope(y []float64) float64 {
	n := float64(len(y))
	if n < 2 {
		return 0
	}
	var sx, sy, sxy, sxx float64
	for i, v := range y {
		x := float64(i)
		sx += x
		sy += v
		sxy += x * v
		sxx += x * x
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func (m HoltWinters) init(y []float64) hwState {
	st := hwState{}
	if m.seasonal() {
		s := m.Season
		var first, second float64
		for i := 0; i < s; i++ {
			first += y[i]
			second += y[s+i]
		}
		first /= float64(s)
		second /= float64(s)
		st.level = first
		if m.Trend {
			st.trend = (second - first) / float64(s)
		}
		st.season = make([]float64, s)
		for i := 0; i < s; i++ {
			if m.Multiplicative {
				st.season[i] = y[i] / first
			} else {
				st.season[i] = y[i] - first
			}
		}
		return st
	}
	st.level = y[0]
	if m.Trend {
		// the slope of the first few points, not of two of them: one noisy point no longer sets the starting trend
		k := len(y)
		if k > 10 {
			k = 10
		}
		st.trend = olsSlope(y[:k])
	}
	return st
}

// run filters the series and returns the final state plus the sum of squared one-step errors.
func (m HoltWinters) run(y []float64, p hwParams) (hwState, float64) {
	st := m.init(y)
	phi := m.phi()
	start := 0
	if m.seasonal() {
		start = m.Season // the first season initializes the components
	}
	var sse float64
	for t := start; t < len(y); t++ {
		base := st.level + phi*st.trend
		pred := base
		var seas float64
		if m.seasonal() {
			seas = st.season[t%m.Season]
			if m.Multiplicative {
				pred = base * seas
			} else {
				pred = base + seas
			}
		}
		err := y[t] - pred
		sse += err * err
		prevLevel := st.level
		if m.seasonal() && m.Multiplicative {
			st.level = p.alpha*(y[t]/seas) + (1-p.alpha)*base
		} else {
			st.level = p.alpha*(y[t]-seas) + (1-p.alpha)*base
		}
		if m.Trend {
			st.trend = p.beta*(st.level-prevLevel) + (1-p.beta)*phi*st.trend
		}
		if m.seasonal() {
			if m.Multiplicative {
				if st.level != 0 {
					st.season[t%m.Season] = p.gamma*(y[t]/st.level) + (1-p.gamma)*seas
				}
			} else {
				st.season[t%m.Season] = p.gamma*(y[t]-st.level) + (1-p.gamma)*seas
			}
		}
	}
	return st, sse
}

func (m HoltWinters) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	y := in.Values
	if m.Multiplicative {
		for _, v := range y {
			if v <= 0 {
				return nil, fmt.Errorf("multiplicative seasonality needs strictly positive history, found %v; use additive seasonality for a series that reaches zero", v)
			}
		}
	}
	alphas, betas, gammas := []float64{m.Alpha}, []float64{m.Beta}, []float64{m.Gamma}
	if m.Alpha == 0 {
		alphas = []float64{0.1, 0.3, 0.5, 0.7, 0.9}
	}
	if !m.Trend {
		betas = []float64{0}
	} else if m.Beta == 0 {
		betas = []float64{0.01, 0.1, 0.3}
	}
	if !m.seasonal() {
		gammas = []float64{0}
	} else if m.Gamma == 0 {
		gammas = []float64{0.1, 0.3, 0.5}
	}
	best := math.Inf(1)
	var bestState hwState
	for _, a := range alphas {
		for _, b := range betas {
			for _, g := range gammas {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				st, sse := m.run(y, hwParams{a, b, g})
				if sse < best {
					best, bestState = sse, st
				}
			}
		}
	}
	phi := m.phi()
	n := len(y)
	out := make([]float64, horizon)
	damp := 0.0
	powPhi := 1.0
	for h := 1; h <= horizon; h++ {
		powPhi *= phi
		damp += powPhi
		v := bestState.level + damp*bestState.trend
		if m.seasonal() {
			if m.Multiplicative {
				v *= bestState.season[(n+h-1)%m.Season]
			} else {
				v += bestState.season[(n+h-1)%m.Season]
			}
		}
		if m.NonNegative && v < 0 {
			v = 0
		}
		out[h-1] = v
	}
	return out, finite(out)
}
