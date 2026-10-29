package forecastsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// tuneBudget bounds one fine-tune request; a fine-tune is slow (tens of seconds on a CPU) and runs in the background.
const tuneBudget = 30 * time.Minute

// seriesKey names a series at a model service: the module, the source and the algorithm instance, so two instances of
// the service (or two modules) never share a fine-tuned copy.
func seriesKey(b *model.AlgorithmRun, source string) string {
	return fmt.Sprintf("m%d/%s/%s", b.ID, source, b.Key())
}

// keepTuned is the platform's side of fine-tuning a model service. When the algorithm of the run is fine-tunable and
// the administrator asked for it (finetune_interval_hours > 0) it
//
//   - names the series in the forecast, so the service answers from the copy it tuned on this series;
//   - starts a fine-tune in the background, with the saved fine-tuning parameters, when the service holds no copy or the
//     copy is older than the interval.
//
// The forecast never waits for the fine-tune: until it is done the service answers from the previous copy, or from the
// pretrained model. A fine-tune that fails is recorded and tried again by the next forecast.
func (s *Service) keepTuned(ctx context.Context, b *model.AlgorithmRun, bm *boundModel, source string, in *forecast.Input, horizon int, step time.Duration, at time.Time) {
	t, ok := forecast.TunableOf(bm.model)
	if !ok || !t.Tuning().Enabled() {
		return
	}
	set := t.Tuning()
	in.Series = seriesKey(b, source)
	if len(in.Values) < set.MinPoints {
		return
	}
	if _, busy := s.tunings.LoadOrStore(in.Series, true); busy {
		return
	}
	snapshot := *in
	snapshot.Values = append([]float64(nil), in.Values...)
	work := func() {
		defer s.tunings.Delete(snapshot.Series)
		s.tuneIfStale(b, bm, t, source, snapshot, horizon, step, at)
	}
	if s.TuneInline {
		work()
		return
	}
	go work()
}

func (s *Service) tuneIfStale(b *model.AlgorithmRun, bm *boundModel, t forecast.Tunable, source string, in forecast.Input, horizon int, step time.Duration, at time.Time) {
	set := t.Tuning()
	ctx, cancel := context.WithTimeout(context.Background(), tuneBudget)
	defer cancel()
	when, held, err := t.TunedAt(ctx, in.Series)
	if err == nil && held && at.Sub(when) < time.Duration(set.IntervalHours)*time.Hour {
		return // the copy is fresh
	}
	out := PredictOutcome{Metric: source, AlgorithmID: b.Key(), ModelVersion: bm.version}
	started := time.Now()
	if err != nil {
		out.Status, out.Reason = StatusFailed, "fine-tune not started: "+err.Error()
	} else if res, err := t.Tune(ctx, in.Series, in.Values, horizon); err != nil {
		out.Status, out.Reason = StatusFailed, "fine-tune failed: "+err.Error()
	} else {
		out.Status, out.Points = StatusOK, res.Points
		out.Reason = fmt.Sprintf("fine-tuned (%s, %d steps, learning rate %g, batch %d, context %d) on %d points in %.0f s",
			set.Mode, set.Steps, set.LearningRate, set.BatchSize, set.ContextLength, len(in.Values), res.Seconds)
	}
	s.recordTraining(ctx, b, bm, out, &in, step, horizon, time.Since(started), at)
}
