package forecastsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// ErrStale means the newest stored sample is too old to predict from.
var ErrStale = errors.New("stored history is stale")

// ErrTimeout means an algorithm did not finish within its time budget.
var ErrTimeout = errors.New("the forecast timed out")

// staleSteps is how many steps behind the newest sample may be before history is considered stale.
const staleSteps = 5

// defaultTrainBudget bounds one forecast when the instance sets no budget.
const defaultTrainBudget = 120 * time.Second

// Outcome statuses of a per-algorithm step.
const (
	StatusOK      = "ok"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// PredictOutcome is the result of one algorithm on one metric source.
type PredictOutcome struct {
	Metric       string `json:"metric"`
	AlgorithmID  string `json:"algorithm_id"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Points       int    `json:"points"`
	ModelVersion string `json:"model_version,omitempty"`
	// Covariates and Dropped say what the algorithm read besides the target: the other sources that were used and
	// the ones that could not be supplied (with the reason).
	Covariates []string          `json:"covariates,omitempty"`
	Dropped    map[string]string `json:"dropped_covariates,omitempty"`
}

// boundModel is a validated algorithm run ready to run.
type boundModel struct {
	binding *model.AlgorithmRun
	key     string // the instance name its results are stored under
	def     forecast.Definition
	model   forecast.Model
	params  string // canonical JSON of the normalized parameters
	version string
}

// canonicalParams validates an algorithm's parameters and returns them in canonical JSON.
func canonicalParams(algorithmID, raw string) (map[string]any, string, error) {
	var params map[string]any
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return nil, "", fmt.Errorf("params is not a JSON object: %w", err)
		}
	}
	normalized, err := forecast.Normalize(algorithmID, params)
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(forecast.Canonical(algorithmID, normalized)) // map keys are sorted, so the encoding is canonical
	return normalized, string(data), err
}

// ModelVersion identifies the exact algorithm, implementation and parameters behind a prediction.
func ModelVersion(def forecast.Definition, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("v%s.impl%s+%s", def.Version, def.ImplVersion, hex.EncodeToString(sum[:])[:8])
}

// bind builds the model of a run. Its version changes with the parameters and with whatever else changes the
// model (the sources it reads, the history window), so a prediction or promotion of another version is told apart.
func bind(b *model.AlgorithmRun) (*boundModel, error) {
	def, ok := forecast.Lookup(b.AlgorithmID)
	if !ok {
		return nil, fmt.Errorf("unknown algorithm %q", b.AlgorithmID)
	}
	params, canonical, err := canonicalParams(b.AlgorithmID, b.Params)
	if err != nil {
		return nil, err
	}
	m, err := forecast.Build(b.AlgorithmID, params)
	if err != nil {
		return nil, err
	}
	version := ModelVersion(def, canonical)
	if sig := instanceSignature(b, def); sig != "" {
		version = ModelVersion(def, canonical+"|"+sig)
	}
	return &boundModel{binding: b, key: b.Key(), def: def, model: m, params: canonical, version: version}, nil
}

// budgetFor is the time one forecast of the algorithm may take.
func budgetFor(b *model.AlgorithmRun) time.Duration {
	if b.TrainBudgetSeconds > 0 {
		return time.Duration(b.TrainBudgetSeconds) * time.Second
	}
	return defaultTrainBudget
}

// horizonOf is how many steps ahead the instance forecasts.
func horizonOf(b *model.AlgorithmRun, step time.Duration) int {
	n := int((time.Duration(b.HorizonMinutes)*time.Minute + step - 1) / step)
	if n < 1 {
		n = 1
	}
	return n
}

// Predict runs every enabled algorithm on each of the module's metric sources that is forecast and appends the
// forecasts. A source an algorithm cannot run on (short history, bad parameters, a timeout, an error) is reported and
// skipped; it never blocks the others.
func (s *Service) Predict(ctx context.Context, moduleID uint) ([]PredictOutcome, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runsOf(ctx, m)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: no algorithm is enabled", ErrNotConfigured)
	}
	if len(dataOf(m).SourceKeys()) == 0 {
		return nil, fmt.Errorf("%w: the module has no metric source to forecast", ErrNotConfigured)
	}
	var all []PredictOutcome
	for _, b := range runs {
		out, err := s.predictRun(ctx, b)
		all = append(all, out...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

// predictRun runs one algorithm on the forecast sources of its module.
func (s *Service) predictRun(ctx context.Context, b *model.AlgorithmRun) ([]PredictOutcome, error) {
	keys := b.SourceKeys()
	predictedAt := s.now()
	bm, bindErr := bind(b)
	var outcomes []PredictOutcome
	bundles := map[string]*bundleResult{}
	for _, key := range keys {
		out := PredictOutcome{Metric: key, AlgorithmID: b.Key()}
		if bindErr != nil {
			out.Status, out.Reason = StatusFailed, bindErr.Error()
			s.Telemetry.recordPredict(key, b.Key(), StatusFailed, 0)
			s.recordTraining(ctx, b, nil, out, nil, 0, 0, 0, predictedAt)
			outcomes = append(outcomes, out)
			continue
		}
		out.ModelVersion = bm.version
		covariates := coverOf(b, key, bm.def)
		res, ok := bundles[key]
		if !ok {
			res = s.loadBundle(ctx, b, key, covariates)
			bundles[key] = res
		}
		if res.err != nil {
			out.Status, out.Reason = StatusSkipped, res.err.Error()
			s.Telemetry.recordPredict(key, b.Key(), out.Status, 0)
			s.recordTraining(ctx, b, bm, out, nil, 0, 0, 0, predictedAt)
			outcomes = append(outcomes, out)
			continue
		}
		in, series := res.bundle.In, res.bundle.Series
		if !bm.def.Covariates {
			in.Covariates = nil // an algorithm that cannot read covariates never sees them
		} else {
			out.Covariates, out.Dropped = res.bundle.Used, res.bundle.Dropped
		}
		step := time.Duration(series.StepSeconds) * time.Second
		h := horizonOf(b, step)

		fctx, cancel := context.WithTimeout(ctx, budgetFor(b))
		started := time.Now()
		values, err := bm.model.Forecast(fctx, in, h)
		took := time.Since(started)
		timedOut := errors.Is(fctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		unit := s.unitOf(ctx, b, key)
		switch {
		case timedOut && err != nil:
			out.Status, out.Reason = StatusFailed, fmt.Sprintf("%v after %s", ErrTimeout, took.Round(time.Millisecond))
		case errors.Is(err, forecast.ErrInsufficientHistory):
			out.Status, out.Reason = StatusSkipped, err.Error()
		case err != nil:
			out.Status, out.Reason = StatusFailed, err.Error()
		default:
			rows := make([]model.Prediction, len(values))
			for i, v := range values {
				rows[i] = model.Prediction{
					ModuleID: b.ID, Metric: key, PredictionTime: predictedAt, TargetTime: in.End.Add(time.Duration(i+1) * step),
					AlgorithmID: b.Key(), ModelVersion: bm.version, QuantilePermille: quantilePermille(bm), Value: v, Unit: unit,
				}
			}
			if err := s.Predictions.SavePredictions(ctx, rows); err != nil {
				return outcomes, err
			}
			out.Status, out.Points = StatusOK, len(rows)
		}
		s.Telemetry.recordPredict(key, b.Key(), out.Status, took)
		s.recordTraining(ctx, b, bm, out, &in, step, h, took, predictedAt)
		outcomes = append(outcomes, out)
	}
	return outcomes, nil
}

// coverOf lists the covariates an instance gives the algorithm for one of its sources: its other sources, when the
// algorithm reads covariates at all.
func coverOf(b *model.AlgorithmRun, target string, def forecast.Definition) []string {
	if !def.Covariates {
		return nil
	}
	var out []string
	for _, k := range b.SourceKeys() {
		if k != target {
			out = append(out, k)
		}
	}
	return out
}

// quantilePermille is the quantile a model's forecast represents: 500 for point forecasts, alpha for the
// quantile objective of gbdt.
func quantilePermille(bm *boundModel) int {
	if g, ok := bm.model.(forecast.GBDT); ok && g.Quantile {
		return int(g.Alpha*1000 + 0.5)
	}
	return 500
}

// ValidateParams checks an algorithm's raw JSON parameters and returns their canonical (defaults filled) JSON.
func ValidateParams(algorithmID, raw string) (string, error) {
	_, canonical, err := canonicalParams(algorithmID, raw)
	return canonical, err
}

// RunVersion is the model version an algorithm currently produces for a module: it changes with the algorithm
// implementation, the parameters, the sources and the history window, so a stored prediction of another version
// was made by a different model.
func (s *Service) RunVersion(_ context.Context, b *model.AlgorithmRun) (string, error) {
	bm, err := bind(b)
	if err != nil {
		return "", err
	}
	return bm.version, nil
}

// bundleResult is a loaded input or why it could not be loaded.
type bundleResult struct {
	bundle *inputBundle
	err    error
}

func (s *Service) loadBundle(ctx context.Context, m *model.AlgorithmRun, target string, covariates []string) *bundleResult {
	bundle, err := s.loadInputFor(ctx, m, target, covariates)
	return &bundleResult{bundle: bundle, err: err}
}

// NormalizedParams validates the raw JSON parameters of an algorithm and returns them with the defaults filled in,
// together with their canonical JSON.
func NormalizedParams(algorithmID, raw string) (map[string]any, string, error) {
	return canonicalParams(algorithmID, raw)
}

// recordTraining files the record of one run: the window it read, the parameters and (for a custom algorithm) the
// formula it ran with, how long it took and how it ended. It is the reproducible version of the model, since nothing
// else of the model is kept. A record that cannot be stored is logged and never fails the forecast.
func (s *Service) recordTraining(ctx context.Context, b *model.AlgorithmRun, bm *boundModel, out PredictOutcome, in *forecast.Input, step time.Duration, horizon int, took time.Duration, at time.Time) {
	t := &model.TrainingRun{ModuleID: b.ID, Metric: out.Metric, AlgorithmID: out.AlgorithmID, ModelVersion: out.ModelVersion, Status: out.Status,
		Reason: truncate(out.Reason, 500), Points: out.Points, DurationMs: took.Milliseconds(), PredictionTime: at,
		StepSeconds: int(step / time.Second), HorizonSteps: horizon}
	if bm != nil {
		t.ImplVersion, t.Params = bm.def.ImplVersion, bm.params
		if bm.def.Custom {
			t.Formula = bm.def.Formula
			if schema, err := json.Marshal(bm.def.Params); err == nil {
				t.ParamSchema = string(schema)
			}
		}
	}
	if in != nil {
		t.Samples = len(in.Values)
		if len(in.Values) > 0 {
			start, end := in.TimeAt(0), in.End
			t.WindowStart, t.WindowEnd = &start, &end
		}
	}
	t.Covariates = strings.Join(out.Covariates, ",")
	if len(out.Dropped) > 0 {
		if raw, err := json.Marshal(out.Dropped); err == nil {
			t.Dropped = string(raw)
		}
	}
	if err := s.Predictions.SaveTraining(ctx, t); err != nil {
		log.Printf("forecast: training record of %s/%s: %v", out.AlgorithmID, out.Metric, err)
	}
}
