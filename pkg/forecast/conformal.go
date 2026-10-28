package forecast

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Quantiled is implemented by a model whose forecast is a quantile of the future rather than its middle: the backtest then
// scores it with the pinball loss and the coverage of that level, and the stored predictions carry the level.
type Quantiled interface {
	// QuantileLevel is the level in (0, 1) and whether the model forecasts a quantile at all.
	QuantileLevel() (float64, bool)
}

// QuantileLevel makes the quantile objective of the gradient-boosted trees visible to the callers above.
func (m GBDT) QuantileLevel() (float64, bool) { return m.Alpha, m.Quantile }

// DefaultCalibrationWindows is how many past windows a Conformal model measures its own errors on.
const DefaultCalibrationWindows = 8

// minCalibrationWindows is the fewest windows a quantile is read from.
const minCalibrationWindows = 3

// Conformal turns the point forecast of any model into a forecast of the Level quantile (split conformal prediction):
// the model is run, as it would have been, from several earlier cut-offs of the very history it is given; the error it
// made at each step ahead (actual minus forecast) is collected over those windows; and the forecast of every step is the
// model's forecast plus the Level quantile of that step's errors. So a Level of 0.9 produces a line the actual stays at
// or below about nine times in ten, calibrated on how this model really did on this workload, whatever the model is.
//
// It only ever uses values before the forecast origin: the calibration windows are cut from the history it is handed.
// The price is time: one extra forecast per window, so it is meant for the cheap models, or for a generous time budget.
// With few windows the quantile is read from the largest error, which is the conservative side.
type Conformal struct {
	Base    Model
	Level   float64 // in (0.5, 1)
	Windows int     // calibration windows; 0 = DefaultCalibrationWindows
}

func (m Conformal) ID() string { return m.Base.ID() }

func (m Conformal) windows() int {
	if m.Windows > 0 {
		return m.Windows
	}
	return DefaultCalibrationWindows
}

// QuantileLevel is the level the forecast covers.
func (m Conformal) QuantileLevel() (float64, bool) { return m.Level, true }

// MinSamples is the base model's and room for the shortest calibration; the horizon-dependent part is in MinSamplesFor.
func (m Conformal) MinSamples() int { return m.Base.MinSamples() + minCalibrationWindows }

// MinSamplesFor adds the calibration: the base model needs its own history before the earliest window, and each window
// needs a horizon of actual values after it.
func (m Conformal) MinSamplesFor(horizon int) int {
	return MinSamplesFor(m.Base, horizon) + minCalibrationWindows*horizon
}

func (m Conformal) validate() error {
	if m.Base == nil {
		return errors.New("conformal needs a base model")
	}
	if m.Level <= 0.5 || m.Level >= 1 {
		return fmt.Errorf("quantile_level must be in (0.5, 1), or 0 for the point forecast")
	}
	if m.Windows < 0 {
		return fmt.Errorf("calibration_windows must not be negative")
	}
	return nil
}

// cut is the history up to (excluding) index c, with its covariates cut at the same place.
func cut(in Input, c int) Input {
	out := Input{Values: in.Values[:c:c], Step: in.Step, End: in.TimeAt(c - 1)}
	if len(in.Covariates) > 0 {
		out.Covariates = make(map[string][]float64, len(in.Covariates))
		for name, series := range in.Covariates {
			out.Covariates[name] = series[:c:c]
		}
	}
	return out
}

// calibrationOrigins are the cut-offs (newest first) the errors are measured at: each leaves a whole horizon of actual
// values, has the base model's minimum before it, and they are spread over the history, at least one horizon apart.
func calibrationOrigins(n, horizon, minBase, windows int) []int {
	last := n - horizon
	if last < minBase || windows < 1 {
		return nil
	}
	stride := horizon
	if windows > 1 {
		if spread := (last - minBase) / (windows - 1); spread > stride {
			stride = spread
		}
	}
	var out []int
	for k := 0; k < windows; k++ {
		c := last - k*stride
		if c < minBase {
			break
		}
		out = append(out, c)
	}
	return out
}

// conformalQuantile is the order statistic ceil((K+1)*level) of the K errors, or the largest when that rank is past the end.
func conformalQuantile(errs []float64, level float64) float64 {
	sorted := append([]float64(nil), errs...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(float64(len(sorted)+1) * level))
	if rank > len(sorted) {
		rank = len(sorted)
	}
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func (m Conformal) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	origins := calibrationOrigins(n, horizon, MinSamplesFor(m.Base, horizon), m.windows())
	if len(origins) < minCalibrationWindows {
		return nil, fmt.Errorf("%w: conformal %s needs %d calibration windows of %d steps after %d steps of history, the history of %d allows %d",
			ErrInsufficientHistory, m.Base.ID(), minCalibrationWindows, horizon, MinSamplesFor(m.Base, horizon), n, len(origins))
	}

	// errs[j] are the errors of the step j+1 ahead over the windows
	errs := make([][]float64, horizon)
	for _, c := range origins {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pred, err := m.Base.Forecast(ctx, cut(in, c), horizon)
		if err != nil {
			return nil, fmt.Errorf("calibration window at %d: %w", c, err)
		}
		for j := 0; j < horizon; j++ {
			errs[j] = append(errs[j], in.Values[c+j]-pred[j])
		}
	}
	point, err := m.Base.Forecast(ctx, in, horizon)
	if err != nil {
		return nil, err
	}
	out := make([]float64, horizon)
	for j := range out {
		out[j] = math.Max(0, point[j]+conformalQuantile(errs[j], m.Level)) // a resource is never negative
	}
	return out, finite(out)
}
