package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/backtest"
	"github.com/cylonchau/kubeforecast/pkg/forecast"
	"github.com/cylonchau/kubeforecast/pkg/model"
)

const (
	defaultCutoffs = 5
	maxCutoffs     = 50
)

func marshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// Backtest replays every enabled algorithm on one of the module's sources over rolling cutoffs and compares each with
// the baselines (naive, and the same time of the previous day) on the same cutoffs. A baseline algorithm is always
// eligible; any other is promoted only when it beats the best baseline under its promotion policy. One run per
// algorithm is stored with the verdict.
func (s *Service) Backtest(ctx context.Context, moduleID uint, metric string, cutoffs int) ([]*model.BacktestRun, error) {
	mod, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runsOf(ctx, mod)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: no algorithm is enabled", ErrNotConfigured)
	}
	if !dataOf(mod).Covers(metric) {
		return nil, fmt.Errorf("%w: the module does not forecast a source %q", ErrNotConfigured, metric)
	}
	var out []*model.BacktestRun
	var last error
	for _, m := range runs {
		rows, err := s.backtestRun(ctx, m, metric, cutoffs)
		if err != nil {
			// one algorithm that cannot run (a missing parameter, too little history) does not stop the others
			last = fmt.Errorf("%s: %w", m.Key(), err)
			continue
		}
		out = append(out, rows...)
	}
	if len(out) == 0 && last != nil {
		return nil, last
	}
	return out, nil
}

// backtestRun replays one algorithm of a module on one of its sources.
func (s *Service) backtestRun(ctx context.Context, m *model.AlgorithmRun, metric string, cutoffs int) ([]*model.BacktestRun, error) {
	if cutoffs <= 0 {
		cutoffs = defaultCutoffs
	}
	if cutoffs > maxCutoffs {
		return nil, fmt.Errorf("cutoffs must be at most %d", maxCutoffs)
	}
	bm, err := bind(m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Key(), err)
	}
	started := time.Now()
	in, series, err := s.loadTarget(ctx, m, metric)
	if err != nil {
		return nil, err
	}
	step := time.Duration(series.StepSeconds) * time.Second
	h := horizonSteps(m, step)

	covariates := coverOf(m, metric, bm.def)
	input := in
	if len(covariates) > 0 {
		bundle := s.loadBundle(ctx, m, metric, covariates)
		if bundle.err != nil {
			return nil, bundle.err
		}
		input = bundle.bundle.In
	}
	run := func(model forecast.Model, withCovariates bool, alpha float64) (backtest.Result, error) {
		cfg := backtest.Config{HorizonSteps: h, Cutoffs: cutoffs, Alpha: alpha}
		data := in
		if withCovariates {
			data = input
		}
		rctx, cancel := context.WithTimeout(ctx, budgetFor(m))
		defer cancel()
		res, err := backtest.Run(rctx, data, model, cfg)
		if err != nil && rctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return backtest.Result{Skipped: ErrTimeout.Error()}, nil
		}
		return res, err
	}
	alpha := 0.5
	if g, ok := bm.model.(forecast.GBDT); ok && g.Quantile {
		alpha = g.Alpha
	}
	row := &model.BacktestRun{ModuleID: m.ID, Metric: metric, AlgorithmID: bm.key, ModelVersion: bm.version, Params: bm.params,
		StepSeconds: series.StepSeconds, HorizonSteps: h, Cutoffs: cutoffs, WindowStart: in.TimeAt(0), WindowEnd: in.End}

	res, err := run(bm.model, true, alpha)
	if err != nil {
		row.BaselineID, row.Metrics, row.Reason = bm.key, marshal(backtest.Result{Skipped: err.Error()}), "failed: "+err.Error()
		row.BaselineRes = row.Metrics
	} else if bm.def.Baseline {
		row.BaselineID, row.Metrics, row.BaselineRes = bm.key, marshal(res), marshal(res)
		row.Promoted, row.Reason = res.Skipped == "", "baseline: always eligible"
		if res.Skipped != "" {
			row.Reason = "baseline skipped: " + res.Skipped
		}
	} else {
		baselineRes, baselineID := backtest.Result{Skipped: "no baseline could be evaluated"}, ""
		var best *backtest.Result
		for _, b := range s.implicitBaselines(step) {
			r, err := run(b.model, false, 0.5)
			if err != nil {
				return nil, err
			}
			if r.Skipped == "" && (best == nil || r.WAPE < best.WAPE) {
				rr := r
				best, baselineID = &rr, b.key
			}
		}
		if best != nil {
			baselineRes = *best
		}
		policy := backtest.Policy{MinPoints: m.PromoteMinPoints, MinImprovement: m.PromoteMinImprovement, PeakTolerance: m.PromotePeakTolerance}
		verdict := backtest.Evaluate(baselineRes, res, policy)
		row.BaselineID, row.Metrics, row.BaselineRes = baselineID, marshal(res), marshal(baselineRes)
		row.Promoted, row.Reason = verdict.Promoted, trim(verdict.Reason, 512)
	}
	if err := s.Predictions.SaveBacktest(ctx, row); err != nil {
		return nil, err
	}
	s.Telemetry.recordBacktest(metric, row.AlgorithmID, backtestResult(row))
	s.Telemetry.recordBacktestPass(metric, time.Since(started))
	return []*model.BacktestRun{row}, nil
}

// implicitBaselines are what a non-baseline algorithm has to beat: the last value, and the value at the same time of
// the previous day (which only has a score when a day of history exists).
func (s *Service) implicitBaselines(step time.Duration) []*boundModel {
	var out []*boundModel
	if naive, err := bind(&model.AlgorithmRun{Name: "naive", AlgorithmID: "naive"}); err == nil {
		out = append(out, naive)
	}
	season := int(24 * time.Hour / step)
	if season > 1 {
		if daily, err := bind(&model.AlgorithmRun{Name: "seasonal_naive", AlgorithmID: "seasonal_naive", Params: fmt.Sprintf(`{"season_length_steps":%d}`, season)}); err == nil {
			out = append(out, daily)
		}
	}
	return out
}

// backtestResult classifies a stored run for the metrics: promoted, rejected, skipped (nothing to score) or failed.
func backtestResult(r *model.BacktestRun) string {
	switch {
	case strings.HasPrefix(r.Reason, "failed"):
		return "failed"
	case strings.Contains(r.Reason, "skipped") || strings.Contains(r.Metrics, `"skipped"`):
		return "skipped"
	case r.Promoted:
		return "promoted"
	}
	return "rejected"
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
