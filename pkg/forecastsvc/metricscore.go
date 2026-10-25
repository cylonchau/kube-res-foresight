package forecastsvc

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// MetricScore is how one algorithm forecast one source of a module, measured against what Prometheus then reported:
// the errors of the finished prediction runs, weighted by the points each run had an actual for.
type MetricScore struct {
	AlgorithmID string `json:"algorithm_id"`
	Metric      string `json:"metric"`
	SourceName  string `json:"source_name"`
	Kind        string `json:"kind"`
	Category    string `json:"category"`
	Unit        string `json:"unit"`
	// SourceWeight is the weight set on the source; Weight its share among the sources the composite is made of.
	SourceWeight float64 `json:"source_weight"`
	Weight       float64 `json:"weight"`
	Runs         int     `json:"runs"`
	Points       int     `json:"points"`
	MAE          float64 `json:"mae"`
	RMSE         float64 `json:"rmse"`
	// WAPE is the error (sum of absolute errors over the sum of actuals); Accuracy = 1 - WAPE, never below 0.
	WAPE     float64 `json:"error"`
	Accuracy float64 `json:"accuracy"`
	Bias     float64 `json:"bias"`
	// PeakUnder is the average relative shortfall of the predicted peak; Coverage the share of actuals at or below the forecast.
	PeakUnder float64 `json:"peak_under"`
	Coverage  float64 `json:"coverage"`
	// InComposite says whether the source counts towards the composite (see AlgorithmScoreRow.Comparable).
	InComposite bool `json:"in_composite"`
}

// AlgorithmScoreRow is the composite of an algorithm over the sources of a module.
type AlgorithmScoreRow struct {
	AlgorithmID string `json:"algorithm_id"`
	Baseline    bool   `json:"baseline"`
	Custom      bool   `json:"custom"`
	// Accuracy is the weighted accuracy over the sources of the composite; Score is 100 x Accuracy.
	Accuracy float64 `json:"accuracy"`
	Error    float64 `json:"error"`
	Score    float64 `json:"score"`
	Rank     int     `json:"rank"`
	Sources  int     `json:"sources"`
	Runs     int     `json:"runs"`
	Points   int     `json:"points"`
	Best     bool    `json:"best"`
	// BeatsBaseline is set for an algorithm that is not a baseline, when a baseline was measured as well.
	BeatsBaseline *bool `json:"beats_baseline,omitempty"`
}

// MetricScores is the result for a module.
type MetricScores struct {
	ModuleID   uint                `json:"module_id"`
	Algorithms []AlgorithmScoreRow `json:"algorithms"`
	Metrics    []MetricScore       `json:"metrics"`
	// Best is the algorithm with the highest score.
	Best string `json:"best"`
	// Comparable is true when the composite of every algorithm is made of the same sources. When no source has been
	// measured for every algorithm, each composite uses the sources the algorithm has, and the ranking is only indicative.
	Comparable bool `json:"comparable"`
	// Sources are the sources the composites are made of.
	Sources []string `json:"sources"`
}

type acc struct {
	runs, points                         int
	wape, mae, mse, bias, cov, peak, wts float64
}

// MetricScores measures every enabled algorithm of a module on each of its forecast sources against the actuals, over
// the evaluations since the given time (all of them when nil), and combines the sources into one score per algorithm.
// The standard is the same for every algorithm: the algorithms are compared on the sources every one of them was measured on.
func (s *Service) MetricScores(ctx context.Context, moduleID uint, since, until *time.Time) (*MetricScores, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runsOf(ctx, mod)
	if err != nil {
		return nil, err
	}
	out := &MetricScores{ModuleID: moduleID, Algorithms: []AlgorithmScoreRow{}, Metrics: []MetricScore{}, Sources: []string{}}
	data := dataOf(mod)
	sources := []*model.MetricSource{}
	for _, src := range data.ActiveSources() {
		if src.Basis == model.BasisForecast {
			sources = append(sources, src)
		}
	}

	type key struct{ algorithm, metric string }
	measured := map[key]*MetricScore{}
	order := []key{}
	for _, run := range runs {
		for _, src := range sources {
			evals, err := s.Predictions.AllEvaluations(ctx, rdb.EvaluationFilter{ModuleID: moduleID, Metric: src.Key, AlgorithmID: run.Key(), Since: since, Until: until})
			if err != nil {
				return nil, err
			}
			var a acc
			for _, e := range evals {
				if e.Points == 0 {
					continue
				}
				w := float64(e.Points)
				a.runs++
				a.points += e.Points
				a.wape += e.WAPE * w
				a.mae += e.MAE * w
				a.mse += e.RMSE * e.RMSE * w
				a.bias += e.Bias * w
				a.cov += e.Coverage * w
				a.peak += e.PeakUnder
			}
			if a.points == 0 {
				continue
			}
			w := float64(a.points)
			wape := a.wape / w
			k := key{run.Key(), src.Key}
			measured[k] = &MetricScore{AlgorithmID: run.Key(), Metric: src.Key, SourceName: src.Name, Kind: src.Kind, Category: src.Category(), Unit: s.unitOf(ctx, data, src.Key),
				SourceWeight: src.Weight, Runs: a.runs, Points: a.points, MAE: a.mae / w, RMSE: math.Sqrt(a.mse / w), WAPE: wape, Accuracy: math.Max(0, 1-wape),
				Bias: a.bias / w, PeakUnder: a.peak / float64(a.runs), Coverage: a.cov / w}
			order = append(order, k)
		}
	}

	// the sources every algorithm that was measured at all was measured on
	algorithms := []string{}
	seenAlg := map[string]bool{}
	for _, k := range order {
		if !seenAlg[k.algorithm] {
			seenAlg[k.algorithm] = true
			algorithms = append(algorithms, k.algorithm)
		}
	}
	var common []string
	for _, src := range sources {
		all := len(algorithms) > 0
		for _, a := range algorithms {
			if measured[key{a, src.Key}] == nil {
				all = false
			}
		}
		if all {
			common = append(common, src.Key)
		}
	}
	out.Comparable = len(common) > 0
	inCommon := map[string]bool{}
	for _, c := range common {
		inCommon[c] = true
	}
	out.Sources = common

	rows := map[string]*AlgorithmScoreRow{}
	for _, a := range algorithms {
		def, _ := forecast.Lookup(a)
		rows[a] = &AlgorithmScoreRow{AlgorithmID: a, Baseline: def.Baseline, Custom: def.Custom}
	}
	for _, a := range algorithms {
		var total float64
		var mine []*MetricScore
		for _, src := range sources {
			m := measured[key{a, src.Key}]
			if m == nil || (out.Comparable && !inCommon[src.Key]) {
				continue
			}
			w := src.Weight
			if w <= 0 {
				w = 1
			}
			total += w
			m.InComposite = true
			mine = append(mine, m)
		}
		row := rows[a]
		for _, m := range mine {
			w := m.SourceWeight
			if w <= 0 {
				w = 1
			}
			m.Weight = w / total
			row.Accuracy += m.Weight * m.Accuracy
			row.Sources++
			row.Runs += m.Runs
			row.Points += m.Points
		}
		row.Error = 1 - row.Accuracy
		row.Score = 100 * row.Accuracy
	}
	for _, k := range order {
		out.Metrics = append(out.Metrics, *measured[k])
	}
	for _, a := range algorithms {
		out.Algorithms = append(out.Algorithms, *rows[a])
	}
	sort.SliceStable(out.Algorithms, func(i, j int) bool {
		if out.Algorithms[i].Score != out.Algorithms[j].Score {
			return out.Algorithms[i].Score > out.Algorithms[j].Score
		}
		return out.Algorithms[i].AlgorithmID < out.Algorithms[j].AlgorithmID
	})
	bestBaseline := math.Inf(-1)
	for _, r := range out.Algorithms {
		if r.Baseline && r.Score > bestBaseline {
			bestBaseline = r.Score
		}
	}
	for i := range out.Algorithms {
		out.Algorithms[i].Rank = i + 1
		out.Algorithms[i].Best = i == 0
		if !out.Algorithms[i].Baseline && !math.IsInf(bestBaseline, -1) {
			beats := out.Algorithms[i].Score > bestBaseline
			out.Algorithms[i].BeatsBaseline = &beats
		}
	}
	if len(out.Algorithms) > 0 {
		out.Best = out.Algorithms[0].AlgorithmID
	}
	return out, nil
}
