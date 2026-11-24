package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func coefficientModule() *ForecastModule {
	return &ForecastModule{Name: "api", Namespace: "prod", Workload: "api"}
}

func TestTheCoefficientsOfACalculationTakeTheirDefaultsAndKeepTheirRanges(t *testing.T) {
	m := coefficientModule()
	require.NoError(t, m.Validate())
	assert.Equal(t, 1.0, m.OOMCalcWorkingSetRatio)
	assert.Equal(t, 1.0, m.OOMCalcLimitRatio)
	assert.Equal(t, 1.0, m.OOMCalcHistoryRatio)
	assert.Equal(t, 0.0, m.FloorRatio, "the floor coefficient is off until it is set")
	assert.Equal(t, DefaultMaxShortfall, m.MaxShortfall)
	assert.Equal(t, 1.0, m.CapacityReserve(), "an unset capacity reserve is 1: nothing is held back")
	assert.InDelta(t, 1.0, m.MaxUncertainty(), 1e-9)

	m.TargetUtilization = 0.8
	m.MaxShortfall = 0.75
	require.NoError(t, m.Validate())
	assert.Equal(t, 0.8, m.CapacityReserve())
	assert.InDelta(t, 3.0, m.MaxUncertainty(), 1e-9, "a shortfall cap of 0.75 scales a forecast by at most 4")

	for name, mutate := range map[string]func(*ForecastModule){
		"oom_calc_working_set_ratio": func(m *ForecastModule) { m.OOMCalcWorkingSetRatio = 9 },
		"oom_calc_limit_ratio":       func(m *ForecastModule) { m.OOMCalcLimitRatio = 0.01 },
		"oom_calc_history_ratio":     func(m *ForecastModule) { m.OOMCalcHistoryRatio = -2 },
		"floor_ratio":                func(m *ForecastModule) { m.FloorRatio = 1.5 },
		"max_shortfall":              func(m *ForecastModule) { m.MaxShortfall = 0.95 },
		"target_utilization":         func(m *ForecastModule) { m.TargetUtilization = 1.2 },
	} {
		bad := coefficientModule()
		mutate(bad)
		err := bad.Validate()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), name)
	}
}

func TestTheCoefficientsOfTheRecommendationFollowTheSystemAtZeroAndKeepTheirRange(t *testing.T) {
	m := coefficientModule()
	require.NoError(t, m.Validate())
	assert.Zero(t, m.RecP95Target, "0 follows the system setting")
	m.RecP95Target, m.RecCPULimitHeadroom = 0.9, 0.25
	require.NoError(t, m.Validate())
	m.RecP95Target = 0.05
	assert.ErrorContains(t, m.Validate(), "rec_p95_target")
	m.RecP95Target, m.RecCPULimitHeadroom = 0, 5
	assert.ErrorContains(t, m.Validate(), "rec_cpu_limit_headroom")
}
