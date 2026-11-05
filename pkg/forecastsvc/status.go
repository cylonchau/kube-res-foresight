package forecastsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// Why an enabled algorithm has, or has not, a result to show. Every enabled algorithm gets one of these on the result
// pages instead of disappearing from them.
const (
	StatusReady    = "ready"     // forecasts (or evaluations) exist in the range
	StatusNotRun   = "not_run"   // enabled, but no run is recorded for the module and metric yet
	StatusNoResult = "no_result" // it ran, but nothing was forecast or evaluated in the range
)

// AlgorithmStatus is the state of one enabled algorithm for a module metric, taken from the recorded runs.
type AlgorithmStatus struct {
	AlgorithmID string     `json:"algorithm_id"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	// Conservatism is "" for an algorithm that forecasts the expected load and, for one whose forecast is deliberately high ("P90",
	// "peak envelope"), what that is: its curve is a level that is enough most of the time, not what is expected to be used next.
	Conservatism string `json:"conservatism,omitempty"`
}

// AlgorithmStatus explains the latest recorded run of an algorithm for a module metric. has says whether results exist
// in the range the page shows; waiting is what to say when it ran fine but has none.
func (s *Service) AlgorithmStatus(ctx context.Context, moduleID uint, metric, algorithmID string, has bool, waiting string) AlgorithmStatus {
	out := AlgorithmStatus{AlgorithmID: algorithmID}
	runs, _, err := s.Predictions.ListTrainings(ctx, rdb.TrainingFilter{ModuleID: moduleID, Metric: metric, AlgorithmID: algorithmID}, 1, 1)
	if err == nil && len(runs) > 0 {
		last := runs[0]
		at := last.PredictionTime
		out.LastRunAt = &at
		switch {
		case last.Status == model.TrainingFailed && !has:
			out.Status, out.Reason = StatusFailed, last.Reason
		case last.Status == model.TrainingSkipped && !has:
			out.Status, out.Reason = StatusSkipped, last.Reason
		}
	}
	switch {
	case has:
		out.Status = StatusReady
	case out.Status != "":
	case out.LastRunAt == nil:
		out.Status, out.Reason = StatusNotRun, "enabled, but no training run is recorded for this module and metric yet"
	default:
		out.Status, out.Reason = StatusNoResult, waiting
	}
	return out
}

func noForecastsInRange(from, to time.Time) string {
	return fmt.Sprintf("it ran, but made no forecast with a target time between %s and %s", from.UTC().Format("01-02 15:04"), to.UTC().Format("01-02 15:04"))
}
