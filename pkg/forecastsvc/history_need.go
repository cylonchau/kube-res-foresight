package forecastsvc

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const (
	// needMarginPercent is the room kept over the shortest history an algorithm accepts: a few steps of the range are always
	// missing or interpolated, and an algorithm that is given exactly its minimum would be skipped by one missing sample.
	needMarginPercent = 150
	// maxExtendHours is how far the history range is extended for the algorithms: an algorithm that needs more than this is
	// left to say so (it is skipped with the number it needs), the range is not grown without a limit.
	maxExtendHours = 24 * 30
)

// stepsNeeded is the shortest history in steps that the algorithm of an instance accepts for its forecast period; 0 for an
// instance that has no algorithm (the module as a data owner).
func stepsNeeded(b *model.AlgorithmRun, step time.Duration) int {
	bm, err := bind(b)
	if err != nil {
		return 0
	}
	return forecast.MinSamplesFor(bm.model, horizonOf(b, step))
}

// lookbackFor is the history range, in hours, an instance needs: the one the module sets, or longer when the algorithm
// needs more steps than that range holds. The module's lookback and training window are what the user wants to spend on
// the data; they are never allowed to starve an algorithm that is enabled, because the algorithm is then skipped with
// "insufficient history" on a module whose data is fine, and that is only cured by editing the module by hand.
func lookbackFor(configured, need int, step time.Duration) int {
	if need <= 0 || step <= 0 {
		return configured
	}
	hours := int((time.Duration(need*needMarginPercent/100)*step + time.Hour - 1) / time.Hour)
	if hours > maxExtendHours || hours <= configured {
		return configured
	}
	return hours
}

// windowFor is the training window in steps: the one the module sets (0 = the whole range), never shorter than the
// algorithm needs.
func windowFor(configured, need int) int {
	if configured > 0 && configured < need {
		return need
	}
	return configured
}

// collectLookback is the history range, in hours, to collect and keep for a module: what its algorithms need, so that
// the stored history holds what they train on. runs may be nil (then it is the module's own lookback).
func collectLookback(m *model.AlgorithmRun, runs []*model.AlgorithmRun, step time.Duration) int {
	need := m.MinPoints
	for _, run := range runs {
		if n := stepsNeeded(run, step); n > need {
			need = n
		}
	}
	return lookbackFor(m.LookbackHours, need, step)
}

// runsForCollect lists the enabled algorithms of a module for sizing its history range; a failure leaves the module's own
// lookback (collecting must not stop because the algorithms could not be listed).
func (s *Service) runsForCollect(ctx context.Context, m *model.ForecastModule) []*model.AlgorithmRun {
	runs, err := s.runsOf(ctx, m)
	if err != nil {
		return nil
	}
	return runs
}
