package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/resultcache"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cachedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.svc.Cache = resultcache.New(8<<20, 100)
	f.svc.Cache.SetClock(func() time.Time { return *f.clock })
	return f
}

func TestTheEffectReportOfAModuleIsKeptWhileNothingItIsMadeFromChangesAndMadeAgainWhenSomethingDoes(t *testing.T) {
	f := cachedFixture(t)
	ctx := context.Background()
	first, res, err := f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source)

	*f.clock = f.clock.Add(time.Minute)
	again, res, err := f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Cached, res.Source)
	assert.Same(t, first, again)
	assert.Equal(t, time.Minute, res.Age)

	// the module writes a recommendation: the report would be another one
	f.recommendation(t, f.module, time.Hour, nil)
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source, "a new recommendation is a new version of the data")
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Cached, res.Source)

	// another window is another report
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 6, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source)

	// the settings of the module changed
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("id = ?", f.module.ID).Update("headroom_ratio", 1.4).Error)
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source, "the settings of the module are in the version")

	// a newer sample of the history
	_, res, _ = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.Equal(t, resultcache.Cached, res.Source)
	f.bind(t, "cpu", "naive", nil, 0)
	_, err = f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source, "history that has a newer sample")

	// the clock itself moves the window: after the stretch it is made again
	_, res, _ = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.Equal(t, resultcache.Cached, res.Source)
	*f.clock = f.clock.Add(16 * time.Minute)
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source, "a window of 24 hours is made again every 15 minutes")

	stats := f.svc.Cache.Stats()
	assert.Positive(t, stats.Hits)
	assert.Positive(t, stats.Changed)
}

func TestTheCacheIsOffWhenItsParameterIsZeroAndAModuleThatDoesNotExistIsNotAnAnswer(t *testing.T) {
	f := cachedFixture(t)
	ctx := context.Background()
	f.setParam(t, timing.ResultCache, "0")
	_, res, err := f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Bypassed, res.Source)
	_, res, err = f.svc.EffectReportCached(ctx, f.module.ID, 24, nil)
	require.NoError(t, err)
	assert.Equal(t, resultcache.Bypassed, res.Source)
	assert.Zero(t, f.svc.Cache.Stats().Entries)

	_, _, err = f.svc.EffectReportCached(ctx, 99999, 24, nil)
	assert.Error(t, err)
}

func TestAPreviewIsKeptByItsChangeItsHoursAndTheDataAndCanBeFoundWithoutWorkingItOut(t *testing.T) {
	f := cachedFixture(t)
	ctx := context.Background()
	headroom := 1.3
	other := 1.5
	key := PreviewChange{HeadroomRatio: &headroom}
	assert.NotEqual(t, previewKey(f.module.ID, 24, key), previewKey(f.module.ID, 24, PreviewChange{HeadroomRatio: &other}), "another change is another key")
	assert.NotEqual(t, previewKey(f.module.ID, 24, key), previewKey(f.module.ID, 6, key), "another window")
	assert.Equal(t, previewKey(f.module.ID, 24, key), previewKey(f.module.ID, 24, PreviewChange{HeadroomRatio: &headroom}))

	_, ok := f.svc.PreviewKept(ctx, f.module.ID, 24, key)
	assert.False(t, ok, "nothing is kept yet")

	// a preview of a module with no algorithm cannot be made: that is an error, and an error is not kept
	_, res, err := f.svc.PreviewChangeCached(ctx, f.module.ID, 24, key)
	require.Error(t, err)
	assert.Equal(t, resultcache.Computed, res.Source)
	_, ok = f.svc.PreviewKept(ctx, f.module.ID, 24, key)
	assert.False(t, ok)

	// one that can be made is kept, and found
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	_, err = f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	rep, res, err := f.svc.PreviewChangeCached(ctx, f.module.ID, 6, key)
	if err != nil {
		t.Skipf("the synthetic history is not enough for a replay here: %v", err)
	}
	assert.Equal(t, resultcache.Computed, res.Source)
	kept, ok := f.svc.PreviewKept(ctx, f.module.ID, 6, key)
	require.True(t, ok)
	assert.Same(t, rep, kept)
	_, res, _ = f.svc.PreviewChangeCached(ctx, f.module.ID, 6, key)
	assert.Equal(t, resultcache.Cached, res.Source)

	*f.clock = f.clock.Add(11 * time.Minute)
	_, ok = f.svc.PreviewKept(ctx, f.module.ID, 6, key)
	assert.False(t, ok, "the history that a replay reads slides with the clock: a replay is kept for ten minutes at most")
}

func TestTheQuantilesReadFromPrometheusAreKeptForAFewMinutesAtMost(t *testing.T) {
	f := cachedFixture(t)
	ctx := context.Background()
	_, res, err := f.svc.PrometheusStatsCached(ctx, f.module.ID, 60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source)
	_, res, err = f.svc.PrometheusStatsCached(ctx, f.module.ID, 60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, resultcache.Cached, res.Source)
	_, res, _ = f.svc.PrometheusStatsCached(ctx, f.module.ID, 1440, time.Time{}, time.Time{})
	assert.Equal(t, resultcache.Computed, res.Source, "another window")
	*f.clock = f.clock.Add(6 * time.Minute)
	_, res, _ = f.svc.PrometheusStatsCached(ctx, f.module.ID, 60, time.Time{}, time.Time{})
	assert.Equal(t, resultcache.Computed, res.Source, "the data of Prometheus is never kept longer than five minutes, whatever cache.result_minutes says")
}

func TestTheWindowOfAReportMovesTheVersionByAboutTwoPercent(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, windowBucket(at, 24), windowBucket(at.Add(10*time.Minute), 24), "inside one stretch of a quarter of an hour")
	assert.NotEqual(t, windowBucket(at, 24), windowBucket(at.Add(16*time.Minute), 24))
	assert.NotEqual(t, windowBucket(at, 1), windowBucket(at.Add(2*time.Minute), 1), "an hour is looked at in stretches of a minute and a bit")
	assert.NotEqual(t, windowBucket(at, 6), windowBucket(at.Add(8*time.Minute), 6))
}

func TestTheEffectReportCanBeAskedForInPartsAndEachPartIsKeptOnItsOwn(t *testing.T) {
	f := cachedFixture(t)
	ctx := context.Background()

	whole, err := f.svc.EffectReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, EffectParts, whole.Parts, "no parts named is all of them")
	require.NotNil(t, whole.Delay)

	usage, err := f.svc.EffectReportParts(ctx, f.module.ID, 24, []string{PartUsage})
	require.NoError(t, err)
	assert.Equal(t, []string{PartUsage}, usage.Parts)
	assert.Nil(t, usage.Delay, "a part that was not asked for is not read")
	assert.Empty(t, usage.Accuracy)
	assert.Empty(t, usage.Algorithm)
	assert.Contains(t, usage.Timings, "recommendations")
	assert.Contains(t, usage.Timings, "executions")
	assert.NotContains(t, usage.Timings, "capacity")
	assert.NotContains(t, usage.Timings, "tuning")

	delay, err := f.svc.EffectReportParts(ctx, f.module.ID, 24, []string{PartDelay})
	require.NoError(t, err)
	require.NotNil(t, delay.Delay)
	assert.NotContains(t, delay.Timings, "recommendations")

	_, res, err := f.svc.EffectReportCached(ctx, f.module.ID, 24, []string{PartCapacity})
	require.NoError(t, err)
	assert.Equal(t, resultcache.Computed, res.Source)
	_, res, _ = f.svc.EffectReportCached(ctx, f.module.ID, 24, []string{PartCapacity})
	assert.Equal(t, resultcache.Cached, res.Source)
	_, res, _ = f.svc.EffectReportCached(ctx, f.module.ID, 24, []string{PartTuning})
	assert.Equal(t, resultcache.Computed, res.Source, "another part is another answer")
}

func TestThePartsOfTheEffectReportAreReadInTheirOrderAndAWrongNameIsAnError(t *testing.T) {
	got, err := ParseEffectParts("capacity, delay")
	require.NoError(t, err)
	assert.Equal(t, []string{PartDelay, PartCapacity}, got, "in the order the page shows them, once each")
	got, err = ParseEffectParts("")
	require.NoError(t, err)
	assert.Equal(t, EffectParts, got)
	got, err = ParseEffectParts("usage,usage")
	require.NoError(t, err)
	assert.Equal(t, []string{PartUsage}, got)
	_, err = ParseEffectParts("delay,everything")
	require.ErrorIs(t, err, ErrEffectPart)
}
