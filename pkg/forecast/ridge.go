package forecast

import (
	"context"
	"fmt"
	"math"
)

// RidgeAR is a linear autoregression with optional calendar features, fitted by ridge regression. Features and target are
// standardized, so L2Lambda is scale free. By default the forecast is recursive: each predicted step becomes the lag of the
// next, so the error of a step is fed into every step after it and a long horizon drifts toward the mean. With Direct a
// separate model is fitted for the step ahead itself (see directSteps), which sees only observed values and so does not
// compound.
type RidgeAR struct {
	Lags         int
	Calendar     bool
	L2Lambda     float64
	MaxTrainRows int // 0 = all history
	CovLags      int // past values of each covariate used as features; 0 = ignore covariates
	// Direct fits one model per anchor step ahead instead of rolling one model forward.
	Direct bool
	// DirectAnchors is how many steps ahead get their own model (the steps between are interpolated); 0 = DefaultDirectAnchors.
	DirectAnchors int
}

func (RidgeAR) ID() string { return "ridge_ar" }

func (m RidgeAR) spec() featureSpec {
	return featureSpec{Lags: m.Lags, Calendar: m.Calendar, CovLags: m.CovLags}
}

// MinSamples needs the lags plus enough rows to fit every coefficient.
func (m RidgeAR) MinSamples() int { return m.Lags + 2*m.spec().width() + 1 }

// MinSamplesFor adds the steps a direct model looks ahead: the model of step h is trained on targets h-1 steps after the
// origin of its features.
func (m RidgeAR) MinSamplesFor(horizon int) int {
	min := m.MinSamples()
	if m.Direct && horizon > 1 {
		min += horizon - 1
	}
	return min
}

// ridgeFit is a fitted ridge regression on standardized features.
type ridgeFit struct {
	coef, mean, std []float64
	yMean           float64
}

func (r *ridgeFit) predict(feat []float64) float64 {
	pred := r.yMean
	for j := range feat {
		pred += r.coef[j] * (feat[j] - r.mean[j]) / r.std[j]
	}
	return pred
}

// fitRidge fits the standardized ridge regression of y on x (width w features).
func fitRidge(ctx context.Context, x [][]float64, y []float64, w int, lambda float64) (*ridgeFit, error) {
	rows := float64(len(x))
	mean := make([]float64, w)
	std := make([]float64, w)
	for _, row := range x {
		for j, v := range row {
			mean[j] += v
		}
	}
	for j := range mean {
		mean[j] /= rows
	}
	for _, row := range x {
		for j, v := range row {
			std[j] += (v - mean[j]) * (v - mean[j])
		}
	}
	for j := range std {
		std[j] = math.Sqrt(std[j] / rows)
		if std[j] < 1e-12 {
			std[j] = 1 // constant feature: standardized to zero
		}
	}
	var yMean float64
	for _, v := range y {
		yMean += v
	}
	yMean /= rows

	gram := make([][]float64, w)
	for i := range gram {
		gram[i] = make([]float64, w)
	}
	rhs := make([]float64, w)
	z := make([]float64, w)
	for r, row := range x {
		if r%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for j := range z {
			z[j] = (row[j] - mean[j]) / std[j]
		}
		for i := 0; i < w; i++ {
			rhs[i] += z[i] * (y[r] - yMean)
			for j := 0; j <= i; j++ {
				gram[i][j] += z[i] * z[j]
			}
		}
	}
	for i := 0; i < w; i++ {
		for j := 0; j < i; j++ {
			gram[j][i] = gram[i][j]
		}
		gram[i][i] += lambda*rows + 1e-9
	}
	coef, err := solveSPD(gram, rhs)
	if err != nil {
		return nil, err
	}
	return &ridgeFit{coef: coef, mean: mean, std: std, yMean: yMean}, nil
}

func (m RidgeAR) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.spec().validate(); err != nil {
		return nil, err
	}
	if m.L2Lambda < 0 {
		return nil, fmt.Errorf("l2_lambda must not be negative")
	}
	if m.Direct && m.DirectAnchors != 0 && m.DirectAnchors < 2 {
		return nil, fmt.Errorf("direct_anchors must be at least 2")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	if m.Direct && len(in.Values) < m.MinSamplesFor(horizon) {
		return nil, fmt.Errorf("%w: ridge_ar direct needs %d samples for a horizon of %d, got %d", ErrInsufficientHistory, m.MinSamplesFor(horizon), horizon, len(in.Values))
	}
	spec, err := m.spec().withCovariates(in)
	if err != nil {
		return nil, err
	}
	w := spec.width()

	if m.Direct {
		steps := directSteps(horizon, m.DirectAnchors)
		vals := make([]float64, len(steps))
		feat := make([]float64, 0, w)
		for i, h := range steps {
			x, y := spec.trainingDirect(in, m.MaxTrainRows, h)
			fit, err := fitRidge(ctx, x, y, w, m.L2Lambda)
			if err != nil {
				return nil, err
			}
			feat = spec.row(in.Values, in.TimeAt(len(in.Values)+h-1), feat)
			vals[i] = fit.predict(feat)
		}
		out := interpolateSteps(steps, vals, horizon)
		return out, finite(out)
	}

	x, y := spec.training(in, m.MaxTrainRows)
	fit, err := fitRidge(ctx, x, y, w, m.L2Lambda)
	if err != nil {
		return nil, err
	}
	history := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	feat := make([]float64, 0, w)
	for h := 0; h < horizon; h++ {
		ts := in.TimeAt(len(in.Values) + h)
		feat = spec.row(history, ts, feat)
		pred := fit.predict(feat)
		out[h] = pred
		history = append(history, pred)
	}
	return out, finite(out)
}
