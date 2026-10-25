package forecast

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// featureSpec describes the lag/calendar features shared by the regression models. Only values strictly
// before the target step are used: lag k of step t is y[t-k], k >= 1. Calendar features depend on the
// timestamp alone, so they are known for any future step.
type featureSpec struct {
	Lags     int
	Calendar bool
	// CovLags is how many past values of each covariate become features; zero means none. cov holds the series
	// (same grid as the target) and covNames their sorted names, which fix the column order.
	CovLags  int
	cov      map[string][]float64
	covNames []string
}

// withCovariates returns the spec that also uses the covariates of in. Covariates are ignored when CovLags is zero.
func (f featureSpec) withCovariates(in Input) (featureSpec, error) {
	if f.CovLags <= 0 || len(in.Covariates) == 0 {
		return f, nil
	}
	names := make([]string, 0, len(in.Covariates))
	for name, series := range in.Covariates {
		if len(series) != len(in.Values) {
			return f, fmt.Errorf("covariate %q has %d values, the target has %d", name, len(series), len(in.Values))
		}
		for _, v := range series {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return f, fmt.Errorf("covariate %q contains NaN/Inf", name)
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	f.cov, f.covNames = in.Covariates, names
	return f, nil
}

func (f featureSpec) width() int {
	w := f.Lags + len(f.covNames)*f.CovLags
	if f.Calendar {
		w += 4
	}
	return w
}

// calendarFeatures returns sin/cos of the position in the UTC day and in the week.
func calendarFeatures(t time.Time) [4]float64 {
	t = t.UTC()
	sec := float64(t.Hour()*3600 + t.Minute()*60 + t.Second())
	day := sec / 86400
	week := (float64(t.Weekday()) + day) / 7
	return [4]float64{math.Sin(2 * math.Pi * day), math.Cos(2 * math.Pi * day), math.Sin(2 * math.Pi * week), math.Cos(2 * math.Pi * week)}
}

// row builds the feature vector of the step at time ts given the values before it (oldest first).
func (f featureSpec) row(prior []float64, ts time.Time, dst []float64) []float64 {
	dst = dst[:0]
	n := len(prior)
	for k := 1; k <= f.Lags; k++ {
		dst = append(dst, prior[n-k])
	}
	// Covariate lag k of the step after n observations is the covariate at index n-k. Beyond the observed history
	// (a step being forecast) the last observed value is carried forward: a covariate is never read from the future.
	for _, name := range f.covNames {
		series := f.cov[name]
		for k := 1; k <= f.CovLags; k++ {
			idx := n - k
			if idx > len(series)-1 {
				idx = len(series) - 1
			}
			if idx < 0 {
				idx = 0
			}
			dst = append(dst, series[idx])
		}
	}
	if f.Calendar {
		c := calendarFeatures(ts)
		dst = append(dst, c[:]...)
	}
	return dst
}

// training builds the design matrix for every step that has Lags values before it, using at most the most
// recent maxRows rows (0 = all).
func (f featureSpec) training(in Input, maxRows int) (x [][]float64, y []float64) {
	first := f.Lags
	if maxRows > 0 && len(in.Values)-first > maxRows {
		first = len(in.Values) - maxRows
	}
	for t := first; t < len(in.Values); t++ {
		x = append(x, f.row(in.Values[:t], in.TimeAt(t), make([]float64, 0, f.width())))
		y = append(y, in.Values[t])
	}
	return x, y
}

func (f featureSpec) validate() error {
	if f.Lags < 1 {
		return fmt.Errorf("lags must be at least 1")
	}
	return nil
}

// solveSPD solves A x = b for a symmetric positive-definite A by Cholesky decomposition.
func solveSPD(a [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := 0; j <= i; j++ {
			sum := a[i][j]
			for k := 0; k < j; k++ {
				sum -= l[i][k] * l[j][k]
			}
			if i == j {
				if sum <= 1e-12 {
					return nil, fmt.Errorf("normal matrix is not positive definite; increase l2_lambda")
				}
				l[i][i] = math.Sqrt(sum)
			} else {
				l[i][j] = sum / l[j][j]
			}
		}
	}
	z := make([]float64, n)
	for i := 0; i < n; i++ {
		sum := b[i]
		for k := 0; k < i; k++ {
			sum -= l[i][k] * z[k]
		}
		z[i] = sum / l[i][i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		sum := z[i]
		for k := i + 1; k < n; k++ {
			sum -= l[k][i] * x[k]
		}
		x[i] = sum / l[i][i]
	}
	return x, nil
}

// quantile returns the q-quantile (0..1) of values by linear interpolation; values is reordered.
func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	pos := q * float64(len(values)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return values[lo]
	}
	frac := pos - float64(lo)
	return values[lo]*(1-frac) + values[hi]*frac
}
