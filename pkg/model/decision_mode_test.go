package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func validModule() *ForecastModule {
	return &ForecastModule{Name: "api", Namespace: "prod", Workload: "api", Mode: ModeObserve}
}

// At most one of the three strategies is on, and the forecast and the recommendation mode are on only while none is, and not both.
func TestTheDecisionModesAreExclusive(t *testing.T) {
	m := validModule()
	assert.NoError(t, m.Validate())
	assert.Equal(t, DecisionModeNone, m.DecisionMode())

	m.PredictEnabled, m.PredictMode = true, PredictDynamic
	assert.NoError(t, m.Validate())
	assert.Equal(t, DecisionModeAlgo, m.DecisionMode())

	m.PeriodsEnabled = true
	assert.ErrorContains(t, m.Validate(), "forecast can be on only while")
	assert.Equal(t, StrategyPeriods, m.ActiveStrategy(), "a strategy has the word over the forecast")
	assert.False(t, m.Predicts(), "and the forecast takes no part while it is on")

	m = validModule()
	m.DecisionSource, m.TriggersEnabled = DecisionRule, true
	assert.ErrorContains(t, m.Validate(), "recommendation mode can be on only while")
	assert.False(t, m.FollowsRule())

	m = validModule()
	m.PeriodsEnabled, m.TriggersEnabled = true, true
	assert.ErrorContains(t, m.Validate(), "only one of")

	m = validModule()
	m.FixedReplicasEnabled = true
	assert.ErrorContains(t, m.Validate(), "replica count above 0", "a fixed count with no count is no strategy")
	m.FixedReplicas = 4
	assert.NoError(t, m.Validate())
	assert.Equal(t, 4, m.ActiveFixedReplicas())
	assert.Equal(t, ModeStrategyPrefix+StrategyFixed, m.DecisionMode())

	// the forecast and the recommendation mode are not on together either: the recommendation mode takes the module off the forecast
	m = validModule()
	m.DecisionSource, m.PredictEnabled, m.PredictMode = DecisionRule, true, PredictDynamic
	assert.NoError(t, m.Validate())
	assert.Equal(t, DecisionModeRule, m.DecisionMode())
	assert.False(t, m.Predicts())
	assert.True(t, m.FollowsRule())
}
