package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestASourceStatisticAndAggregationAcceptAnyPercentile(t *testing.T) {
	for _, level := range []string{"p40", "p41", "p42", "p95", "p99.9"} {
		s := MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Stat: level, Aggregation: level}
		assert.NoError(t, s.Validate(), level)
		assert.Equal(t, level, s.Stat)
	}
	for _, bad := range []string{"p0", "p100", "pp", "p99.99999", "p1e1"} {
		s := MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Stat: bad}
		assert.Error(t, s.Validate(), bad)
		s = MetricSource{Name: "n", MappingID: 1, Kind: "custom", Resource: "signal", Aggregation: bad}
		assert.Error(t, s.Validate(), bad)
	}
}
