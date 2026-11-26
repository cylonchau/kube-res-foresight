package forecastsvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// the step is the window over the points, rounded up to a whole multiple of the step of the mapping, and never finer than the mapping
func TestSubqueryStepFollowsThePointsParameter(t *testing.T) {
	const day = 24 * time.Hour
	def := timing.PrometheusSubqueryPoints.Default
	assert.Equal(t, 1500, def)

	cases := []struct {
		name    string
		mapping time.Duration
		window  time.Duration
		points  int
		want    time.Duration
	}{
		{"7 days at 1500 points is 403 s, so 420 s", time.Minute, 7 * day, 1500, 420 * time.Second},
		{"1 hour needs 2.4 s, the mapping step of 60 s stays", time.Minute, time.Hour, 1500, time.Minute},
		{"30 days at 1500 points", time.Minute, 30 * day, 1500, 29 * time.Minute},
		{"more points make a finer step", time.Minute, 7 * day, 3000, 240 * time.Second},
		{"the most points cannot go under the mapping step", time.Minute, 7 * day, 11000, time.Minute},
		{"fewer points make a coarser step", time.Minute, 7 * day, 500, 1260 * time.Second},
		{"a mapping step of 15 s lets 1 hour be 15 s", 15 * time.Second, time.Hour, 1500, 15 * time.Second},
		{"no mapping step is a minute", 0, time.Hour, 1500, time.Minute},
		{"no points is the default", time.Minute, 7 * day, 0, 420 * time.Second},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, subqueryStep(c.mapping, c.window, c.points), c.name)
	}
}

func TestSubqueryPointsIsASystemParameterWithARange(t *testing.T) {
	p := timing.PrometheusSubqueryPoints
	assert.Equal(t, "forecast.prometheus_subquery_points", p.Key)
	assert.Equal(t, 100, p.Clamp(1), "a value under the range is brought to its lowest")
	assert.Equal(t, 11000, p.Clamp(1000000))
	assert.Equal(t, 1500, p.Parse(""), "an empty value is the default")
	assert.Equal(t, 2000, p.Parse("2000"))
	found := false
	for _, q := range timing.All() {
		found = found || q.Key == p.Key
	}
	assert.True(t, found, "it is in the list the system parameters are seeded from")
}
