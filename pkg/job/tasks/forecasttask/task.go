// Package forecasttask holds the scheduled forecasting jobs. Every job walks the enabled modules and applies every
// enabled algorithm to each of them (an algorithm needs no creating or binding: enabling it is enough); they run the
// same forecastsvc steps the HTTP API exposes, and a failing module is reported but never stops the others.
package forecasttask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const (
	// RunJobType collects history and runs every enabled algorithm for every enabled module, then (outside observe
	// mode) recommends for the module.
	RunJobType = "forecast.run"
	// ScoreJobType replays the recent windows for the algorithms of each module and ranks them.
	ScoreJobType = "forecast.score"
	// BacktestJobType re-evaluates every enabled algorithm of every module against the baselines.
	BacktestJobType = "forecast.backtest"

	executionRetentionDays = 30
	admissionRetentionDays = 30
)

// Run is the collect + predict + recommend job.
type Run struct {
	Modules *rdb.ModuleDAO
	Service *forecastsvc.Service
}

func (t *Run) Type() string { return RunJobType }

func (t *Run) Execute(ctx context.Context, _ *model.Job) error {
	modules, err := t.Modules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, m := range modules {
		if due, err := t.Service.Due(ctx, m); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		} else if !due {
			continue // the module runs every run_interval_minutes, not every cycle of the task
		}
		failures := map[string]string{} // algorithm/source -> why, for the fallback reasons of the module
		data, err := t.Service.RunData(ctx, m.ID, failures)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		}
		// step errors (a missing mapping, a disabled datasource, stale data) are configuration or data problems:
		// they are logged and shown in the pipeline result, and retrying the whole job would not fix them
		for _, e := range data.Errors {
			logger.Warn("forecast pipeline step failed", logger.String("module", m.Name), logger.String("error", e))
		}
		res, err := t.Service.RunModule(ctx, m.ID, failures, data.Errors)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		}
		for _, e := range res.Errors[len(data.Errors):] {
			logger.Warn("forecast module step failed", logger.String("module", m.Name), logger.String("error", e))
		}
	}
	// operator reports are an audit trail, not an archive
	execDays, admDays := t.Service.HistoryDays(ctx, executionRetentionDays), t.Service.HistoryDays(ctx, admissionRetentionDays)
	if _, err := t.Service.Predictions.PruneOutcomes(ctx, time.Now().UTC().AddDate(0, 0, -execDays)); err != nil {
		logger.Warn("pruning scale outcomes failed", logger.Err(err))
	}
	if _, err := t.Service.Predictions.PruneExecutions(ctx, time.Now().UTC().AddDate(0, 0, -execDays)); err != nil {
		logger.Warn("pruning scaling executions failed", logger.Err(err))
	}
	if _, err := t.Service.Predictions.PruneAdmissions(ctx, time.Now().UTC().AddDate(0, 0, -admDays)); err != nil {
		logger.Warn("pruning admission decisions failed", logger.Err(err))
	}
	return joinFailures(failed)
}

// Backtest is the scheduled evaluation job: every enabled algorithm on each source of every enabled module.
type Backtest struct {
	Modules *rdb.ModuleDAO
	Service *forecastsvc.Service
}

func (t *Backtest) Type() string { return BacktestJobType }

func (t *Backtest) Execute(ctx context.Context, _ *model.Job) error {
	modules, err := t.Modules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, m := range modules {
		for _, metric := range model.NewRun(m, &model.AlgorithmConfig{}).SourceKeys() {
			if _, err := t.Service.Backtest(ctx, m.ID, metric, 0); err != nil {
				if errors.Is(err, forecastsvc.ErrNotConfigured) || errors.Is(err, forecastsvc.ErrStale) {
					logger.Info("forecast backtest skipped", logger.String("module", m.Name), logger.String("metric", metric), logger.String("reason", err.Error()))
					continue
				}
				failed = append(failed, fmt.Sprintf("%s/%s: %v", m.Name, metric, err))
			}
		}
	}
	return joinFailures(failed)
}

// Score is the scheduled scoring job: the score and rank of every algorithm for each module, which the selection reads.
type Score struct {
	Modules *rdb.ModuleDAO
	Service *forecastsvc.Service
}

func (t *Score) Type() string { return ScoreJobType }

func (t *Score) Execute(ctx context.Context, _ *model.Job) error {
	modules, err := t.Modules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, m := range modules {
		if _, err := t.Service.ScoreModule(ctx, m.ID); err != nil {
			if errors.Is(err, forecastsvc.ErrNotConfigured) || errors.Is(err, forecastsvc.ErrStale) || errors.Is(err, forecastsvc.ErrNothingToScore) {
				logger.Info("forecast scoring skipped", logger.String("module", m.Name), logger.String("reason", err.Error()))
				continue
			}
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
		}
	}
	return joinFailures(failed)
}

func joinFailures(failed []string) error {
	if len(failed) == 0 {
		return nil
	}
	return errors.New(strings.Join(failed, "; "))
}

// EvaluateJobType scores finished prediction runs against the actuals collected since.
const EvaluateJobType = "forecast.evaluate"

// Evaluate is the actuals back-fill job: every enabled module.
type Evaluate struct {
	Modules *rdb.ModuleDAO
	Service *forecastsvc.Service
}

func (t *Evaluate) Type() string { return EvaluateJobType }

func (t *Evaluate) Execute(ctx context.Context, _ *model.Job) error {
	modules, err := t.Modules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, m := range modules {
		results, err := t.Service.Evaluate(ctx, m.ID)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
			continue
		}
		for _, r := range results {
			if r.Evaluated > 0 {
				logger.Info("forecast evaluation", logger.String("module", m.Name), logger.String("result", r.String()))
			}
		}
	}
	// the prediction rows are the bulk of the data (one per step, run and algorithm), so the hourly pass trims them
	if removed, err := t.Service.Predictions.Prune(ctx, time.Now().UTC(), t.Service.RetentionPolicy(ctx)); err != nil {
		logger.Warn("pruning forecast history failed", logger.Err(err))
	} else if removed["predictions"] > 0 {
		logger.Info("pruned forecast history", logger.Int64("predictions", removed["predictions"]), logger.Int64("recommendations", removed["recommendations"]))
	}
	return joinFailures(failed)
}
