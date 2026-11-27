package forecastsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The dispatch detail of a module answers the questions between "what is recommended" and "what happened in the cluster": which source decided (the forecasts or the fixed formula), what
// each source says (replicas now, replicas recommended, the difference; the Pod spec now and recommended), whether the result was taken by the module's decision, what the mode of the module
// lets it do, and what the operator did.

// Dispatch states: how far a decision has gone.
const (
	// DispatchNotFollowed: the module follows the forecasts; the fixed formula is shown beside them and decides nothing.
	DispatchNotFollowed = "not_followed"
	// DispatchObserveOnly: the module is in observe mode: calculations are made and shown, nothing is recommended or done.
	DispatchObserveOnly = "observe_only"
	// DispatchUnavailable: the module follows the formula but there is no calculation it can take (none, too old, or not certain); the forecasts decided.
	DispatchUnavailable = "unavailable"
	// DispatchPending: a calculation is made, and no recommendation has taken it yet (the run of the module is the next to).
	DispatchPending = "pending"
	// DispatchRecommended: the recommendation took it and the mode is recommend: it is shown and nothing writes it to the cluster.
	DispatchRecommended = "recommended"
	// DispatchAdmission: the recommendation took it and the mode is admission: the Pod spec is written when a Pod is admitted; the replicas are not scaled by the platform.
	DispatchAdmission = "admission"
	// DispatchWaitingOperator: the mode is auto: the operator scales to the recommendation; it has not reported an action on it yet.
	DispatchWaitingOperator = "waiting_operator"
	// DispatchExecuted: the operator reported an action on the recommendation.
	DispatchExecuted = "executed"
)

// DispatchState says how far the decision of the fixed formula has gone, from what is known: the module (source and mode), the newest calculation and whether the newest recommendation took
// it, and the actions the operator reported on the recommendations that did. It is pure.
func DispatchState(m *model.ForecastModule, rule *model.RuleDecision, followedWhy string, newest *model.Recommendation, executed bool) string {
	switch {
	case !m.FollowsRule():
		return DispatchNotFollowed
	case m.Mode == model.ModeObserve:
		return DispatchObserveOnly
	case rule == nil || followedWhy != "":
		return DispatchUnavailable
	case newest == nil || newest.RuleDecisionID != rule.ID:
		return DispatchPending
	}
	switch m.Mode {
	case model.ModeAdmission:
		return DispatchAdmission
	case model.ModeAuto:
		if executed {
			return DispatchExecuted
		}
		return DispatchWaitingOperator
	}
	return DispatchRecommended
}

// DispatchSide is what one source says about a module: the replicas and the Pod spec it would have.
type DispatchSide struct {
	Replicas    int64 `json:"replicas"`
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
	// Algorithm is the algorithm that decided (for the forecasts) and Score its score.
	Algorithm string  `json:"algorithm,omitempty"`
	Score     float64 `json:"score,omitempty"`
}

// DispatchEffective is the newest recommendation of the module: what is in force as a recommendation, and the sources of its fields.
type DispatchEffective struct {
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	ValidUntil     time.Time `json:"valid_until"`
	Mode           string    `json:"mode"`
	Replicas       int       `json:"replicas"`
	ReplicasSet    bool      `json:"replicas_set"`
	ReplicasSource string    `json:"replicas_source"`
	CPUMilli       int64     `json:"cpu_milli"`
	CPUSource      string    `json:"cpu_source"`
	MemoryBytes    int64     `json:"memory_bytes"`
	MemorySource   string    `json:"memory_source"`
	RuleDecisionID uint      `json:"rule_decision_id"`
	Degraded       bool      `json:"degraded"`
}

// Dispatch is the answer.
type Dispatch struct {
	ModuleID       uint   `json:"module_id"`
	Name           string `json:"name"`
	Mode           string `json:"mode"`
	Enabled        bool   `json:"enabled"`
	DecisionSource string `json:"decision_source"`
	State          string `json:"state"`
	// Why says why a module that follows the formula has no calculation to take.
	Why string `json:"why,omitempty"`
	// Rule is the newest calculation of the fixed formula; Algorithm what the forecasts said in the newest recommendation (nil when none); Effective the newest recommendation.
	Rule      *model.RuleDecision `json:"rule"`
	Algorithm *DispatchSide       `json:"algorithm"`
	Effective *DispatchEffective  `json:"effective"`
	// Executions are the actions the operator reported on the recommendations that took the calculation, newest first.
	Executions []*model.ScalingExecution `json:"executions"`
}

// Dispatch reads the dispatch detail of a module.
func (s *Service) Dispatch(ctx context.Context, moduleID uint) (*Dispatch, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	out := &Dispatch{ModuleID: m.ID, Name: m.Name, Mode: m.Mode, Enabled: m.Enabled, DecisionSource: m.DecisionSource, Executions: []*model.ScalingExecution{}}
	if out.DecisionSource == "" {
		out.DecisionSource = model.DecisionAlgorithm
	}
	if out.Rule, err = s.Rules.Latest(ctx, moduleID); err != nil {
		return nil, err
	}
	newest, err := s.Predictions.LatestRecommendation(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if newest != nil {
		out.Effective = &DispatchEffective{Version: newest.Version, CreatedAt: newest.CreatedAt, ValidUntil: newest.ValidUntil, Mode: newest.Mode, Replicas: newest.Replicas,
			ReplicasSet: newest.ReplicasSet, ReplicasSource: newest.ReplicasSource, CPUMilli: newest.CPUMilli, CPUSource: newest.CPUSource, MemoryBytes: newest.MemoryBytes,
			MemorySource: newest.MemorySource, RuleDecisionID: newest.RuleDecisionID, Degraded: newest.Degraded}
		out.Algorithm = algorithmSide(newest)
	}
	_, out.Why = s.RuleFollowed(ctx, m)
	executed := false
	if out.Rule != nil {
		recs, err := s.Rules.RecommendationsOf(ctx, out.Rule.ID, 20)
		if err != nil {
			return nil, err
		}
		versions := make([]int, 0, len(recs))
		for _, r := range recs {
			versions = append(versions, r.Version)
		}
		if out.Executions, err = s.Rules.ExecutionsFor(ctx, moduleID, versions, 20); err != nil {
			return nil, err
		}
		if out.Executions == nil {
			out.Executions = []*model.ScalingExecution{}
		}
		executed = len(out.Executions) > 0
	}
	// the state is of the formula that is followed now: a module that follows it with no calculation to take has none to be pending on
	rule := out.Rule
	if out.Why != "" {
		rule = nil
	}
	out.State = DispatchState(m, rule, out.Why, newest, executed)
	return out, nil
}

// algorithmSide reads what the forecasts said from the detail of a recommendation: when the rule took a field, the figure the algorithms had is kept in the detail; otherwise the
// recommendation itself is what they said.
func algorithmSide(rec *model.Recommendation) *DispatchSide {
	side := &DispatchSide{Replicas: int64(rec.Replicas), CPUMilli: rec.CPUMilli, MemoryBytes: rec.MemoryBytes, Algorithm: rec.Algorithm, Score: rec.Score}
	var d struct {
		Rule *RuleDetail `json:"rule"`
	}
	if rec.Detail == "" || json.Unmarshal([]byte(rec.Detail), &d) != nil || d.Rule == nil {
		return side
	}
	if rec.ReplicasSource == string(decision.SourceRule) {
		side.Replicas = d.Rule.AlgorithmReplicas
	}
	if rec.CPUSource == string(decision.SourceRule) {
		side.CPUMilli = d.Rule.AlgorithmCPUMilli
	}
	if rec.MemorySource == string(decision.SourceRule) {
		side.MemoryBytes = d.Rule.AlgorithmMemory
	}
	return side
}

// RuleRow is a module with its newest calculation and how far it has gone: a line of the page of the fixed formula.
type RuleRow struct {
	ModuleID       uint                `json:"module_id"`
	Name           string              `json:"name"`
	Namespace      string              `json:"namespace"`
	Workload       string              `json:"workload"`
	Cluster        string              `json:"cluster,omitempty"`
	Mode           string              `json:"mode"`
	Enabled        bool                `json:"enabled"`
	DecisionSource string              `json:"decision_source"`
	State          string              `json:"state"`
	Rule           *model.RuleDecision `json:"rule"`
}

// RuleRows lists every module with its newest calculation of the fixed formula and how far its decision has gone. The state is read from the newest recommendation of each module.
func (s *Service) RuleRows(ctx context.Context) ([]*RuleRow, error) {
	modules, err := s.Modules.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.Rules.LatestOfAll(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.Predictions.LatestRecommendations(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]*RuleRow, 0, len(modules))
	for _, m := range modules {
		rule := rules[m.ID]
		row := &RuleRow{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, Workload: m.Workload, Cluster: m.Cluster, Mode: m.Mode, Enabled: m.Enabled, DecisionSource: m.DecisionSource, Rule: rule}
		if row.DecisionSource == "" {
			row.DecisionSource = model.DecisionAlgorithm
		}
		why := ""
		switch {
		case rule == nil:
			why = "none"
		case now.Sub(rule.CheckedAt) > RuleMaxAge:
			why = "old"
		case !rule.Certain:
			why = "uncertain"
		}
		usable := rule
		if why != "" {
			usable = nil
		}
		// whether the operator acted on it is read for the modules in auto mode only (one query each); the others do not need it
		executed := false
		if m.FollowsRule() && m.Mode == model.ModeAuto && rule != nil && recs[m.ID] != nil && recs[m.ID].RuleDecisionID == rule.ID {
			if list, err := s.Rules.ExecutionsFor(ctx, m.ID, []int{recs[m.ID].Version}, 1); err == nil {
				executed = len(list) > 0
			}
		}
		row.State = DispatchState(m, usable, why, recs[m.ID], executed)
		out = append(out, row)
	}
	return out, nil
}
