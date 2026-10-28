package decision

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func byKey(indicators []Indicator) map[string]Indicator {
	out := map[string]Indicator{}
	for _, i := range indicators {
		out[i.Key] = i
	}
	return out
}

func TestTheDelayIndicatorsPassWhenCapacityIsAskedForInTime(t *testing.T) {
	got := byKey(DelayIndicators(DelayInputs{Timely: 18, Late: 1, Unneeded: 2, LateSeconds: []float64{30}, Scaled: 21, Emergency: 2,
		PeakUnder: []float64{0.02, 0.05, 0.04, 0.08, 0.1, 0.03}, ReachSeconds: 600, LeadSeconds: 300, DecisionWaitSeconds: 90}))
	assert.Equal(t, IndicatorPass, got["timely_rate"].Verdict)
	assert.InDelta(t, 18.0/19, got["timely_rate"].Value, 1e-9)
	assert.Equal(t, IndicatorPass, got["late_p90_seconds"].Verdict)
	assert.Equal(t, IndicatorPass, got["emergency_share"].Verdict)
	assert.Equal(t, IndicatorPass, got["peak_under_p90"].Verdict)
	assert.Equal(t, IndicatorPass, got["reach_over_lead"].Verdict)
	assert.InDelta(t, 2, got["reach_over_lead"].Value, 1e-9)
	assert.Equal(t, IndicatorPass, got["decision_wait_seconds"].Verdict)
	assert.Equal(t, IndicatorPass, DelayVerdict(DelayIndicators(DelayInputs{Timely: 18, Late: 1, LateSeconds: []float64{30}, Scaled: 19, ReachSeconds: 600, LeadSeconds: 300, DecisionWaitSeconds: 90})))
}

func TestTheDelayIndicatorsFailWhenTheForecastIsBehindTheLoad(t *testing.T) {
	in := DelayInputs{Timely: 5, Late: 5, LateSeconds: []float64{40, 80, 120, 200, 240}, Scaled: 12, Emergency: 6,
		PeakUnder: []float64{0.3, 0.4, 0.2, 0.5, 0.35}, ReachSeconds: 120, LeadSeconds: 300, DecisionWaitSeconds: 300}
	got := byKey(DelayIndicators(in))
	assert.Equal(t, IndicatorFail, got["timely_rate"].Verdict, "half of the scale-ups were late")
	assert.Equal(t, IndicatorFail, got["late_p90_seconds"].Verdict)
	assert.Greater(t, got["late_p90_seconds"].Value, 200.0)
	assert.Equal(t, IndicatorFail, got["emergency_share"].Verdict, "half of them were asked for by the live load")
	assert.Equal(t, IndicatorFail, got["peak_under_p90"].Verdict)
	assert.Equal(t, IndicatorFail, got["reach_over_lead"].Verdict, "the forecast ends before the lead does")
	assert.Equal(t, IndicatorFail, got["decision_wait_seconds"].Verdict)
	assert.Equal(t, IndicatorFail, DelayVerdict(DelayIndicators(in)))
}

func TestTheDelayIndicatorsSayInsufficientInsteadOfPassingOnNothing(t *testing.T) {
	got := byKey(DelayIndicators(DelayInputs{Timely: 2, Scaled: 2}))
	assert.Equal(t, IndicatorInsufficient, got["timely_rate"].Verdict, "two judged scale-ups prove nothing")
	assert.Equal(t, IndicatorInsufficient, got["late_p90_seconds"].Verdict)
	assert.Equal(t, IndicatorInsufficient, got["emergency_share"].Verdict)
	assert.Equal(t, IndicatorInsufficient, got["peak_under_p90"].Verdict)
	assert.Equal(t, IndicatorInsufficient, got["reach_over_lead"].Verdict, "no decision to read the reach from")
	assert.Equal(t, IndicatorInsufficient, DelayVerdict(DelayIndicators(DelayInputs{})))

	// a failing indicator decides even when the rest cannot be judged
	assert.Equal(t, IndicatorFail, DelayVerdict(DelayIndicators(DelayInputs{ReachSeconds: 60, LeadSeconds: 300})))
}

// A figure that does not exist is not NaN in the answer: that cannot be written as JSON.
func TestAnIndicatorWithoutAFigureHasNoNaN(t *testing.T) {
	for _, i := range DelayIndicators(DelayInputs{}) {
		assert.False(t, i.Value != i.Value, i.Key)
	}
}
