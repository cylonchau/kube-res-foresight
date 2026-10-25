// Package forecast holds the pure-Go forecasting algorithms. Every model is stateless: one call fits on the
// history it is given and predicts the next horizon steps, so a backtest can call it once per cutoff and a
// model can never see data after the cutoff it is handed.
package forecast

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// ImplVersion is the version of the Go implementations; it is stored with every prediction.
const ImplVersion = "1"

// Input is the history handed to a model. Values are on a regular grid of Step, oldest first, with no gaps:
// Values[len-1] is at End, Values[i] at End - (len-1-i)*Step. Models predict End+Step, End+2*Step, ...
type Input struct {
	Values []float64
	End    time.Time
	Step   time.Duration
	// Covariates are other metrics of the same workload on the same grid as Values (same length, finite, oldest
	// first). Only algorithms that declare support use them, and only through their values strictly before the
	// step being predicted, so a covariate can never carry information from the future.
	Covariates map[string][]float64
}

// TimeAt returns the timestamp of history index i (negative or past the end are valid).
func (in Input) TimeAt(i int) time.Time {
	return in.End.Add(time.Duration(i-(len(in.Values)-1)) * in.Step)
}

// Model is one forecasting algorithm configured with validated parameters.
type Model interface {
	// ID is the algorithm identifier from the registry.
	ID() string
	// MinSamples is the shortest history the model accepts; shorter history is skipped, never padded.
	MinSamples() int
	// Forecast predicts the next horizon steps. The returned slice has exactly horizon finite values.
	Forecast(ctx context.Context, in Input, horizon int) ([]float64, error)
}

// ErrInsufficientHistory is returned (wrapped) when the history is shorter than the model needs.
var ErrInsufficientHistory = errors.New("insufficient history")

func checkInput(m Model, in Input, horizon int) error {
	if horizon <= 0 {
		return fmt.Errorf("horizon must be positive")
	}
	if in.Step <= 0 {
		return fmt.Errorf("step must be positive")
	}
	if len(in.Values) < m.MinSamples() {
		return fmt.Errorf("%w: %s needs %d samples, got %d", ErrInsufficientHistory, m.ID(), m.MinSamples(), len(in.Values))
	}
	for _, v := range in.Values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("history contains NaN/Inf; fill or trim gaps first")
		}
	}
	return nil
}

func finite(out []float64) error {
	for _, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("model produced a non-finite forecast")
		}
	}
	return nil
}

// Naive repeats the last observation.
type Naive struct{}

func (Naive) ID() string      { return "naive" }
func (Naive) MinSamples() int { return 1 }
func (m Naive) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	out := make([]float64, horizon)
	last := in.Values[len(in.Values)-1]
	for i := range out {
		out[i] = last
	}
	return out, nil
}

// SeasonalNaive repeats the value one season earlier. The season is counted in steps, never assumed.
type SeasonalNaive struct{ Season int }

func (SeasonalNaive) ID() string        { return "seasonal_naive" }
func (m SeasonalNaive) MinSamples() int { return m.Season }
func (m SeasonalNaive) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	if m.Season < 2 {
		return nil, fmt.Errorf("season_length_steps must be at least 2")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	out := make([]float64, horizon)
	for h := 0; h < horizon; h++ {
		out[h] = in.Values[n-m.Season+(h%m.Season)]
	}
	return out, nil
}

// EWMA is simple exponential smoothing: a flat forecast at the smoothed level. Span, when positive, replaces Alpha
// with 2/(Span+1). BiasCorrection weights the history by (1-alpha)^age and divides by the sum of the weights
// instead of starting the recursion at the first value, which removes the pull of the first observation on short
// histories.
type EWMA struct {
	Alpha          float64
	Span           int
	BiasCorrection bool
}

func (m EWMA) alpha() float64 {
	if m.Span > 0 {
		return 2 / (float64(m.Span) + 1)
	}
	return m.Alpha
}

func (EWMA) ID() string      { return "ewma" }
func (EWMA) MinSamples() int { return 2 }
func (m EWMA) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	alpha := m.alpha()
	if alpha <= 0 || alpha > 1 {
		return nil, fmt.Errorf("alpha must be in (0, 1]")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	var level float64
	if m.BiasCorrection {
		var num, den float64
		weight := 1.0
		for i := len(in.Values) - 1; i >= 0; i-- {
			num += weight * in.Values[i]
			den += weight
			weight *= 1 - alpha
		}
		level = num / den
	} else {
		level = in.Values[0]
		for _, v := range in.Values[1:] {
			level = alpha*v + (1-alpha)*level
		}
	}
	out := make([]float64, horizon)
	for i := range out {
		out[i] = level
	}
	return out, finite(out)
}
