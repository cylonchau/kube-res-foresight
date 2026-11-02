// Package backtest evaluates forecasting models with a rolling origin: at every cutoff the model sees only
// the history before it and is scored on the steps after it, so no future value can reach the model.
package backtest

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
)

// Config selects the rolling origins and scoring level.
type Config struct {
	HorizonSteps int     // steps predicted from each cutoff
	Cutoffs      int     // number of rolling origins (default 5)
	Stride       int     // steps between origins (default HorizonSteps)
	MinTrain     int     // minimum history before a cutoff; raised to the model's own minimum
	Alpha        float64 // quantile level for pinball loss and coverage (default 0.5)

	// Spread spaces the origins evenly over the whole history (at least one horizon apart) instead of packing them
	// into the last Cutoffs*Stride steps, so the score covers different hours of the day and not just the latest few.
	Spread bool
	// SeasonSteps, when positive, keeps the spaced stride from being an exact multiple of the season, so the origins do
	// not all land on the same time of day.
	SeasonSteps int
	// Origins, when set, are the exact cutoff indexes to use (see Origins); candidate and baseline are then scored on the
	// same windows. Every model must have at least its own MinSamples before each of them.
	Origins []int
	// UnderWeight is how many times an underestimate costs an overestimate in RiskWAPE (default DefaultUnderWeight).
	UnderWeight float64
}

// DefaultUnderWeight is the cost of a unit of underestimate relative to a unit of overestimate in RiskWAPE: a resource
// forecast that is too low starves the workload, one that is too high only wastes some capacity.
const DefaultUnderWeight = 3.0

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
	if c.UnderWeight < 1 {
		c.UnderWeight = DefaultUnderWeight
	}
	return c
}

// Origins returns the cutoff indexes (newest first) for a series of n points: the most recent one leaves a full horizon
// to score, and each earlier one has at least minTrain points before it. Fewer than Cutoffs are returned when the
// history does not allow more. Two models that share the same minTrain, n and config get the same origins, which is
// what lets a candidate and a baseline be compared on identical windows.
func Origins(n int, cfg Config, minTrain int) []int {
	cfg = cfg.withDefaults()
	last := n - cfg.HorizonSteps
	if cfg.HorizonSteps <= 0 || last < minTrain || last < 1 {
		return nil
	}
	stride := cfg.Stride
	if cfg.Spread && cfg.Cutoffs > 1 {
		stride = (last - minTrain) / (cfg.Cutoffs - 1)
		if stride < cfg.HorizonSteps {
			stride = cfg.HorizonSteps
		}
		if cfg.SeasonSteps > 1 && stride > cfg.HorizonSteps && stride%cfg.SeasonSteps == 0 {
			stride--
		}
	}
	var out []int
	for k := 0; k < cfg.Cutoffs; k++ {
		c := last - k*stride
		if c < minTrain {
			break
		}
		out = append(out, c)
	}
	return out
}

// RiskWAPE rebuilds the underestimate-weighted error from a score's WAPE, MAE and bias (the mean of prediction minus
// actual): the share of the absolute error that came from underestimating is (MAE-bias)/(2*MAE). It is how runs stored
// before RiskWAPE existed, and the evaluations of real outcomes, get the same number a backtest computes directly.
func RiskWAPE(wape, mae, bias, underWeight float64) float64 {
	if underWeight < 1 {
		underWeight = DefaultUnderWeight
	}
	if mae <= 0 {
		return wape
	}
	under := (mae - bias) / (2 * mae)
	under = math.Max(0, math.Min(1, under))
	return wape * (1 + (underWeight-1)*under)
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
	// Bias is the mean of prediction minus actual: positive means the forecast ran high.
	Bias float64 `json:"bias"`
	// RiskWAPE is the WAPE with underestimates costing UnderWeight times as much as overestimates. UnderWeight is 0 on
	// results stored before this metric existed, which tells a reader not to compare them on it.
	RiskWAPE    float64 `json:"risk_wape"`
	UnderWeight float64 `json:"under_weight,omitempty"`
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
	if m := forecast.MinSamplesFor(model, cfg.HorizonSteps); m > minTrain {
		minTrain = m
	}
	n := len(series.Values)
	origins := cfg.Origins
	for _, c := range origins {
		if c < forecast.MinSamplesFor(model, cfg.HorizonSteps) || c+cfg.HorizonSteps > n {
			return Result{Skipped: fmt.Sprintf("insufficient history: %s needs %d training steps before every shared origin, origin %d has %d", model.ID(), forecast.MinSamplesFor(model, cfg.HorizonSteps), c, c)}, nil
		}
	}
	if len(origins) == 0 {
		origins = Origins(n, cfg, minTrain)
	}
	if len(origins) == 0 {
		return Result{Skipped: fmt.Sprintf("insufficient history: need %d training and %d test steps, have %d", minTrain, cfg.HorizonSteps, n)}, nil
	}

	var absErr, sqErr, absActual, pinball, signed, riskErr float64
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
			signed -= e
			if e > 0 {
				riskErr += cfg.UnderWeight * e // the forecast was below the actual
			} else {
				riskErr -= e
			}
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
		PeakUnder: peakUnder / float64(peakCutoffs), Coverage: float64(covered) / p, Alpha: cfg.Alpha, Bias: signed / p, UnderWeight: cfg.UnderWeight}
	if absActual > 0 {
		m.WAPE = absErr / absActual
		m.RiskWAPE = riskErr / absActual
	} else {
		m.WAPE = m.MAE
		m.RiskWAPE = riskErr / p
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
// strictly lower WAPE, no worse peak underestimation and no worse underestimate-weighted error (RiskWAPE). Thresholds are administrator policy, not defaults
// tuned to a workload.
type Policy struct {
	MinPoints      int     // fewest evaluated points for any decision
	MinImprovement float64 // required relative WAPE reduction, 0.05 = 5%
	PeakTolerance  float64 // allowed relative worsening of peak underestimation and of RiskWAPE
}

// Verdict is a promotion decision with its reason.
type Verdict struct {
	Promoted bool   `json:"promoted"`
	Reason   string `json:"reason"`
}

// QuantileCoverageSlack is how far below its level the coverage of a quantile forecast may fall and still count as calibrated.
const QuantileCoverageSlack = 0.1

// evaluateQuantile is the rule for a candidate that forecasts a quantile (a P90 line) rather than the middle: it is not
// judged on WAPE, which a line that deliberately runs high would always lose, but on what it is for. Both results were scored
// at the same level, so the pinball loss of the candidate has to beat the baseline's, the line has to cover about its level of
// the actuals, and it must not underestimate the peak more than the baseline does.
func evaluateQuantile(baseline, candidate Result, p Policy) Verdict {
	switch {
	case candidate.Coverage < candidate.Alpha-QuantileCoverageSlack:
		return Verdict{Reason: fmt.Sprintf("the %.0f%% line covers only %.1f%% of the actuals (at least %.0f%% needed)", candidate.Alpha*100, candidate.Coverage*100, (candidate.Alpha-QuantileCoverageSlack)*100)}
	case !(candidate.Pinball < baseline.Pinball*(1-p.MinImprovement)):
		return Verdict{Reason: fmt.Sprintf("pinball loss %.4f at %.0f%% does not beat baseline %.4f by %.1f%%", candidate.Pinball, candidate.Alpha*100, baseline.Pinball, p.MinImprovement*100)}
	case candidate.PeakUnder > baseline.PeakUnder*(1+p.PeakTolerance)+1e-12:
		return Verdict{Reason: fmt.Sprintf("peak underestimation %.4f is worse than baseline %.4f", candidate.PeakUnder, baseline.PeakUnder)}
	}
	return Verdict{Promoted: true, Reason: fmt.Sprintf("the %.0f%% line covers %.1f%% and its pinball loss %.4f beats baseline %.4f", candidate.Alpha*100, candidate.Coverage*100, candidate.Pinball, baseline.Pinball)}
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
	case candidate.Points != baseline.Points || candidate.Cutoffs != baseline.Cutoffs:
		// a verdict on different windows would compare the models and the data at once
		return Verdict{Reason: fmt.Sprintf("not comparable: candidate scored %d points over %d cutoffs, baseline %d over %d", candidate.Points, candidate.Cutoffs, baseline.Points, baseline.Cutoffs)}
	}
	if candidate.Alpha > 0 && candidate.Alpha != 0.5 && candidate.Alpha == baseline.Alpha {
		return evaluateQuantile(baseline, candidate, p)
	}
	if !(candidate.WAPE < baseline.WAPE*(1-p.MinImprovement)) {
		return Verdict{Reason: fmt.Sprintf("WAPE %.4f does not beat baseline %.4f by %.1f%%", candidate.WAPE, baseline.WAPE, p.MinImprovement*100)}
	}
	if candidate.PeakUnder > baseline.PeakUnder*(1+p.PeakTolerance)+1e-12 {
		return Verdict{Reason: fmt.Sprintf("peak underestimation %.4f is worse than baseline %.4f", candidate.PeakUnder, baseline.PeakUnder)}
	}
	// Underestimating costs more than overestimating, so a candidate that wins on WAPE by running low is not promoted.
	// Results stored before RiskWAPE existed (UnderWeight 0) cannot be compared on it and are judged on the rules above.
	if candidate.UnderWeight > 0 && baseline.UnderWeight > 0 && candidate.RiskWAPE > baseline.RiskWAPE*(1+p.PeakTolerance)+1e-12 {
		return Verdict{Reason: fmt.Sprintf("underestimate-weighted error %.4f is worse than baseline %.4f (WAPE %.4f does beat %.4f, but it runs low)", candidate.RiskWAPE, baseline.RiskWAPE, candidate.WAPE, baseline.WAPE)}
	}
	return Verdict{Promoted: true, Reason: fmt.Sprintf("WAPE %.4f beats baseline %.4f without worse peak underestimation or underestimate-weighted error", candidate.WAPE, baseline.WAPE)}
}
