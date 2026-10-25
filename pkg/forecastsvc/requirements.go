package forecastsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/model"
)

// Requirement says whether the dataset of a module can feed an algorithm: how much history the algorithm needs on the
// grid of the source, and how much the module's history range holds at that grid.
type Requirement struct {
	AlgorithmID string `json:"algorithm_id"`
	Metric      string `json:"metric"`
	// StepSeconds is the grid of the source's dataset.
	StepSeconds int `json:"step_seconds"`
	// MinSamples is the shortest history the configured algorithm accepts, in steps; RequiredSeconds the same as time.
	MinSamples      int `json:"min_samples"`
	RequiredSeconds int `json:"required_seconds"`
	// AvailableSteps is how many steps the history range of the module can hold at that grid.
	AvailableSteps int `json:"available_steps"`
	// ModuleMinPoints is the module's own floor (min_points).
	ModuleMinPoints int `json:"module_min_points"`
	// Horizon is how many steps the forecast covers.
	HorizonSteps int    `json:"horizon_steps"`
	OK           bool   `json:"ok"`
	Reason       string `json:"reason,omitempty"`
}

// Requirements lists, for every enabled algorithm and every forecast source of a module, the history the algorithm
// needs at the sampling step of the source and whether the history range of the module can hold it. The need is not a
// fixed number: it comes from the algorithm as configured (a seasonal baseline needs a full season, a recurrent network
// its lags plus a training margin, a formula its own minimum) and grows with a finer step.
func (s *Service) Requirements(ctx context.Context, moduleID uint) ([]Requirement, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	runs, err := s.runsOf(ctx, m)
	if err != nil {
		return nil, err
	}
	data := dataOf(m)
	var out []Requirement
	for _, key := range data.SourceKeys() {
		setup, setupErr := s.setup(ctx, data, key)
		for _, run := range runs {
			req := Requirement{AlgorithmID: run.Key(), Metric: key, ModuleMinPoints: m.MinPoints}
			if setupErr != nil {
				req.Reason = setupErr.Error()
				out = append(out, req)
				continue
			}
			step := setup.step
			req.StepSeconds = int(step / time.Second)
			req.AvailableSteps = int(time.Duration(m.LookbackHours) * time.Hour / step)
			if m.WindowSteps > 0 && m.WindowSteps < req.AvailableSteps {
				req.AvailableSteps = m.WindowSteps // the algorithm only sees the training window
			}
			req.HorizonSteps = horizonOf(run, step)
			bm, err := bind(run)
			if err != nil {
				req.Reason = err.Error()
				out = append(out, req)
				continue
			}
			req.MinSamples = bm.model.MinSamples()
			req.RequiredSeconds = req.MinSamples * req.StepSeconds
			need := max(req.MinSamples, m.MinPoints)
			switch {
			case req.AvailableSteps < need:
				req.Reason = fmt.Sprintf("needs %d steps of %ds, the history range or training window holds %d: extend lookback_hours to at least %d (and window_steps to at least %d) or sample more coarsely",
					need, req.StepSeconds, req.AvailableSteps, (need*req.StepSeconds+3599)/3600, need)
			default:
				req.OK = true
			}
			out = append(out, req)
		}
	}
	return out, nil
}

// runSlack is how early a scheduled run may come and still count as due, so a task that fires a few seconds before
// the interval has passed does not skip a whole interval.
const runSlack = 10 * time.Second

// Due reports whether the scheduler should collect and forecast the module now: always when the module sets no run
// interval, otherwise when the newest run is at least one interval old.
func (s *Service) Due(ctx context.Context, m *model.ForecastModule) (bool, error) {
	if m.RunIntervalMinutes <= 0 {
		return true, nil
	}
	last, ok, err := s.Predictions.LatestTrainingAt(ctx, m.ID)
	if err != nil || !ok {
		return true, err
	}
	return s.now().Sub(last)+runSlack >= time.Duration(m.RunIntervalMinutes)*time.Minute, nil
}
