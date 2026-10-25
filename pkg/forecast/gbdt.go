package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
)

const gbdtBins = 32

// GBDT is gradient-boosted regression trees on lag and calendar features, forecast recursively. The
// objective is squared error, or the alpha-quantile (pinball) loss. Trees are deterministic (no sampling), so
// the same history always yields the same forecast. Trees cannot extrapolate beyond the range of the training
// targets, which is why the backtest compares this model with the baselines before it can be promoted.
type GBDT struct {
	Lags         int
	Calendar     bool
	Trees        int
	LearningRate float64
	MaxDepth     int
	MinLeaf      int
	Quantile     bool // false = squared error
	Alpha        float64
	MaxTrainRows int
	CovLags      int // past values of each covariate used as features; 0 = ignore covariates
}

func (GBDT) ID() string { return "gbdt" }

func (m GBDT) spec() featureSpec {
	return featureSpec{Lags: m.Lags, Calendar: m.Calendar, CovLags: m.CovLags}
}

func (m GBDT) MinSamples() int { return m.Lags + 4*m.MinLeaf + 1 }

type treeNode struct {
	feature     int
	threshold   float64
	left, right *treeNode
	value       float64
	leaf        bool
}

func (n *treeNode) predict(x []float64) float64 {
	for !n.leaf {
		if x[n.feature] <= n.threshold {
			n = n.left
		} else {
			n = n.right
		}
	}
	return n.value
}

type gbdtData struct {
	x      [][]float64
	binned [][]uint8
	edges  [][]float64 // per feature: upper edge of each bin except the last
	y      []float64
}

func newGBDTData(x [][]float64, y []float64) *gbdtData {
	w := len(x[0])
	d := &gbdtData{x: x, y: y, edges: make([][]float64, w), binned: make([][]uint8, len(x))}
	for f := 0; f < w; f++ {
		col := make([]float64, len(x))
		for i := range x {
			col[i] = x[i][f]
		}
		sort.Float64s(col)
		var edges []float64
		for b := 1; b < gbdtBins; b++ {
			v := col[b*(len(col)-1)/gbdtBins]
			if len(edges) == 0 || v > edges[len(edges)-1] {
				edges = append(edges, v)
			}
		}
		// an edge equal to the maximum would leave the right bin empty
		for len(edges) > 0 && edges[len(edges)-1] >= col[len(col)-1] {
			edges = edges[:len(edges)-1]
		}
		d.edges[f] = edges
	}
	for i := range x {
		d.binned[i] = make([]uint8, w)
		for f := 0; f < w; f++ {
			d.binned[i][f] = uint8(sort.SearchFloat64s(d.edges[f], x[i][f]))
		}
	}
	return d
}

type gbdtBuilder struct {
	d       *gbdtData
	grad    []float64 // pseudo-residual the tree structure is fitted to
	resid   []float64 // y - current prediction, used for leaf values
	depth   int
	minLeaf int
	alpha   float64
	quant   bool
	// features, when set, restricts the splits to those feature columns (a random forest tree sees a subset).
	features []int
}

func (b *gbdtBuilder) leafValue(idx []int) float64 {
	vals := make([]float64, len(idx))
	for k, i := range idx {
		vals[k] = b.resid[i]
	}
	if b.quant {
		return quantile(vals, b.alpha)
	}
	var s float64
	for _, v := range vals {
		s += v
	}
	return s / float64(len(vals))
}

func (b *gbdtBuilder) build(idx []int, depth int) *treeNode {
	if depth >= b.depth || len(idx) < 2*b.minLeaf {
		return &treeNode{leaf: true, value: b.leafValue(idx)}
	}
	w := len(b.d.edges)
	var total float64
	for _, i := range idx {
		total += b.grad[i]
	}
	n := float64(len(idx))
	bestGain, bestF, bestBin := 0.0, -1, -1
	nf := w
	if b.features != nil {
		nf = len(b.features)
	}
	for fi := 0; fi < nf; fi++ {
		f := fi
		if b.features != nil {
			f = b.features[fi]
		}
		nb := len(b.d.edges[f]) + 1
		if nb < 2 {
			continue
		}
		sum := make([]float64, nb)
		cnt := make([]int, nb)
		for _, i := range idx {
			bin := b.d.binned[i][f]
			sum[bin] += b.grad[i]
			cnt[bin]++
		}
		var ls float64
		lc := 0
		for bin := 0; bin < nb-1; bin++ {
			ls += sum[bin]
			lc += cnt[bin]
			rc := len(idx) - lc
			if lc < b.minLeaf || rc < b.minLeaf {
				continue
			}
			rs := total - ls
			gain := ls*ls/float64(lc) + rs*rs/float64(rc) - total*total/n
			if gain > bestGain+1e-12 {
				bestGain, bestF, bestBin = gain, f, bin
			}
		}
	}
	if bestF < 0 {
		return &treeNode{leaf: true, value: b.leafValue(idx)}
	}
	var left, right []int
	for _, i := range idx {
		if int(b.d.binned[i][bestF]) <= bestBin {
			left = append(left, i)
		} else {
			right = append(right, i)
		}
	}
	return &treeNode{
		feature: bestF, threshold: b.d.edges[bestF][bestBin],
		left: b.build(left, depth+1), right: b.build(right, depth+1),
	}
}

func (m GBDT) validate() error {
	if err := m.spec().validate(); err != nil {
		return err
	}
	if m.Trees < 1 {
		return fmt.Errorf("n_estimators must be at least 1")
	}
	if m.LearningRate <= 0 || m.LearningRate > 1 {
		return fmt.Errorf("learning_rate must be in (0, 1]")
	}
	if m.MaxDepth < 1 {
		return fmt.Errorf("max_depth must be at least 1")
	}
	if m.MinLeaf < 1 {
		return fmt.Errorf("min_samples_leaf must be at least 1")
	}
	if m.Quantile && (m.Alpha <= 0 || m.Alpha >= 1) {
		return fmt.Errorf("alpha must be in (0, 1) for the quantile objective")
	}
	return nil
}

func (m GBDT) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
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
	if len(x) < 2*m.MinLeaf {
		return nil, fmt.Errorf("%w: gbdt needs %d training rows, got %d", ErrInsufficientHistory, 2*m.MinLeaf, len(x))
	}
	data := newGBDTData(x, y)

	alpha := 0.5
	if m.Quantile {
		alpha = m.Alpha
	}
	base := 0.0
	if m.Quantile {
		base = quantile(append([]float64(nil), y...), alpha)
	} else {
		for _, v := range y {
			base += v
		}
		base /= float64(len(y))
	}
	pred := make([]float64, len(y))
	for i := range pred {
		pred[i] = base
	}
	builder := &gbdtBuilder{d: data, grad: make([]float64, len(y)), resid: make([]float64, len(y)),
		depth: m.MaxDepth, minLeaf: m.MinLeaf, alpha: alpha, quant: m.Quantile}
	all := make([]int, len(y))
	for i := range all {
		all[i] = i
	}
	trees := make([]*treeNode, 0, m.Trees)
	for t := 0; t < m.Trees; t++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := range y {
			builder.resid[i] = y[i] - pred[i]
			if m.Quantile {
				if y[i] > pred[i] {
					builder.grad[i] = alpha
				} else {
					builder.grad[i] = alpha - 1
				}
			} else {
				builder.grad[i] = builder.resid[i]
			}
		}
		tree := builder.build(all, 0)
		trees = append(trees, tree)
		for i := range y {
			pred[i] += m.LearningRate * tree.predict(x[i])
		}
	}

	history := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	feat := make([]float64, 0, spec.width())
	for h := 0; h < horizon; h++ {
		feat = spec.row(history, in.TimeAt(len(in.Values)+h), feat)
		v := base
		for _, tree := range trees {
			v += m.LearningRate * tree.predict(feat)
		}
		out[h] = v
		history = append(history, v)
	}
	if err := finite(out); err != nil {
		return nil, err
	}
	for _, v := range out {
		if math.IsNaN(v) {
			return nil, fmt.Errorf("gbdt produced NaN")
		}
	}
	return out, nil
}
