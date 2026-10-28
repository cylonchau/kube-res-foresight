package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/backtest"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// maxRunsPerAlgorithm bounds the evaluations done for one algorithm in one pass.
const maxRunsPerAlgorithm = 100

// Score summarizes how a forecast compares with what actually happened. Values are finite.
type Score struct {
	Points    int
	MAE       float64
	RMSE      float64
	WAPE      float64
	Bias      float64
	PeakUnder float64
	Coverage  float64
}

// ScoreRun compares aligned predictions and actuals. NaN actuals mean "no actual for that step" and are
// skipped; the result has Points == 0 when nothing could be compared.
func ScoreRun(pred, actual []float64) Score {
	var s Score
	var absErr, sqErr, absActual, bias float64
	maxActual, maxPred := math.Inf(-1), math.Inf(-1)
	covered := 0
	for i := range pred {
		if i >= len(actual) || math.IsNaN(actual[i]) {
			continue
		}
		e := pred[i] - actual[i]
		absErr += math.Abs(e)
		sqErr += e * e
		absActual += math.Abs(actual[i])
		bias += e
		if actual[i] <= pred[i] {
			covered++
		}
		maxActual = math.Max(maxActual, actual[i])
		maxPred = math.Max(maxPred, pred[i])
		s.Points++
	}
	if s.Points == 0 {
		return s
	}
	n := float64(s.Points)
	s.MAE, s.RMSE, s.Bias, s.Coverage = absErr/n, math.Sqrt(sqErr/n), bias/n, float64(covered)/n
	if absActual > 0 {
		s.WAPE = absErr / absActual
	} else {
		s.WAPE = s.MAE
	}
	if maxActual > 0 {
		s.PeakUnder = math.Max(0, (maxActual-maxPred)/maxActual)
	}
	return s
}

// EvaluateResult counts the evaluations added for one metric.
type EvaluateResult struct {
	Metric    string `json:"metric"`
	Evaluated int    `json:"evaluated"`
	Pending   int    `json:"pending"` // runs whose horizon has not been fully observed yet
}

// Evaluate scores every finished prediction run of a module's algorithms against the samples collected since. A run
// is finished once the newest stored sample is at or after its last target time. Evaluations are appended; the
// predictions are never modified, and a run is scored once.
func (s *Service) Evaluate(ctx context.Context, moduleID uint) ([]EvaluateResult, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	m := dataOf(mod)
	var results []EvaluateResult
	for _, metric := range configuredMetrics(m) {
		res := EvaluateResult{Metric: metric}
		series, err := s.Samples.Series(ctx, m.ID, metric)
		if err != nil {
			results = append(results, res)
			continue
		}
		latest, ok, err := s.Samples.Latest(ctx, series.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			results = append(results, res)
			continue
		}
		algos, err := s.Predictions.AlgorithmsWithPredictions(ctx, m.ID, metric)
		if err != nil {
			return nil, err
		}
		sort.Strings(algos)
		for _, algo := range algos {
			runs, err := s.Predictions.UnevaluatedRuns(ctx, m.ID, metric, algo, maxRunsPerAlgorithm)
			if err != nil {
				return nil, err
			}
			for _, run := range runs {
				rows, err := s.Predictions.RunRows(ctx, m.ID, metric, algo, run.PredictionTime, run.ModelVersion)
				if err != nil {
					return nil, err
				}
				if len(rows) == 0 {
					continue
				}
				last := rows[len(rows)-1].TargetTime
				if last.After(latest) {
					res.Pending++
					continue
				}
				samples, err := s.Samples.Range(ctx, series.ID, rows[0].TargetTime, last)
				if err != nil {
					return nil, err
				}
				byTime := make(map[int64]float64, len(samples))
				for _, sm := range samples {
					byTime[sm.TS] = sm.Value
				}
				pred := make([]float64, len(rows))
				actual := make([]float64, len(rows))
				for i, r := range rows {
					pred[i] = r.Value
					if v, found := byTime[r.TargetTime.Unix()]; found {
						actual[i] = v
					} else {
						actual[i] = math.NaN()
					}
				}
				sc := ScoreRun(pred, actual)
				ev := &model.PredictionEvaluation{
					ModuleID: m.ID, Metric: metric, AlgorithmID: algo, ModelVersion: run.ModelVersion, PredictionTime: run.PredictionTime,
					QuantilePermille: rows[0].QuantilePermille, Points: sc.Points, Expected: len(rows), MAE: sc.MAE, RMSE: sc.RMSE,
					WAPE: sc.WAPE, Bias: sc.Bias, PeakUnder: sc.PeakUnder, Coverage: sc.Coverage,
				}
				if err := s.Predictions.SaveEvaluation(ctx, ev); err != nil {
					return nil, err
				}
				result := "scored"
				if sc.Points == 0 {
					result = "no_actuals"
				}
				s.Telemetry.recordEvaluation(metric, result, s.now().Sub(last))
				res.Evaluated++
			}
		}
		results = append(results, res)
	}
	return results, nil
}

// LeaderboardEntry ranks one algorithm on the evaluations of one of its sources.
type LeaderboardEntry struct {
	Rank        int     `json:"rank"`
	AlgorithmID string  `json:"algorithm_id"`
	ModuleID    uint    `json:"module_id"`
	Metric      string  `json:"metric"` // the source key
	Catalog     string  `json:"catalog"`
	Baseline    bool    `json:"baseline"`
	Runs        int     `json:"runs"`
	Points      int     `json:"points"`
	WAPE        float64 `json:"wape"` // mean of the runs' WAPE, weighted by compared points
	// RiskWAPE is the WAPE with underestimates costing backtest.DefaultUnderWeight times an overestimate; the ranking is by it.
	RiskWAPE      float64 `json:"risk_wape"`
	MAE           float64 `json:"mae"`        // same weighting
	Bias          float64 `json:"bias"`       // same weighting; positive = over-prediction
	PeakUnder     float64 `json:"peak_under"` // mean over runs
	Coverage      float64 `json:"coverage"`   // same weighting
	BeatsBaseline *bool   `json:"beats_baseline,omitempty"`
	Promoted      *bool   `json:"promoted,omitempty"` // latest backtest verdict, when one exists
	// Status is ready for a ranked algorithm; for one with nothing to rank it says why (Rank is 0 then).
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Leaderboard ranks the enabled algorithms of a module on the sources of one kind (cpu, memory, ...) by
// observed RiskWAPE (WAPE that counts running low as worse than running high, because a forecast that is too low
// starves the workload) over the evaluations since the given time; WAPE breaks ties and is shown beside it. Ranking is by what actually happened, not by backtest;
// it never promotes anything by itself.
func (s *Service) Leaderboard(ctx context.Context, moduleID uint, kind string, since, until *time.Time) ([]LeaderboardEntry, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runsOf(ctx, mod)
	if err != nil {
		return nil, err
	}
	var entries, unranked []LeaderboardEntry
	for _, inst := range runs {
		for _, src := range inst.ActiveSources() {
			if src.Kind != kind || src.Basis != model.BasisForecast {
				continue
			}
			evals, err := s.Predictions.AllEvaluations(ctx, rdb.EvaluationFilter{ModuleID: inst.ID, Metric: src.Key, AlgorithmID: inst.Key(), Since: since, Until: until})
			if err != nil {
				return nil, err
			}
			var runs, points int
			var wape, risk, mae, bias, cov, peak float64
			for _, e := range evals {
				if e.Points == 0 {
					continue
				}
				w := float64(e.Points)
				runs++
				points += e.Points
				wape += e.WAPE * w
				risk += backtest.RiskWAPE(e.WAPE, e.MAE, e.Bias, backtest.DefaultUnderWeight) * w
				mae += e.MAE * w
				bias += e.Bias * w
				cov += e.Coverage * w
				peak += e.PeakUnder
			}
			if points == 0 {
				st := s.AlgorithmStatus(ctx, inst.ID, src.Key, inst.Key(), false, "it forecast, but no forecast has met its actual values yet in this range, so there is nothing to score")
				if len(evals) == 0 && st.Status == StatusNoResult {
					if n, _ := s.Predictions.CountPredictions(ctx, inst.ID, src.Key, inst.Key()); n == 0 {
						st.Reason = "it ran, but has made no forecast for this metric"
					}
				}
				unranked = append(unranked, LeaderboardEntry{AlgorithmID: inst.Key(), ModuleID: inst.ID, Metric: src.Key, Catalog: inst.AlgorithmID, Status: st.Status, Reason: st.Reason})
				continue
			}
			w := float64(points)
			def, _ := forecast.Lookup(inst.AlgorithmID)
			entry := LeaderboardEntry{Status: StatusReady, AlgorithmID: inst.Key(), ModuleID: inst.ID, Metric: src.Key, Catalog: inst.AlgorithmID, Baseline: def.Baseline,
				Runs: runs, Points: points, WAPE: wape / w, RiskWAPE: risk / w, MAE: mae / w, Bias: bias / w, Coverage: cov / w, PeakUnder: peak / float64(runs)}
			if run, err := s.Predictions.LatestBacktest(ctx, inst.ID, src.Key, inst.Key()); err == nil && run != nil {
				promoted := run.Promoted
				entry.Promoted = &promoted
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RiskWAPE != entries[j].RiskWAPE {
			return entries[i].RiskWAPE < entries[j].RiskWAPE
		}
		if entries[i].WAPE != entries[j].WAPE {
			return entries[i].WAPE < entries[j].WAPE
		}
		return entries[i].AlgorithmID < entries[j].AlgorithmID
	})
	bestBaseline := math.Inf(1)
	for _, e := range entries {
		if e.Baseline && e.RiskWAPE < bestBaseline {
			bestBaseline = e.RiskWAPE
		}
	}
	for i := range entries {
		entries[i].Rank = i + 1
		if !entries[i].Baseline && !math.IsInf(bestBaseline, 1) {
			beats := entries[i].RiskWAPE < bestBaseline
			entries[i].BeatsBaseline = &beats
		}
	}
	// the enabled algorithms with nothing to rank follow the ranked ones, each with its reason
	sort.Slice(unranked, func(i, j int) bool { return unranked[i].AlgorithmID < unranked[j].AlgorithmID })
	return append(entries, unranked...), nil
}

// EvaluationSummary is a one-line description for logs.
func (r EvaluateResult) String() string {
	return fmt.Sprintf("%s: %d evaluated, %d pending", r.Metric, r.Evaluated, r.Pending)
}
