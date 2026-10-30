package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/backtest"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

const (
	defaultCutoffs = 10
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
	// Origins are spread over the whole history and kept off exact multiples of a day, so the score covers different hours
	// of the day, and are shared: a candidate and the baseline it is judged against are scored on the same windows.
	season := 0
	if step > 0 && step <= 12*time.Hour {
		season = int(24 * time.Hour / step)
	}
	shape := backtest.Config{HorizonSteps: h, Cutoffs: cutoffs, Spread: true, SeasonSteps: season}
	run := func(model forecast.Model, withCovariates bool, alpha float64, origins []int) (backtest.Result, error) {
		cfg := shape
		cfg.Alpha, cfg.Origins = alpha, origins
		data := in
		if withCovariates {
			data = input
		}
		rctx, cancel := context.WithTimeout(ctx, s.budgetFor(ctx, m))
		defer cancel()
		res, err := backtest.Run(rctx, data, model, cfg)
		if err != nil && rctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return backtest.Result{Skipped: ErrTimeout.Error()}, nil
		}
		return res, err
	}
	alpha := 0.5
	if q, ok := bm.model.(forecast.Quantiled); ok {
		if level, on := q.QuantileLevel(); on {
			alpha = level // a quantile forecast is scored with the pinball loss and the coverage of its own level
		}
	}
	row := &model.BacktestRun{ModuleID: m.ID, Metric: metric, AlgorithmID: bm.key, ModelVersion: bm.version, Params: bm.params,
		StepSeconds: series.StepSeconds, HorizonSteps: h, Cutoffs: cutoffs, WindowStart: in.TimeAt(0), WindowEnd: in.End}

	res, err := run(bm.model, true, alpha, nil)
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
		policy := backtest.Policy{MinPoints: m.PromoteMinPoints, MinImprovement: m.PromoteMinImprovement, PeakTolerance: m.PromotePeakTolerance}
		cmp := s.compareWithBaselines(ctx, bm, step, input, in, shape, alpha, res, policy, run)
		if cmp.err != nil {
			return nil, cmp.err
		}
		row.BaselineID, row.Metrics, row.BaselineRes = cmp.baselineID, marshal(cmp.candidate), marshal(cmp.baseline)
		row.Promoted, row.Reason = cmp.verdict.Promoted, trim(cmp.verdict.Reason, 512)
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

// comparison is the outcome of judging a candidate against the implicit baselines.
type comparison struct {
	baselineID string
	candidate  backtest.Result // the candidate on the windows of the reported baseline
	baseline   backtest.Result
	verdict    backtest.Verdict
	err        error
}

// originTimes are the last training timestamps of the given cutoffs of data.
func originTimes(data forecast.Input, origins []int) []time.Time {
	out := make([]time.Time, len(origins))
	for i, c := range origins {
		out[i] = data.TimeAt(c - 1)
	}
	return out
}

// originsIn maps cutoffs given by time onto another series of the same grid; a time it does not cover is dropped.
func originsIn(data forecast.Input, times []time.Time) []int {
	out := make([]int, 0, len(times))
	for _, t := range times {
		if i, ok := gridIndex(data, t); ok && data.TimeAt(i).Equal(t) {
			out = append(out, i+1)
		}
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// compareWithBaselines judges a candidate against every baseline it shares enough windows with. Each baseline needs
// history of its own (the same time of the previous day needs a day), so the windows both can be scored on are the
// ones after the longer of the two minimums; the candidate is scored again on exactly those windows when they differ
// from its own, so the two WAPEs describe the same data. A baseline that would leave fewer than three windows (or
// fewer than the candidate has) is not used; the candidate is promoted only when it beats all the others. The reported
// baseline is the first one it fails against, or else the strongest.
func (s *Service) compareWithBaselines(ctx context.Context, bm *boundModel, step time.Duration, cdata, bdata forecast.Input, shape backtest.Config, alpha float64,
	own backtest.Result, policy backtest.Policy, run func(forecast.Model, bool, float64, []int) (backtest.Result, error)) comparison {
	ownOrigins := backtest.Origins(len(cdata.Values), shape, forecast.MinSamplesFor(bm.model, shape.HorizonSteps))
	need := len(ownOrigins)
	if need > 3 {
		need = 3
	}
	out := comparison{candidate: own, baseline: backtest.Result{Skipped: "no baseline shares enough windows with this algorithm"}}
	out.verdict = backtest.Evaluate(out.baseline, own, policy)
	if own.Skipped != "" {
		return out
	}
	var strongest *comparison
	failed := false
	for _, b := range s.implicitBaselines(step) {
		min := forecast.MinSamplesFor(bm.model, shape.HorizonSteps)
		if bmin := forecast.MinSamplesFor(b.model, shape.HorizonSteps); bmin > min {
			min = bmin
		}
		origins := backtest.Origins(len(cdata.Values), shape, min)
		if need == 0 || len(origins) < need {
			continue
		}
		cand := own
		if !sameInts(origins, ownOrigins) {
			var err error
			if cand, err = run(bm.model, true, alpha, origins); err != nil {
				return comparison{err: err}
			}
		}
		base, err := run(b.model, false, alpha, originsIn(bdata, originTimes(cdata, origins)))
		if err != nil {
			return comparison{err: err}
		}
		if base.Skipped != "" || cand.Skipped != "" {
			continue
		}
		c := comparison{baselineID: b.key, candidate: cand, baseline: base, verdict: backtest.Evaluate(base, cand, policy)}
		switch {
		case !c.verdict.Promoted && !failed:
			failed = true
			strongest = &c
		case !failed && (strongest == nil || base.WAPE < strongest.baseline.WAPE):
			strongest = &c
		}
	}
	if strongest != nil {
		return *strongest
	}
	return out
}
