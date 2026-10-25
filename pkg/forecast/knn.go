package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// KNN is analogue forecasting: the latest Window values are compared with every earlier window of the history, and
// the forecast is the average of what followed the K most similar ones. With Normalize "relative" each window is
// divided by its own mean first, so a pattern seen at a lower load still matches, and the result is scaled back to
// the level of the current window. Only values before the forecast origin are compared, so nothing leaks.
type KNN struct {
	Window    int
	K         int
	Normalize string // "relative" or "none"
}

func (KNN) ID() string        { return "knn" }
func (m KNN) MinSamples() int { return m.Window + m.K + 1 }
func (m KNN) relative() bool  { return m.Normalize != "none" }
func (m KNN) validate() error {
	if m.Window < 2 {
		return fmt.Errorf("window must be at least 2")
	}
	if m.K < 1 {
		return fmt.Errorf("k must be at least 1")
	}
	if m.Normalize != "" && m.Normalize != "relative" && m.Normalize != "none" {
		return fmt.Errorf("normalize must be relative or none")
	}
	return nil
}

func windowMean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func (m KNN) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	query := in.Values[n-m.Window:]
	qMean := windowMean(query)
	scale := func(mean float64) float64 {
		if !m.relative() || math.Abs(mean) < 1e-12 {
			return 1
		}
		return mean
	}
	qScale := scale(qMean)

	type candidate struct {
		end  int // index of the last value of the window
		dist float64
	}
	var cands []candidate
	// a window ending at e is usable when the horizon values after it exist
	for e := m.Window - 1; e+horizon <= n-1; e++ {
		if e%2048 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		win := in.Values[e-m.Window+1 : e+1]
		wScale := scale(windowMean(win))
		var d float64
		for i := range win {
			diff := win[i]/wScale - query[i]/qScale
			d += diff * diff
		}
		cands = append(cands, candidate{e, d})
	}
	if len(cands) < m.K {
		return nil, fmt.Errorf("%w: knn needs %d earlier windows followed by %d values, found %d", ErrInsufficientHistory, m.K, horizon, len(cands))
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].dist < cands[b].dist })
	cands = cands[:m.K]

	out := make([]float64, horizon)
	var wsum float64
	for _, c := range cands {
		w := 1 / (math.Sqrt(c.dist) + 1e-9) // closer analogues count more
		wScale := scale(windowMean(in.Values[c.end-m.Window+1 : c.end+1]))
		for h := 0; h < horizon; h++ {
			out[h] += w * in.Values[c.end+1+h] / wScale
		}
		wsum += w
	}
	for h := range out {
		out[h] = out[h] / wsum * qScale
	}
	return out, finite(out)
}
