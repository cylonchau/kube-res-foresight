package forecast

import (
	"context"
	"fmt"
	"math"
)

// RidgeAR is a linear autoregression with optional calendar features, fitted by ridge regression and
// forecast recursively. Features and target are standardized, so L2Lambda is scale free.
type RidgeAR struct {
	Lags         int
	Calendar     bool
	L2Lambda     float64
	MaxTrainRows int // 0 = all history
	CovLags      int // past values of each covariate used as features; 0 = ignore covariates
}

func (RidgeAR) ID() string { return "ridge_ar" }

func (m RidgeAR) spec() featureSpec {
	return featureSpec{Lags: m.Lags, Calendar: m.Calendar, CovLags: m.CovLags}
}

// MinSamples needs the lags plus enough rows to fit every coefficient.
func (m RidgeAR) MinSamples() int { return m.Lags + 2*m.spec().width() + 1 }

func (m RidgeAR) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.spec().validate(); err != nil {
		return nil, err
	}
	if m.L2Lambda < 0 {
		return nil, fmt.Errorf("l2_lambda must not be negative")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	spec, err := m.spec().withCovariates(in)
	if err != nil {
		return nil, err
	}
	x, y := spec.training(in, m.MaxTrainRows)
	w := spec.width()
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
		gram[i][i] += m.L2Lambda*rows + 1e-9
	}
	coef, err := solveSPD(gram, rhs)
	if err != nil {
		return nil, err
	}

	history := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	feat := make([]float64, 0, w)
	for h := 0; h < horizon; h++ {
		ts := in.TimeAt(len(in.Values) + h)
		feat = spec.row(history, ts, feat)
		pred := yMean
		for j := range feat {
			pred += coef[j] * (feat[j] - mean[j]) / std[j]
		}
		out[h] = pred
		history = append(history, pred)
	}
	return out, finite(out)
}
