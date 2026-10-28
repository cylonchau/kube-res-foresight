package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceRequestsAreRoundedUpToReadableSteps(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{{0, 0}, {1, 10}, {417, 420}, {420, 420}, {999, 1000}, {1001, 1050}, {1309, 1350}, {2000, 2000}} {
		assert.Equal(t, tc.want, RoundCPUMilli(tc.in), "cpu %d", tc.in)
	}
	for _, tc := range []struct{ in, want int64 }{
		{0, 0}, {1, 10 * mib}, {10 * mib, 10 * mib}, {10*mib + 1, 20 * mib}, {317 * mib, 320 * mib}, {411 * mib, 420 * mib}, {1023 * mib, 1030 * mib},
		{1024 * mib, 1024 * mib}, {1025 * mib, 1088 * mib}, {3*1024*mib + 5, 3*1024*mib + 64*mib},
	} {
		assert.Equal(t, tc.want, RoundMemoryBytes(tc.in), "memory %d", tc.in)
	}
	for _, v := range []int64{1, 123456789, 5 << 30} {
		assert.GreaterOrEqual(t, RoundMemoryBytes(v), v, "never below what was computed")
		assert.Zero(t, RoundMemoryBytes(v)%mib, "whole MiB")
	}
}

func algorithmField(v int64) decision.Field {
	return decision.Field{Set: true, Value: v, Source: decision.SourceAlgorithm, Reason: "forecast"}
}

func TestThePodSpecIsKeptUntilTheForecastMovesFarEnough(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	changed := now.Add(-2 * time.Hour)
	const ratio, hold = 0.2, 24 * time.Hour

	// within the ratio: kept, whichever way it moved
	f, held, why := pinOne(algorithmField(1100), 1000, true, 0, 0, now, &changed, ratio, hold)
	assert.True(t, held)
	assert.EqualValues(t, 1000, f.Value)
	assert.Contains(t, f.Reason, "kept")
	assert.Contains(t, why, "within 20%")
	f, held, _ = pinOne(algorithmField(900), 1000, true, 0, 0, now, &changed, ratio, hold)
	assert.True(t, held)
	assert.EqualValues(t, 1000, f.Value)

	// an increase past the ratio is taken at once: a workload that needs more is not made to wait
	f, held, _ = pinOne(algorithmField(1300), 1000, true, 0, 0, now, &changed, ratio, hold)
	assert.False(t, held)
	assert.EqualValues(t, 1300, f.Value)

	// a decrease past the ratio waits for the spec to have stood for the hold time
	f, held, why = pinOne(algorithmField(600), 1000, true, 0, 0, now, &changed, ratio, hold)
	assert.True(t, held)
	assert.EqualValues(t, 1000, f.Value)
	assert.Contains(t, why, "hold")
	old := now.Add(-25 * time.Hour)
	f, held, _ = pinOne(algorithmField(600), 1000, true, 0, 0, now, &old, ratio, hold)
	assert.False(t, held)
	assert.EqualValues(t, 600, f.Value, "after the hold time the lower spec is taken")

	// overrides and protection act at once, and a spec that was itself an override does not hold the forecast back
	for _, source := range []decision.Source{decision.SourceFixedPeriod, decision.SourceBypass, decision.SourceFallback} {
		f, held, _ = pinOne(decision.Field{Set: true, Value: 1050, Source: source}, 1000, true, 0, 0, now, &changed, ratio, hold)
		assert.False(t, held, string(source))
		assert.EqualValues(t, 1050, f.Value)
	}
	floored := algorithmField(1050)
	floored.Floored = true
	f, held, _ = pinOne(floored, 1000, true, 0, 0, now, &changed, ratio, hold)
	assert.False(t, held, "the OOM protection is not held back")
	f, held, _ = pinOne(algorithmField(1050), 1000, false, 0, 0, now, &changed, ratio, hold)
	assert.False(t, held, "the spec in force came from a fixed period: the forecast takes over when it ends")
	assert.EqualValues(t, 1050, f.Value)
	f, held, _ = pinOne(algorithmField(1050), 0, true, 0, 0, now, &changed, ratio, hold)
	assert.False(t, held, "no spec yet: the first one is taken")
	f, held, _ = pinOne(algorithmField(1050), 1000, true, 1100, 0, now, &changed, ratio, hold)
	assert.False(t, held, "a spec outside the module's bounds is not kept")
	assert.EqualValues(t, 1050, f.Value)
	assert.False(t, func() bool {
		_, h, _ := pinOne(decision.Field{}, 1000, true, 0, 0, now, &changed, ratio, hold)
		return h
	}(), "a field that is not set is not pinned")
}

// The replica count follows the forecast every cycle while the Pod resources stay: two snapshots with a different load carry the
// same spec and the same spec version, and a spec that has to change gets the next version.
func TestScalingDecisionsDoNotRewriteThePodResources(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	first, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.True(t, first.CPUSet)
	require.NotNil(t, detail.ResourceSpec)
	assert.Equal(t, 1, first.ResourceVersion)
	assert.True(t, detail.ResourceSpec.Changed, "the first spec is a new one")
	assert.EqualValues(t, 0, first.CPUMilli%10, "a readable step")
	computed := first.CPUMilli

	// the forecast moves a little: the spec stays, and the snapshot says it was kept
	require.NoError(t, f.db.Model(&model.Recommendation{}).Where("id = ?", first.ID).Update("cpu_milli", computed+computed/20).Error)
	second, detail2, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, computed+computed/20, second.CPUMilli, "kept at the spec in force, not rewritten to the new forecast")
	assert.Equal(t, 1, second.ResourceVersion, "the same version of the spec")
	assert.True(t, second.ResourceHeld)
	assert.True(t, detail2.ResourceSpec.Held)
	assert.False(t, detail2.ResourceSpec.Changed)
	assert.Contains(t, detail2.CPU.Reason, "Pod spec is kept")

	// the spec in force is far below what is now forecast: it is raised at once and is a new version
	require.NoError(t, f.db.Model(&model.Recommendation{}).Where("id = ?", second.ID).Update("cpu_milli", computed/3).Error)
	third, detail3, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, computed, third.CPUMilli)
	assert.Equal(t, 2, third.ResourceVersion)
	assert.True(t, detail3.ResourceSpec.Changed)
	assert.False(t, third.ResourceHeld)
	assert.NotNil(t, third.ResourceChangedAt)
}
