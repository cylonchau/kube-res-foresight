package forecastsvc

import (
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/stretchr/testify/assert"
)

func resource(request, p95, p99 float64, enough, idle bool, surplus float64) *ResourceCost {
	return &ResourceCost{Waste: cost.Waste{Request: request, Usage: cost.Usage{Points: 500, P95: p95, P99: p99}, Utilization: p95 / request, Idle: idle, Enough: enough, Surplus: surplus}}
}

func TestTheVerdictRestsOnTheUtilizationAndSaysWhyOrWhyThereIsNone(t *testing.T) {
	const below, above = 0.5, 0.85
	cases := []struct {
		name string
		rc   *ResourceCost
		want string
	}{
		{"idle beats everything", resource(1, 0.05, 0.08, true, true, 0.4), VerdictIdle},
		{"under the limit", resource(1, 0.3, 0.4, true, false, 0.5), VerdictOverprovisioned},
		{"between the limits", resource(1, 0.6, 0.7, true, false, 0), VerdictOK},
		{"above the tight limit", resource(1, 0.9, 0.95, true, false, 0), VerdictTight},
		{"not enough evidence says unknown and why", &ResourceCost{Waste: cost.Waste{Request: 1, Note: cost.ReasonTooFewSamples}}, VerdictUnknown},
	}
	for _, c := range cases {
		f := finding(c.rc, below, above)
		assert.Equal(t, c.want, f.Verdict, c.name)
		assert.NotEmpty(t, f.Why, c.name)
	}
	assert.Equal(t, cost.ReasonTooFewSamples, finding(&ResourceCost{Waste: cost.Waste{Request: 1, Note: cost.ReasonTooFewSamples}}, below, above).Why)
	assert.InDelta(t, 0.5, finding(resource(2, 0.6, 0.7, true, false, 1), below, above).SurplusShare, 1e-9, "the surplus is a share of the request")
	assert.Nil(t, finding(nil, below, above), "a resource the module does not have has no finding")
}
