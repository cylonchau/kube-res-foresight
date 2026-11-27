package forecastsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// P90, P95 and P99 of the window are the level the samples stood above for that share of the time, so one spike does not decide.
func TestATriggerCanReduceItsWindowToAPercentile(t *testing.T) {
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i + 1) // 1..100
	}
	assert.InDelta(t, 90.1, reduceTrigger(model.TriggerStatP90, values), 0.01)
	assert.InDelta(t, 95.05, reduceTrigger(model.TriggerStatP95, values), 0.01)
	assert.InDelta(t, 99.01, reduceTrigger(model.TriggerStatP99, values), 0.01)
	assert.EqualValues(t, 7, reduceTrigger(model.TriggerStatP95, []float64{7}))
	spiky := append(make([]float64, 0, 21), 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 500)
	assert.EqualValues(t, 500, reduceTrigger(model.TriggerStatMax, spiky))
	assert.Less(t, reduceTrigger(model.TriggerStatP90, spiky), 100.0, "the spike does not move P90")
}
