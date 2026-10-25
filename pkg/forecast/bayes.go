package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// NaiveBayes is a categorical naive Bayes classifier turned into a forecaster: the target is cut into Bins
// quantile classes, each feature (lags, covariate lags, calendar position) is cut into the same number of quantile
// bins, and the model learns P(class) and P(feature bin | class) with Laplace smoothing. The forecast of a step is
// the posterior-weighted mean of the classes ("expectation") or the mean of the most probable class ("mode"), and
// is fed back as the next lag. Its resolution is the bin width, which is the price of the classification view.
type NaiveBayes struct {
	Lags         int
	Bins         int
	CovLags      int
	Calendar     bool
	Output       string // "expectation" or "mode"
	MaxTrainRows int
}

func (NaiveBayes) ID() string { return "naive_bayes" }

func (m NaiveBayes) spec() featureSpec {
	return featureSpec{Lags: m.Lags, Calendar: m.Calendar, CovLags: m.CovLags}
}

func (m NaiveBayes) MinSamples() int { return m.Lags + 4*m.Bins + 1 }

func (m NaiveBayes) validate() error {
	if err := m.spec().validate(); err != nil {
		return err
	}
	if m.Bins < 2 || m.Bins > 64 {
		return fmt.Errorf("bins must be between 2 and 64")
	}
	if m.Output != "" && m.Output != "expectation" && m.Output != "mode" {
		return fmt.Errorf("output must be expectation or mode")
	}
	return nil
}

// quantileEdges returns up to bins-1 strictly increasing cut points of the values.
func quantileEdges(values []float64, bins int) []float64 {
	col := append([]float64(nil), values...)
	sort.Float64s(col)
	// few distinct values (a count, a replica number): every value is its own class
	distinct := col[:0:0]
	for _, v := range col {
		if len(distinct) == 0 || v > distinct[len(distinct)-1] {
			distinct = append(distinct, v)
		}
	}
	if len(distinct) <= bins {
		return distinct[:len(distinct)-1]
	}
	var edges []float64
	for b := 1; b < bins; b++ {
		v := col[b*(len(col)-1)/bins]
		if len(edges) == 0 || v > edges[len(edges)-1] {
			edges = append(edges, v)
		}
	}
	for len(edges) > 0 && edges[len(edges)-1] >= col[len(col)-1] {
		edges = edges[:len(edges)-1]
	}
	return edges
}

func binOf(edges []float64, v float64) int { return sort.SearchFloat64s(edges, v) }

func (m NaiveBayes) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	spec, err := m.spec().withCovariates(in)
	if err != nil {
		return nil, err
	}
	x, y := spec.training(in, m.MaxTrainRows)
	if len(x) < 2*m.Bins {
		return nil, fmt.Errorf("%w: naive_bayes needs %d training rows, got %d", ErrInsufficientHistory, 2*m.Bins, len(x))
	}
	width := spec.width()
	targetEdges := quantileEdges(y, m.Bins)
	classes := len(targetEdges) + 1
	featEdges := make([][]float64, width)
	for f := 0; f < width; f++ {
		col := make([]float64, len(x))
		for i := range x {
			col[i] = x[i][f]
		}
		featEdges[f] = quantileEdges(col, m.Bins)
	}

	classCount := make([]float64, classes)
	classSum := make([]float64, classes)
	// counts[f][class][bin]
	counts := make([][][]float64, width)
	for f := range counts {
		counts[f] = make([][]float64, classes)
		for c := range counts[f] {
			counts[f][c] = make([]float64, len(featEdges[f])+1)
		}
	}
	for i := range x {
		if i%2048 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		c := binOf(targetEdges, y[i])
		classCount[c]++
		classSum[c] += y[i]
		for f := 0; f < width; f++ {
			counts[f][c][binOf(featEdges[f], x[i][f])]++
		}
	}
	classMean := make([]float64, classes)
	for c := range classMean {
		if classCount[c] > 0 {
			classMean[c] = classSum[c] / classCount[c]
		}
	}

	history := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	feat := make([]float64, 0, width)
	logPost := make([]float64, classes)
	total := float64(len(x))
	for h := 0; h < horizon; h++ {
		feat = spec.row(history, in.TimeAt(len(in.Values)+h), feat)
		best := math.Inf(-1)
		for c := 0; c < classes; c++ {
			lp := math.Log((classCount[c] + 1) / (total + float64(classes)))
			for f := 0; f < width; f++ {
				bins := float64(len(featEdges[f]) + 1)
				lp += math.Log((counts[f][c][binOf(featEdges[f], feat[f])] + 1) / (classCount[c] + bins))
			}
			logPost[c] = lp
			if lp > best {
				best = lp
			}
		}
		var norm, expect float64
		bestClass := 0
		for c := 0; c < classes; c++ {
			p := math.Exp(logPost[c] - best)
			norm += p
			expect += p * classMean[c]
			if logPost[c] > logPost[bestClass] {
				bestClass = c
			}
		}
		if m.Output == "mode" {
			out[h] = classMean[bestClass]
		} else {
			out[h] = expect / norm
		}
		// the next step reads the most probable class as its lag, never the blend: a blend falls between the
		// classes the features were cut into and would be read as a neighbouring class
		history = append(history, classMean[bestClass])
	}
	return out, finite(out)
}
