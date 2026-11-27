package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// ruleFixture is opFixture with the table of the calculations and a module the formula can read: 4 Pods, each asking 4 cores and 16 GiB.
func ruleFixture(t *testing.T) *fixture {
	t.Helper()
	f := opFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.RuleDecision{}))
	f.svc.Rules = rdb.NewRuleDecisionDAO(f.db)
	f.withRequests(t, 1.5)
	f.withReplicas()
	_, err := f.collect(context.Background())
	require.NoError(t, err)
	return f
}

// The calculation of the fixed formula carries the replicas now and needed, the Pod spec now and recommended and the percentiles it was made of; made again with the same result it is
// not stored again, it is checked again.
func TestTheFixedFormulaIsStoredOnceAndCheckedAgain(t *testing.T) {
	f := ruleFixture(t)
	ctx := context.Background()
	first, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotZero(t, first.ID)
	assert.Equal(t, f.module.ID, first.ModuleID)
	assert.True(t, first.ReplicasSet, "the formula gave a replica count")
	assert.Equal(t, 4, first.CurrentReplicas, "the replicas the cluster has")
	assert.Equal(t, first.RecommendedReplicas-first.CurrentReplicas, first.ReplicasDelta)
	assert.True(t, first.CPUSet)
	assert.Greater(t, first.CPURecommendedMilli, int64(0))
	assert.Equal(t, int64(4000), first.CPUCurrentMilli, "16 cores over 4 Pods")
	assert.Greater(t, first.CPUP95, 0.0)
	assert.Greater(t, first.CPUP99, first.CPUP90-1e-9)
	assert.NotEmpty(t, first.Formula, "the formulas are written out with their numbers")

	second, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "the same result is not stored again")
	list, total, err := f.svc.Rules.List(ctx, f.module.ID, 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, list, 1)
}

// A module that follows the fixed formula takes its replicas and Pod spec from it; the forecasts are still made and kept in the detail beside it.
func TestAModuleThatFollowsTheFixedFormulaTakesItsReplicasAndPodSpecFromIt(t *testing.T) {
	f := ruleFixture(t)
	ctx := context.Background()
	rule, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	require.True(t, rule.Certain, "the calculation is certain: %s", rule.Caution)
	require.True(t, rule.ReplicasSet)

	f.setModule(t, func(m *model.ForecastModule) {
		m.Mode = model.ModeRecommend
		m.DecisionSource = model.DecisionRule
		m.MinReplicas, m.MaxReplicas = 1, 100
	})
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, string(decision.SourceRule), rec.ReplicasSource, "the replicas follow the formula")
	assert.Equal(t, rule.RecommendedReplicas, rec.Replicas)
	assert.Equal(t, rule.ID, rec.RuleDecisionID, "the way from the recommendation to the calculation")
	require.NotNil(t, detail.Rule)
	assert.Equal(t, model.DecisionRule, detail.Rule.Source)
	assert.True(t, detail.Rule.Replicas)
	assert.Empty(t, detail.Rule.Unavailable)
	if rule.CPUSet {
		assert.Equal(t, string(decision.SourceRule), rec.CPUSource)
	}

	// the module follows the forecasts again: nothing is taken from the formula
	f.setModule(t, func(m *model.ForecastModule) { m.DecisionSource = model.DecisionAlgorithm })
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.NotEqual(t, string(decision.SourceRule), rec.ReplicasSource)
	assert.Zero(t, rec.RuleDecisionID)
	assert.Equal(t, model.DecisionAlgorithm, detail.Rule.Source)
}

// A calculation that is old, or not certain, is not followed: the forecasts decide and the detail says why.
func TestACalculationThatIsOldOrNotCertainIsNotFollowed(t *testing.T) {
	f := ruleFixture(t)
	ctx := context.Background()
	rule, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	f.setModule(t, func(m *model.ForecastModule) {
		m.Mode = model.ModeRecommend
		m.DecisionSource = model.DecisionRule
		m.MinReplicas, m.MaxReplicas = 1, 100
	})

	require.NoError(t, f.db.Model(&model.RuleDecision{}).Where("id = ?", rule.ID).Update("checked_at", f.svc.now().Add(-RuleMaxAge-time.Minute)).Error)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.NotEqual(t, string(decision.SourceRule), rec.ReplicasSource)
	assert.Contains(t, detail.Rule.Unavailable, "older than")

	require.NoError(t, f.db.Model(&model.RuleDecision{}).Where("id = ?", rule.ID).Updates(map[string]any{"checked_at": f.svc.now(), "certain": false, "caution": "low confidence"}).Error)
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.NotEqual(t, string(decision.SourceRule), rec.ReplicasSource)
	assert.Contains(t, detail.Rule.Unavailable, "not certain: low confidence")
}

// What outranks a computed value outranks the formula too: a bypass, a fixed period and a fixed count keep their replicas.
func TestTheFixedFormulaDoesNotTakeWhatAFixedValueOutranks(t *testing.T) {
	rd := &model.RuleDecision{ID: 7, ReplicasSet: true, RecommendedReplicas: 9, CurrentReplicas: 4, CPUSet: true, CPURecommendedMilli: 1500, MemorySet: true, MemoryRecommendedBytes: 2 << 30, Period: "7d"}
	for _, src := range []decision.Source{decision.SourceBypass, decision.SourceFixedPeriod, decision.SourceFixedCount} {
		rec := decision.Recommendation{Replicas: decision.Field{Set: true, Value: 3, Source: src}}
		d := &RuleDetail{}
		assert.False(t, takeRule(&rec, rd, d), string(src))
		assert.EqualValues(t, 3, rec.Replicas.Value)
	}
	rec := decision.Recommendation{
		Replicas:    decision.Field{Set: true, Value: 6, Source: decision.SourceAlgorithm},
		CPUMilli:    decision.Field{Set: true, Value: 1000, Source: decision.SourceAlgorithm},
		MemoryBytes: decision.Field{Set: true, Value: 1 << 30, Source: decision.SourceFixedPeriod},
	}
	d := &RuleDetail{}
	require.True(t, takeRule(&rec, rd, d))
	takeRuleResources(&rec, rd, d)
	assert.EqualValues(t, 9, rec.Replicas.Value)
	assert.Equal(t, decision.SourceRule, rec.Replicas.Source)
	assert.EqualValues(t, 6, d.AlgorithmReplicas, "what the forecasts said is kept beside it")
	assert.EqualValues(t, 1500, rec.CPUMilli.Value)
	assert.EqualValues(t, 1000, d.AlgorithmCPUMilli)
	assert.EqualValues(t, 1<<30, rec.MemoryBytes.Value, "a fixed period keeps its memory")
	assert.False(t, d.Memory)

	none := decision.Recommendation{Replicas: decision.Field{Set: true, Value: 6, Source: decision.SourceAlgorithm}}
	assert.False(t, takeRule(&none, nil, &RuleDetail{}), "no calculation: nothing is taken")
	assert.False(t, takeRule(&none, &model.RuleDecision{}, &RuleDetail{}), "a calculation with no replicas: nothing is taken")
}

// How far a decision has gone is told from the module, the calculation and the recommendation that took it.
func TestTheDispatchStateTellsHowFarADecisionHasGone(t *testing.T) {
	rule := &model.RuleDecision{ID: 5, Certain: true}
	took := &model.Recommendation{RuleDecisionID: 5}
	other := &model.Recommendation{RuleDecisionID: 4}
	module := func(source, mode string) *model.ForecastModule {
		return &model.ForecastModule{DecisionSource: source, Mode: mode}
	}
	for name, tc := range map[string]struct {
		m        *model.ForecastModule
		rule     *model.RuleDecision
		why      string
		rec      *model.Recommendation
		executed bool
		want     string
	}{
		"the forecasts are followed":     {module(model.DecisionAlgorithm, model.ModeAuto), rule, "", took, true, DispatchNotFollowed},
		"nothing set is the forecasts":   {module("", model.ModeAuto), rule, "", took, true, DispatchNotFollowed},
		"observe only records":           {module(model.DecisionRule, model.ModeObserve), rule, "", took, false, DispatchObserveOnly},
		"no calculation to take":         {module(model.DecisionRule, model.ModeAuto), nil, "none", nil, false, DispatchUnavailable},
		"a calculation that is not used": {module(model.DecisionRule, model.ModeAuto), rule, "not certain", took, false, DispatchUnavailable},
		"made and not taken yet":         {module(model.DecisionRule, model.ModeAuto), rule, "", other, false, DispatchPending},
		"no recommendation yet":          {module(model.DecisionRule, model.ModeRecommend), rule, "", nil, false, DispatchPending},
		"recommend mode shows it":        {module(model.DecisionRule, model.ModeRecommend), rule, "", took, false, DispatchRecommended},
		"admission mode writes the spec": {module(model.DecisionRule, model.ModeAdmission), rule, "", took, false, DispatchAdmission},
		"auto waits for the operator":    {module(model.DecisionRule, model.ModeAuto), rule, "", took, false, DispatchWaitingOperator},
		"auto and the operator acted":    {module(model.DecisionRule, model.ModeAuto), rule, "", took, true, DispatchExecuted},
	} {
		assert.Equal(t, tc.want, DispatchState(tc.m, tc.rule, tc.why, tc.rec, tc.executed), name)
	}
}

// The detail reads the execution records of the recommendations that took the calculation, and tells the forecasts' figures beside the formula's.
func TestTheDispatchDetailLinksTheCalculationToWhatTheOperatorDid(t *testing.T) {
	f := ruleFixture(t)
	ctx := context.Background()
	rule, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	f.setModule(t, func(m *model.ForecastModule) {
		m.Mode = model.ModeAuto
		m.DecisionSource = model.DecisionRule
		m.MinReplicas, m.MaxReplicas = 1, 100
	})
	rec, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.Equal(t, rule.ID, rec.RuleDecisionID)

	got, err := f.svc.Dispatch(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, DispatchWaitingOperator, got.State, "taken, and the operator has not acted yet")
	require.NotNil(t, got.Effective)
	assert.Equal(t, rec.Version, got.Effective.Version)
	assert.Equal(t, string(decision.SourceRule), got.Effective.ReplicasSource)
	require.NotNil(t, got.Rule)
	assert.Equal(t, rule.ID, got.Rule.ID)
	require.NotNil(t, got.Algorithm, "what the forecasts said is shown beside the formula")
	assert.Empty(t, got.Executions)

	// the operator reports an action on that recommendation
	require.NoError(t, f.db.Create(&model.ScalingExecution{ModuleID: f.module.ID, Namespace: "ns", Scaler: "operator", TargetKind: "Deployment", TargetName: "api",
		Current: 4, Ready: 4, Desired: rec.Replicas, Action: "scale_up", Result: "success", DecisionVersion: int64(rec.Version)}).Error)
	got, err = f.svc.Dispatch(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, DispatchExecuted, got.State)
	require.Len(t, got.Executions, 1)
	assert.EqualValues(t, rec.Version, got.Executions[0].DecisionVersion)

	rows, err := f.svc.RuleRows(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, DispatchExecuted, rows[0].State)
	assert.Equal(t, model.DecisionRule, rows[0].DecisionSource)
}

// A fixed replica count outranks the formula for the replicas; the Pod spec still follows it, and the recommendation says it considered the calculation.
func TestAFixedReplicaCountOutranksTheFormulaForTheReplicasOnly(t *testing.T) {
	f := ruleFixture(t)
	ctx := context.Background()
	rule, err := f.svc.RuleDecide(ctx, f.module.ID)
	require.NoError(t, err)
	require.True(t, rule.CPUSet)
	f.setModule(t, func(m *model.ForecastModule) {
		m.Mode = model.ModeRecommend
		m.DecisionSource = model.DecisionRule
		m.MinReplicas, m.MaxReplicas = 1, 100
		m.FixedReplicas, m.FixedReplicasEnabled = 5, true
	})
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, string(decision.SourceFixedCount), rec.ReplicasSource)
	assert.Equal(t, 5, rec.Replicas)
	assert.False(t, detail.Rule.Replicas, "the replicas were not taken from the formula")
	assert.True(t, detail.Rule.CPU, "the Pod spec was")
	assert.Equal(t, string(decision.SourceRule), rec.CPUSource)
	assert.Equal(t, rule.ID, rec.RuleDecisionID, "the calculation was considered")
	got, err := f.svc.Dispatch(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, DispatchRecommended, got.State)
}
