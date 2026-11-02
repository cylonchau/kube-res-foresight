package forecastsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// DefaultDelayHours is the window the delay report looks at when none is asked for.
const DefaultDelayHours = 24

// DelayReport is the acceptance of the prediction delay of one module over a window: the indicators with their targets and the verdict of
// all of them. It is measured on what happened to the scale-ups the platform decided (see decision.DelayIndicators), not on a backtest.
type DelayReport struct {
	ModuleID uint      `json:"module_id"`
	Since    time.Time `json:"since"`
	Hours    int       `json:"hours"`
	// Scaled is how many scale-ups were decided in the window and Judged how many of them have been judged.
	Scaled int `json:"scaled"`
	Judged int `json:"judged"`
	// Algorithm is the algorithm in use, whose evaluated runs the peak indicator is made of.
	Algorithm  string               `json:"algorithm,omitempty"`
	Verdict    string               `json:"verdict"`
	Indicators []decision.Indicator `json:"indicators"`
}

// DelayReport judges how late the module asks for capacity over the last hours (DefaultDelayHours when 0).
func (s *Service) DelayReport(ctx context.Context, moduleID uint, hours int) (*DelayReport, error) {
	if _, err := s.Modules.Get(ctx, moduleID); err != nil {
		return nil, err
	}
	if hours <= 0 {
		hours = DefaultDelayHours
	}
	now := s.now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	rep := &DelayReport{ModuleID: moduleID, Since: since, Hours: hours}
	in := decision.DelayInputs{DecisionWaitSeconds: s.aheadSettings(ctx).DecisionWait, Percentile: s.Percentile(ctx, timing.JudgePercentile)}

	outcomes, err := s.Predictions.OutcomesSince(ctx, moduleID, since)
	if err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		rep.Scaled++
		in.Scaled++
		if o.Emergency {
			in.Emergency++
		}
		if !o.Final {
			continue
		}
		rep.Judged++
		switch o.Verdict {
		case decision.VerdictTimely:
			in.Timely++
		case decision.VerdictLate:
			in.Late++
			in.LateSeconds = append(in.LateSeconds, o.LateSeconds)
		case decision.VerdictUnneeded:
			in.Unneeded++
		}
	}

	if rec, err := s.Predictions.LatestRecommendation(ctx, moduleID); err == nil && rec != nil && rec.Detail != "" {
		var detail RecommendDetail
		if json.Unmarshal([]byte(rec.Detail), &detail) == nil && detail.Ahead != nil {
			in.ReachSeconds = float64(detail.Ahead.Reach.EffectiveMinutes) * 60
			in.LeadSeconds = detail.Ahead.Lead.Seconds
		}
	}

	if run, err := s.RunStates.RunState(ctx, moduleID); err == nil && run != nil && run.Selected != "" {
		rep.Algorithm = run.Selected
		evals, err := s.Predictions.AllEvaluations(ctx, rdb.EvaluationFilter{ModuleID: moduleID, AlgorithmID: run.Selected, Since: &since})
		if err != nil {
			return nil, err
		}
		for _, e := range evals {
			if e.Points > 0 {
				in.PeakUnder = append(in.PeakUnder, e.PeakUnder)
			}
		}
	}

	rep.Indicators = decision.DelayIndicators(in)
	rep.Verdict = decision.DelayVerdict(rep.Indicators)
	return rep, nil
}
