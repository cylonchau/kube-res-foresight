package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// Errors that make a metric unusable for forecasting; the recommendation turns them into fallback reasons.
var (
	// ErrInsufficientData means the history is too sparse or too broken to train on: too many missing steps or
	// too little of it is one contiguous run.
	ErrInsufficientData = errors.New("insufficient or low quality history")
)

// inputBundle is the history one algorithm instance trains on for one source: the target and the covariates (the
// other sources the instance takes part in) that could be supplied.
type inputBundle struct {
	In     forecast.Input
	Series *model.MetricSeries
	// Used are the covariates that went into In; Dropped maps a covariate that could not be supplied to why.
	Used    []string
	Dropped map[string]string
}

// checkQuality refuses a history that is too sparse to train on. The ratios are measured on the observed span
// (first to last sample), so a young module with a short history is not penalised for the days it has not lived.
func checkQuality(m *model.AlgorithmRun, metric string, present, usable, spanSteps int) error {
	if spanSteps <= 0 || present <= 0 {
		return nil
	}
	missing := 1 - float64(present)/float64(spanSteps)
	if m.MaxMissingRatio > 0 && missing > m.MaxMissingRatio+1e-9 {
		return fmt.Errorf("%w: %s misses %.0f%% of its steps (limit %.0f%%)", ErrInsufficientData, metric, missing*100, m.MaxMissingRatio*100)
	}
	coverage := float64(usable) / float64(spanSteps)
	if m.FallbackMinHistoryRatio > 0 && coverage < m.FallbackMinHistoryRatio-1e-9 {
		return fmt.Errorf("%w: only %.0f%% of the observed %s history is one contiguous usable run (minimum %.0f%%)", ErrInsufficientData, coverage*100, metric, m.FallbackMinHistoryRatio*100)
	}
	return nil
}

// loadTarget reads the stored history of a source as a gap-free grid ending at the newest sample, over the instance
// lookback.
func (s *Service) loadTarget(ctx context.Context, m *model.AlgorithmRun, metric string) (forecast.Input, *model.MetricSeries, error) {
	series, err := s.Samples.Series(ctx, m.ID, metric)
	if err != nil {
		return forecast.Input{}, nil, fmt.Errorf("%w: no collected history for %s", ErrNotConfigured, metric)
	}
	step := time.Duration(series.StepSeconds) * time.Second
	now := s.now()
	to := floorToStep(now, step)
	// the range and the window are never shorter than the algorithm of the instance needs (see lookbackFor)
	need := max(stepsNeeded(m, step), m.MinPoints)
	hours := lookbackFor(m.LookbackHours, need, step)
	from := floorToStep(to.Add(-time.Duration(hours)*time.Hour), step)
	samples, err := s.Samples.Range(ctx, series.ID, from, to)
	if err != nil {
		return forecast.Input{}, nil, err
	}
	points := make([]collector.Point, len(samples))
	for i, sm := range samples {
		points[i] = collector.Point{Time: time.Unix(sm.TS, 0).UTC(), Value: sm.Value}
	}
	fill := model.MissingInterpolate
	if src := m.Source(metric); src != nil && src.Missing != "" {
		fill = src.Missing
	}
	in, _, ok := collector.ToInput(points, from, to, step, m.MaxFillSteps, fill)
	if !ok {
		return forecast.Input{}, series, fmt.Errorf("%w: no stored samples for %s", ErrNotConfigured, metric)
	}
	if m.MinPoints > 0 && len(in.Values) < m.MinPoints {
		return forecast.Input{}, series, fmt.Errorf("%w: %s has %d usable points, the module needs at least %d", ErrInsufficientData, metric, len(in.Values), m.MinPoints)
	}
	if lag := to.Sub(in.End); lag > staleSteps*step {
		return forecast.Input{}, series, fmt.Errorf("%w: newest %s sample is %s old", ErrStale, metric, lag.Round(time.Second))
	}
	if len(samples) > 1 {
		span := int((samples[len(samples)-1].TS-samples[0].TS)/int64(series.StepSeconds)) + 1
		if err := checkQuality(m, metric, len(samples), len(in.Values), span); err != nil {
			return forecast.Input{}, series, err
		}
	}
	// the training window: the newest steps of the history range (the quality above is judged on the whole range)
	if window := windowFor(m.WindowSteps, need); window > 0 && len(in.Values) > window {
		in.Values = in.Values[len(in.Values)-window:]
	}
	return in, series, nil
}

// alignCovariate lays a covariate on the grid of the target: each target step takes the newest covariate sample at
// or before it (a covariate may be scraped at another resolution), a few missing steps are bridged and the
// result must be complete, or the covariate is reported unavailable.
func (s *Service) alignCovariate(ctx context.Context, m *model.AlgorithmRun, key string, in forecast.Input) ([]float64, error) {
	series, err := s.Samples.Series(ctx, m.ID, key)
	if err != nil {
		return nil, fmt.Errorf("no collected history for %s", key)
	}
	step := time.Duration(series.StepSeconds) * time.Second
	start := in.TimeAt(0)
	samples, err := s.Samples.Range(ctx, series.ID, start.Add(-3*step), in.End)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s has no samples in the training window", key)
	}
	out := make([]float64, len(in.Values))
	j := -1
	missing := 0
	for i := range out {
		t := in.TimeAt(i).Unix()
		for j+1 < len(samples) && samples[j+1].TS <= t {
			j++
		}
		if j >= 0 && t-samples[j].TS <= int64(3*step/time.Second) {
			out[i] = samples[j].Value
		} else {
			out[i] = math.NaN()
			missing++
		}
	}
	if float64(missing) > 0.1*float64(len(out)) {
		return nil, fmt.Errorf("%s misses %d of %d steps of the training window", key, missing, len(out))
	}
	if math.IsNaN(out[len(out)-1]) {
		return nil, fmt.Errorf("%s has no recent sample", key)
	}
	last := math.NaN()
	for i := range out { // carry the previous value over the few missing steps
		if math.IsNaN(out[i]) {
			out[i] = last
		} else {
			last = out[i]
		}
	}
	for i := range out { // leading steps before the first sample take the first value
		if !math.IsNaN(out[i]) {
			for k := 0; k < i; k++ {
				out[k] = out[i]
			}
			break
		}
	}
	return out, nil
}

// loadInputFor reads what one algorithm instance trains on for one of its sources: the source over the
// instance's history window plus, for an algorithm that reads covariates, the other sources of the instance. A
// covariate that cannot be supplied is dropped and the algorithm runs on what is left; the bundle says which.
func (s *Service) loadInputFor(ctx context.Context, m *model.AlgorithmRun, target string, covariates []string) (*inputBundle, error) {
	in, series, err := s.loadTarget(ctx, m, target)
	if err != nil {
		return nil, err
	}
	bundle := &inputBundle{In: in, Series: series, Dropped: map[string]string{}}
	values := map[string][]float64{}
	for _, key := range covariates {
		if key == target {
			continue
		}
		aligned, err := s.alignCovariate(ctx, m, key, in)
		if err != nil {
			bundle.Dropped[key] = err.Error()
			continue
		}
		values[key] = aligned
		bundle.Used = append(bundle.Used, key)
	}
	sort.Strings(bundle.Used)
	if len(values) > 0 {
		bundle.In.Covariates = values
	}
	return bundle, nil
}

// instanceSignature is the part of an algorithm instance, besides its parameters, that changes the model: which
// sources it reads and how much history it trains on. It is part of the model version, so a promotion of an
// instance never carries over to a changed one.
func instanceSignature(b *model.AlgorithmRun, def forecast.Definition) string {
	var parts []string
	if def.Covariates {
		keys := b.SourceKeys()
		sort.Strings(keys)
		if len(keys) > 1 {
			parts = append(parts, "src:"+strings.Join(keys, ","))
		}
	}
	if cfg := shortTermOf(b, def); cfg.Enabled() {
		parts = append(parts, fmt.Sprintf("st:%g/%g/%g/%g", cfg.RecentMinutes, cfg.Trigger, cfg.FadeMinutes, cfg.DownShare))
	}
	if b.WindowSteps > 0 {
		parts = append(parts, fmt.Sprintf("win:%d", b.WindowSteps))
	}
	if b.LookbackHours != 720 {
		parts = append(parts, fmt.Sprintf("lb:%d", b.LookbackHours))
	}
	return strings.Join(parts, "|")
}
