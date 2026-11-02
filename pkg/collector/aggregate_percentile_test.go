package collector

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAStepCanBeMergedAtAnyPercentile(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for how, want := range map[string]float64{"avg": 5.5, "max": 10, "min": 1, "last": 10, "sum": 55, "p40": 4.6, "p41": 4.69, "p95": 9.55, "p99.9": 9.991} {
		assert.InDelta(t, want, aggregate(values, how), 1e-9, how)
	}
	// an unknown name is still the average, as it always was
	assert.InDelta(t, 5.5, aggregate(values, "median-ish"), 1e-9)
}
