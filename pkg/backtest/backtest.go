// Package backtest evaluates forecasting models with a rolling origin: at every cutoff the model sees only
// the history before it and is scored on the steps after it, so no future value can reach the model.
package backtest

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
)

// Config selects the rolling origins and scoring level.
type Config struct {
	HorizonSteps int     // steps predicted from each cutoff
	Cutoffs      int     // number of rolling origins (default 5)
	Stride       int     // steps between origins (default HorizonSteps)
	MinTrain     int     // minimum history before a cutoff; raised to the model's own minimum
	Alpha        float64 // quantile level for pinball loss and coverage (default 0.5)
}

func (c Config) withDefaults() Config {
	if c.Cutoffs <= 0 {
		c.Cutoffs = 5
	}
	if c.Stride <= 0 {
		c.Stride = c.HorizonSteps
	}
	if c.Alpha <= 0 || c.Alpha >= 1 {
		c.Alpha = 0.5
	}
	return c
}

// Metrics are scores over every evaluated point. All values are finite.
type Metrics struct {
	Points    int     `json:"points"`
	Cutoffs   int     `json:"cutoffs"`
	MAE       float64 `json:"mae"`
	RMSE      float64 `json:"rmse"`
	WAPE      float64 `json:"wape"`       // sum|error| / sum|actual|; equals MAE when every actual is zero
	Pinball   float64 `json:"pinball"`    // mean pinball loss at Alpha
	PeakUnder float64 `json:"peak_under"` // mean over cutoffs of the relative shortfall of the predicted peak
	Coverage  float64 `json:"coverage"`   // share of actuals at or below the forecast
	Alpha     float64 `json:"alpha"`
}

// Result is the outcome for one model. Skipped explains why no score exists; it is never an error.
type Result struct {
	Metrics
	Skipped string `json:"skipped,omitempty"`
}

// Run scores model on series with a rolling origin.
func Run(ctx context.Context, series forecast.Input, model forecast.Model, cfg Config) (Result, error) {
	if cfg.HorizonSteps <= 0 {
		return Result{}, fmt.Errorf("horizon must be positive")
	}
	cfg = cfg.withDefaults()
	minTrain := cfg.MinTrain
	if m := model.MinSamples(); m > minTrain {
		minTrain = m
	}
	n := len(series.Values)
	last := n - cfg.HorizonSteps
	var origins []int
	for k := 0; k < cfg.Cutoffs; k++ {
		c := last - k*cfg.Stride
		if c < minTrain {
			break
		}
		origins = append(origins, c)
	}
	if len(origins) == 0 {
		return Result{Skipped: fmt.Sprintf("insufficient history: need %d training and %d test steps, have %d", minTrain, cfg.HorizonSteps, n)}, nil
	}

	var absErr, sqErr, absActual, pinball float64
	var covered, points int
	var peakUnder float64
	var peakCutoffs int
	for _, c := range origins {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		actual := series.Values[c : c+cfg.HorizonSteps]
		if hasNaN(actual) {
			continue // a gap in the test window cannot be scored
		}
		train := forecast.Input{Values: series.Values[:c:c], Step: series.Step, End: series.TimeAt(c - 1)}
		if len(series.Covariates) > 0 {
			// covariates are cut at the same cutoff as the target: the model sees none of their future
			train.Covariates = make(map[string][]float64, len(series.Covariates))
			for name, values := range series.Covariates {
				train.Covariates[name] = values[:c:c]
			}
		}
		pred, err := model.Forecast(ctx, train, cfg.HorizonSteps)
		if err != nil {
			if errors.Is(err, forecast.ErrInsufficientHistory) {
				return Result{Skipped: err.Error()}, nil
			}
			return Result{}, fmt.Errorf("%s at cutoff %d: %w", model.ID(), c, err)
		}
		var maxActual, maxPred float64 = math.Inf(-1), math.Inf(-1)
		for i, a := range actual {
			e := a - pred[i]
			absErr += math.Abs(e)
			sqErr += e * e
			absActual += math.Abs(a)
			if e >= 0 {
				pinball += cfg.Alpha * e
			} else {
				pinball += (cfg.Alpha - 1) * e
			}
			if a <= pred[i] {
				covered++
			}
			maxActual = math.Max(maxActual, a)
			maxPred = math.Max(maxPred, pred[i])
			points++
		}
		if maxActual > 0 {
			peakUnder += math.Max(0, (maxActual-maxPred)/maxActual)
		}
		peakCutoffs++
	}
	if points == 0 {
		return Result{Skipped: "every test window contains gaps"}, nil
	}
	p := float64(points)
	m := Metrics{Points: points, Cutoffs: peakCutoffs, MAE: absErr / p, RMSE: math.Sqrt(sqErr / p), Pinball: pinball / p,
		PeakUnder: peakUnder / float64(peakCutoffs), Coverage: float64(covered) / p, Alpha: cfg.Alpha}
	if absActual > 0 {
		m.WAPE = absErr / absActual
	} else {
		m.WAPE = m.MAE
	}
	return Result{Metrics: m}, nil
}

func hasNaN(v []float64) bool {
	for _, x := range v {
		if math.IsNaN(x) {
			return true
		}
	}
	return false
}

// Policy decides whether a candidate may replace the baseline. The zero value is the strictest neutral rule:
// strictly lower WAPE and no worse peak underestimation. Thresholds are administrator policy, not defaults
// tuned to a workload.
type Policy struct {
	MinPoints      int     // fewest evaluated points for any decision
	MinImprovement float64 // required relative WAPE reduction, 0.05 = 5%
	PeakTolerance  float64 // allowed relative worsening of peak underestimation
}

// Verdict is a promotion decision with its reason.
type Verdict struct {
	Promoted bool   `json:"promoted"`
	Reason   string `json:"reason"`
}

// Evaluate compares a candidate with the baseline on the same cutoffs. A model that does not beat the
// baseline is not promoted, however complex it is.
func Evaluate(baseline, candidate Result, p Policy) Verdict {
	switch {
	case candidate.Skipped != "":
		return Verdict{Reason: "candidate skipped: " + candidate.Skipped}
	case baseline.Skipped != "":
		return Verdict{Reason: "baseline skipped: " + baseline.Skipped}
	case candidate.Points < p.MinPoints || baseline.Points < p.MinPoints:
		return Verdict{Reason: fmt.Sprintf("only %d evaluated points, policy needs %d", candidate.Points, p.MinPoints)}
	}
	if !(candidate.WAPE < baseline.WAPE*(1-p.MinImprovement)) {
		return Verdict{Reason: fmt.Sprintf("WAPE %.4f does not beat baseline %.4f by %.1f%%", candidate.WAPE, baseline.WAPE, p.MinImprovement*100)}
	}
	if candidate.PeakUnder > baseline.PeakUnder*(1+p.PeakTolerance)+1e-12 {
		return Verdict{Reason: fmt.Sprintf("peak underestimation %.4f is worse than baseline %.4f", candidate.PeakUnder, baseline.PeakUnder)}
	}
	return Verdict{Promoted: true, Reason: fmt.Sprintf("WAPE %.4f beats baseline %.4f without worse peak underestimation", candidate.WAPE, baseline.WAPE)}
}
