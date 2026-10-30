package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The curves of the chart are the algorithms that run for the module now. An algorithm that was disabled keeps its forecasts in the
// stored history, but it is no longer one of the module's algorithms: its curve is gone from the chart, and a request that names it
// does not bring it back.
func TestTheComparisonShowsOnlyTheAlgorithmsThatRunForTheModuleNow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	opts := CompareOptions{Metric: "cpu", From: testNow.Add(-time.Hour), To: testNow.Add(time.Hour), WithPredictions: true}
	ids := func(c *Compare) map[string]bool {
		out := map[string]bool{}
		for _, a := range c.Algorithms {
			out[a.AlgorithmID] = true
		}
		return out
	}
	both, err := f.svc.Compare(ctx, f.module.ID, opts)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true, "ewma": true}, ids(both))

	cfg, err := f.svc.Algorithms.Get(ctx, "ewma")
	require.NoError(t, err)
	cfg.Enabled = false
	require.NoError(t, f.svc.Algorithms.Save(ctx, cfg))

	after, err := f.svc.Compare(ctx, f.module.ID, opts)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true}, ids(after), "the disabled algorithm is not a curve any more")
	for _, s := range after.Statuses {
		assert.NotEqual(t, "ewma", s.AlgorithmID)
	}

	opts.Algorithms = []string{"ewma", "naive"}
	named, err := f.svc.Compare(ctx, f.module.ID, opts)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"naive": true}, ids(named), "naming it does not bring it back")
}
