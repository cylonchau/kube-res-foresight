package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
)

// The fixed formula is the second source of a decision (the first is the forecasts of the algorithms). It is the analysis of the resource dashboard: from the P90, P95 and P99 of the
// real use over the period of the recommendation it works out the replicas the module needs and the CPU and memory a Pod should request. Nothing is forecast. A calculation is stored
// (model.RuleDecision); a module that follows the rule takes the newest certain one as its replicas and Pod spec, and then everything else works as it does for the forecasts: the bounds,
// the periods, the triggers, the pace of the operator, and what the mode of the module allows.

const (
	// RuleMaxAge is how old the calculation a module follows may be: older, the module does not follow it (the figures may not be the use of now).
	RuleMaxAge = 30 * time.Minute
	// ruleReferenceEvery is how often the calculation is made for a module that does not follow it (to show next to the forecasts, not to decide with).
	ruleReferenceEvery = 6 * time.Hour
	// ruleRetention is how long the calculations are kept.
	ruleRetention = 14 * 24 * time.Hour
	ruleParallel  = 4
)

// ruleRowOf is the calculation of a module out of its analysis, as the dashboard shows it. It is pure.
func ruleRowOf(m *model.ForecastModule, d *DetailOverprovision, at time.Time) *model.RuleDecision {
	row := &model.RuleDecision{CreatedAt: at, CheckedAt: at, ModuleID: m.ID, Mode: m.Mode}
	if d == nil || d.ModuleOverprovision == nil {
		row.Caution = "no analysis"
		return row
	}
	advice := adviceOf(d)
	rec := d.Recommend
	if rec == nil && len(d.Windows) > 0 {
		rec = d.Windows[0]
	}
	if rec == nil {
		row.Caution = "no analysis"
		return row
	}
	row.Period, row.Partial = rec.Label, d.Partial
	var formulas []string
	certain, read := true, false
	resource := func(r *overprovision.Result, a *AdviceResource) (set bool, current, recommended, limit int64, p90, p95, p99 float64, status string) {
		if r == nil || a == nil {
			return
		}
		read = true
		if !a.Certain {
			certain = false
			if row.Caution == "" {
				row.Caution = r.Resource + ": " + a.Caution
			}
		}
		if r.Formula != "" {
			formulas = append(formulas, r.Resource+": "+r.Formula)
		}
		scale := 1000.0
		if r.Resource == overprovision.Memory {
			scale = 1
		}
		return r.Suggested > 0, int64(math.Round(r.Request * scale)), int64(math.Round(r.Suggested * scale)), int64(math.Round(r.SuggestedLimit * scale)),
			r.Usage.P90, r.Usage.P95, r.Usage.P99, string(r.Status)
	}
	row.CPUSet, row.CPUCurrentMilli, row.CPURecommendedMilli, row.CPULimitMilli, row.CPUP90, row.CPUP95, row.CPUP99, row.CPUStatus = resource(rec.CPU, advice.CPU)
	row.MemorySet, row.MemoryCurrentBytes, row.MemoryRecommendedBytes, row.MemoryLimitBytes, row.MemoryP90, row.MemoryP95, row.MemoryP99, row.MemoryStatus = resource(rec.Memory, advice.Memory)
	if rec.CPU != nil {
		row.Confidence = string(rec.CPU.Confidence)
	} else if rec.Memory != nil {
		row.Confidence = string(rec.Memory.Confidence)
	}
	var reasons []string
	for _, r := range []*overprovision.Result{rec.CPU, rec.Memory} {
		if r != nil {
			reasons = append(reasons, r.Reasons...)
		}
	}
	if plan := rec.ReplicaPlan; plan != nil && plan.Needed > 0 {
		row.ReplicasSet = true
		row.CurrentReplicas, row.RecommendedReplicas, row.ReplicasDelta = plan.Current, plan.Needed, plan.Needed-plan.Current
		if plan.Current == 0 {
			row.ReplicasDelta = 0 // the workload's replicas are not known: there is no difference to speak of
		}
		row.Direction, row.Binding, row.ThisRound, row.Caliber = plan.Direction, plan.Binding, plan.ThisRound, plan.Caliber
		row.ReplicasFormula = clip(plan.Formula, 600)
		reasons = append(reasons, plan.Reasons...)
	}
	if !read {
		certain = false
		if row.Caution == "" {
			row.Caution = "no analysis"
		}
	}
	if d.Partial {
		certain = false
		if row.Caution == "" || !strings.HasPrefix(row.Caution, "partial") {
			row.Caution = "partial: some figures of the module could not be read"
		}
	}
	row.Certain = certain
	row.Reasons = clip(overprovision.JoinReasons(reasons), 400)
	row.Formula = clip(strings.Join(formulas, "\n"), 1200)
	return row
}

// sameRule says a calculation came out as the one before it: nothing a decision is made of changed.
func sameRule(a, b *model.RuleDecision) bool {
	return a.ReplicasSet == b.ReplicasSet && a.CurrentReplicas == b.CurrentReplicas && a.RecommendedReplicas == b.RecommendedReplicas &&
		a.CPUSet == b.CPUSet && a.CPURecommendedMilli == b.CPURecommendedMilli && a.CPUCurrentMilli == b.CPUCurrentMilli &&
		a.MemorySet == b.MemorySet && a.MemoryRecommendedBytes == b.MemoryRecommendedBytes && a.MemoryCurrentBytes == b.MemoryCurrentBytes &&
		a.Certain == b.Certain && a.Mode == b.Mode && a.Caliber == b.Caliber
}

// RuleDecide makes the calculation of the fixed formula for a module now, from Prometheus (the same reading as the dashboard) and the Pod spec from Kubernetes, and stores it. A calculation
// that came out as the newest stored one is not stored again: the newest one is marked as checked now.
func (s *Service) RuleDecide(ctx context.Context, moduleID uint) (*model.RuleDecision, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	d, err := s.OverprovisionDetail(ctx, moduleID, 0, time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	row := ruleRowOf(m, d, s.now())
	if last, err := s.Rules.Latest(ctx, moduleID); err != nil {
		return nil, err
	} else if last != nil && sameRule(last, row) {
		if err := s.Rules.Touch(ctx, last.ID, row.CheckedAt); err != nil {
			return nil, err
		}
		last.CheckedAt = row.CheckedAt
		return last, nil
	}
	if err := s.Rules.Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// RuleDecideAll makes the calculation for every enabled module that follows the rule, and for the others when their newest one is old (they show it next to the forecasts). A module that
// fails does not stop the others. It returns how many were made.
func (s *Service) RuleDecideAll(ctx context.Context) (int, error) {
	modules, err := s.Modules.ListEnabled(ctx)
	if err != nil {
		return 0, err
	}
	latest, err := s.Rules.LatestOfAll(ctx)
	if err != nil {
		return 0, err
	}
	now := s.now()
	var due []*model.ForecastModule
	for _, m := range modules {
		last := latest[m.ID]
		if m.FollowsRule() || last == nil || now.Sub(last.CheckedAt) >= ruleReferenceEvery {
			due = append(due, m)
		}
	}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		done   int
		failed []string
	)
	slots := make(chan struct{}, ruleParallel)
	for _, m := range due {
		slots <- struct{}{}
		wg.Add(1)
		go func(m *model.ForecastModule) {
			defer wg.Done()
			defer func() { <-slots }()
			readCtx, cancel := context.WithTimeout(ctx, adviceModuleTimeout)
			defer cancel()
			_, err := s.RuleDecide(readCtx, m.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", m.Name, err))
				return
			}
			done++
		}(m)
	}
	wg.Wait()
	if _, err := s.Rules.Prune(ctx, now.Add(-ruleRetention), 5000); err != nil {
		failed = append(failed, "pruning: "+err.Error())
	}
	if len(failed) > 0 {
		return done, errors.New(strings.Join(failed, "; "))
	}
	return done, nil
}

// RuleFollowed is the calculation a module that follows the rule takes its decision from: the newest one, when it is certain, was checked within RuleMaxAge and says something. When
// there is none, why not (for the detail of the recommendation).
func (s *Service) RuleFollowed(ctx context.Context, m *model.ForecastModule) (*model.RuleDecision, string) {
	if !m.FollowsRule() {
		return nil, ""
	}
	last, err := s.Rules.Latest(ctx, m.ID)
	switch {
	case err != nil:
		return nil, "the calculation could not be read: " + err.Error()
	case last == nil:
		return nil, "no calculation of the fixed formula has been made for the module yet"
	case s.now().Sub(last.CheckedAt) > RuleMaxAge:
		return nil, fmt.Sprintf("the newest calculation of the fixed formula is %s old, older than %s", s.now().Sub(last.CheckedAt).Round(time.Minute), RuleMaxAge)
	case !last.Certain:
		return nil, "the newest calculation of the fixed formula is not certain: " + last.Caution
	}
	return last, ""
}
