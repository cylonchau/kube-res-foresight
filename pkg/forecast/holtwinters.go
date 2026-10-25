package forecast

import (
	"context"
	"fmt"
	"math"
)

// HoltWinters is additive exponential smoothing (the ETS family): level, optional damped trend and optional
// additive seasonality. A zero smoothing parameter means "choose from a small grid by in-sample one-step error".
type HoltWinters struct {
	Season int // steps per season; 0 or 1 means non-seasonal
	Trend  bool
	Damped bool
	Alpha  float64
	Beta   float64
	Gamma  float64
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
	return nil
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
			st.season[i] = y[i] - first
		}
		return st
	}
	st.level = y[0]
	if m.Trend {
		st.trend = (y[3] - y[0]) / 3
	}
	return st
}

// run filters the series and returns the final state plus the sum of squared one-step errors.
func (m HoltWinters) run(y []float64, p hwParams) (hwState, float64) {
	st := m.init(y)
	phi := 1.0
	if m.Damped {
		phi = dampingPhi
	}
	start := 0
	if m.seasonal() {
		start = m.Season // the first season initializes the components
	}
	var sse float64
	for t := start; t < len(y); t++ {
		var seas float64
		if m.seasonal() {
			seas = st.season[t%m.Season]
		}
		pred := st.level + phi*st.trend + seas
		err := y[t] - pred
		sse += err * err
		prevLevel := st.level
		st.level = p.alpha*(y[t]-seas) + (1-p.alpha)*(prevLevel+phi*st.trend)
		if m.Trend {
			st.trend = p.beta*(st.level-prevLevel) + (1-p.beta)*phi*st.trend
		}
		if m.seasonal() {
			st.season[t%m.Season] = p.gamma*(y[t]-st.level) + (1-p.gamma)*seas
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
	phi := 1.0
	if m.Damped {
		phi = dampingPhi
	}
	n := len(y)
	out := make([]float64, horizon)
	damp := 0.0
	powPhi := 1.0
	for h := 1; h <= horizon; h++ {
		powPhi *= phi
		damp += powPhi
		v := bestState.level + damp*bestState.trend
		if m.seasonal() {
			v += bestState.season[(n+h-1)%m.Season]
		}
		out[h-1] = v
	}
	return out, finite(out)
}
