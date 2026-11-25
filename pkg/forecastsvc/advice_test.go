package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The advice is the analysis of the dashboard in a short form: the same figures, a verdict for each resource, the replicas, and the module that has the most to change first.
func TestTheAdviceIsTheAnalysisOfTheDashboardInAShortForm(t *testing.T) {
	f := opFixture(t)
	f.withRequests(t, 1.5)
	f.withReplicas()
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)

	advice, err := f.svc.ResourceAdvice(ctx, 0, 5)
	require.NoError(t, err)
	require.Equal(t, 1, advice.Read, "the module that is enabled")
	require.Len(t, advice.Modules, 1)
	m := advice.Modules[0]
	assert.Equal(t, f.module.ID, m.ModuleID)
	d, err := f.svc.OverprovisionDetail(ctx, f.module.ID, 0, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NotNil(t, d.Recommend.CPU)
	require.NotNil(t, m.CPU)
	assert.InDelta(t, d.Recommend.CPU.Request, m.CPU.Request, 1e-9, "the Requests of the dashboard")
	assert.InDelta(t, d.Recommend.CPU.Suggested, m.CPU.SuggestedRequest, 1e-9, "the recommendation of the dashboard")
	assert.InDelta(t, d.Recommend.CPU.Usage.P95, m.CPU.P95, 1e-9)
	assert.Equal(t, string(d.Recommend.CPU.Status), m.CPU.Status)
	require.NotNil(t, m.Replicas)
	assert.Equal(t, d.Recommend.ReplicaPlan.Needed, m.Replicas.Recommended, "the replicas of the dashboard, without the pace")
	assert.NotEmpty(t, advice.Basis)

	// one module by its id, and a limit that cuts the list
	one, err := f.svc.ResourceAdvice(ctx, f.module.ID, 1)
	require.NoError(t, err)
	assert.Len(t, one.Modules, 1)
	_, err = f.svc.ResourceAdvice(ctx, 99999, 1)
	assert.Error(t, err, "a module that does not exist")
}

// A module that is short or limited comes before one that has something to give back, which comes before one with nothing to change; the figures are the engine's.
func TestTheAdviceOrdersTheModulesThatNeedALookFirst(t *testing.T) {
	detail := func(status string, request, suggested float64) *DetailOverprovision {
		res := &overprovision.Result{Resource: overprovision.CPU, Request: request, Suggested: suggested, Status: status, Usage: overprovision.Usage{P95: 1}}
		return &DetailOverprovision{ModuleOverprovision: &ModuleOverprovision{Name: "m", Recommend: &WindowOverprovision{Label: "7d", CPU: res}}}
	}
	short := adviceOf(detail(overprovision.StatusUnder, 1, 2))
	over := adviceOf(detail(overprovision.StatusOver, 4, 1))
	ok := adviceOf(detail(overprovision.StatusOK, 4, 4))
	assert.Greater(t, short.Score, over.Score)
	assert.Greater(t, over.Score, ok.Score)
	assert.Zero(t, ok.Score)
	assert.InDelta(t, 2.0, short.Score, 1e-9, "1 + the share it is short of")
	assert.Equal(t, "7d", short.Period)
	assert.Equal(t, 1.0, short.CPU.P95)
}
