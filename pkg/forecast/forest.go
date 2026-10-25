package forecast

import (
	"context"
	"fmt"
	"math"
	"math/rand"
)

// RandomForest is bagged regression trees on lag, calendar and covariate features, forecast recursively. Each tree
// sees a bootstrap sample of the rows and a random subset of the features; the forecast is the mean of the trees.
// The random generator is seeded from the Seed parameter, so the same history and parameters always give the same
// forecast. Like any tree model it cannot extrapolate beyond the range of its training targets.
type RandomForest struct {
	Lags         int
	Calendar     bool
	Trees        int
	MaxDepth     int
	MinLeaf      int
	FeatureRatio float64 // share of the features each tree may split on, (0, 1]
	SampleRatio  float64 // share of the rows each tree is trained on (with replacement), (0, 1]
	MaxTrainRows int
	CovLags      int
	Seed         int64
}

func (RandomForest) ID() string { return "random_forest" }

func (m RandomForest) spec() featureSpec {
	return featureSpec{Lags: m.Lags, Calendar: m.Calendar, CovLags: m.CovLags}
}

func (m RandomForest) MinSamples() int { return m.Lags + 4*m.MinLeaf + 1 }

func (m RandomForest) validate() error {
	if err := m.spec().validate(); err != nil {
		return err
	}
	if m.Trees < 1 {
		return fmt.Errorf("n_estimators must be at least 1")
	}
	if m.MaxDepth < 1 {
		return fmt.Errorf("max_depth must be at least 1")
	}
	if m.MinLeaf < 1 {
		return fmt.Errorf("min_samples_leaf must be at least 1")
	}
	if m.FeatureRatio <= 0 || m.FeatureRatio > 1 {
		return fmt.Errorf("feature_ratio must be in (0, 1]")
	}
	if m.SampleRatio <= 0 || m.SampleRatio > 1 {
		return fmt.Errorf("sample_ratio must be in (0, 1]")
	}
	return nil
}

func (m RandomForest) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
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
		return nil, fmt.Errorf("%w: random_forest needs %d training rows, got %d", ErrInsufficientHistory, 2*m.MinLeaf, len(x))
	}
	data := newGBDTData(x, y)
	rng := rand.New(rand.NewSource(m.Seed))
	width := len(data.edges)
	perTree := int(math.Ceil(m.FeatureRatio * float64(width)))
	if perTree < 1 {
		perTree = 1
	}
	sample := int(math.Ceil(m.SampleRatio * float64(len(y))))
	if sample < 2*m.MinLeaf {
		sample = 2 * m.MinLeaf
	}

	// the tree is fitted to the targets themselves: the split gain is the variance reduction and the leaf the mean
	builder := &gbdtBuilder{d: data, grad: y, resid: y, depth: m.MaxDepth, minLeaf: m.MinLeaf}
	trees := make([]*treeNode, 0, m.Trees)
	for t := 0; t < m.Trees; t++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		idx := make([]int, sample)
		for i := range idx {
			idx[i] = rng.Intn(len(y))
		}
		builder.features = rng.Perm(width)[:perTree]
		trees = append(trees, builder.build(idx, 0))
	}

	history := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	feat := make([]float64, 0, spec.width())
	for h := 0; h < horizon; h++ {
		feat = spec.row(history, in.TimeAt(len(in.Values)+h), feat)
		var sum float64
		for _, tree := range trees {
			sum += tree.predict(feat)
		}
		out[h] = sum / float64(len(trees))
		history = append(history, out[h])
	}
	return out, finite(out)
}
