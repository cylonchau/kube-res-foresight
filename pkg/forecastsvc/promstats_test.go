package forecastsvc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promFake is a Prometheus that answers the quantile queries with a figure of its own for each, and records what it was asked.
type promFake struct {
	mu      sync.Mutex
	queries []string
	// series makes the answers of the queries that contain the text more than one series
	series string
}

func (p *promFake) QueryVector(_ context.Context, query string, _ time.Time) ([]promclient.VectorSample, error) {
	p.mu.Lock()
	p.queries = append(p.queries, query)
	p.mu.Unlock()
	value := 0.0
	switch {
	case strings.Contains(query, "quantile_over_time(0.5,"):
		value = 1
	case strings.Contains(query, "quantile_over_time(0.9,"):
		value = 2
	case strings.Contains(query, "quantile_over_time(0.95,"):
		value = 3
	case strings.Contains(query, "quantile_over_time(0.99,"):
		value = 4
	case strings.Contains(query, "max_over_time("):
		value = 5
	default:
		return nil, nil
	}
	if p.series != "" && strings.Contains(query, p.series) {
		return []promclient.VectorSample{{Value: value}, {Value: value}}, nil
	}
	return []promclient.VectorSample{{Value: value}}, nil
}

func (p *promFake) asked(part string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, q := range p.queries {
		if strings.Contains(q, part) {
			out = append(out, q)
		}
	}
	return out
}

// The P50, P90, P95, P99 and the maximum are the answers of the data source to quantile_over_time over a subquery of the PromQL of the mapping; the sum over
// the Pods and one Pod (the same query over the ready replicas) are asked apart, and the PromQL that was sent comes back with them.
func TestTheQuantilesAreAskedOfPrometheusWithThePromQLOfTheMapping(t *testing.T) {
	f := opFixture(t)
	fake := &promFake{}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	got, err := f.svc.PrometheusStats(context.Background(), f.module.ID, 7*24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "604800s", got.Window)
	assert.Equal(t, "420s", got.Step, "a week at a step of a minute is 10080 points: the step is made 420 s, a multiple of the minute, to keep it under 1500")
	assert.NotEmpty(t, got.Datasource.Name)

	var cpu *PromMetric
	for i := range got.Metrics {
		if got.Metrics[i].Kind == "cpu" {
			cpu = &got.Metrics[i]
		}
	}
	require.NotNil(t, cpu)
	require.Empty(t, cpu.Error)
	require.NotNil(t, cpu.Total)
	assert.Equal(t, PromQuantiles{P50: 1, P90: 2, P95: 3, P99: 4, Max: 5}, *cpu.Total)
	require.NotNil(t, cpu.Pod, "the module has a replicas source: one Pod is the same query over the replicas")
	assert.Equal(t, 3.0, cpu.Pod.P95)
	assert.Contains(t, cpu.Query, "cpu", "the PromQL of the mapping as rendered for the module")
	assert.Contains(t, cpu.Example, "quantile_over_time(0.95, (")
	assert.Contains(t, cpu.Example, "[604800s:420s])")
	assert.NotEmpty(t, cpu.Replicas)

	// the pod queries divide by the replicas at each moment
	assert.NotEmpty(t, fake.asked("/ scalar("))
	// nothing was read from the samples the platform stored: the numbers are the fake's, whatever was collected
	assert.Equal(t, 5.0, cpu.Total.Max)
}

func TestAQueryThatAnswersSeveralSeriesIsRefusedWithTheReason(t *testing.T) {
	f := opFixture(t)
	fake := &promFake{series: "quantile_over_time(0.9,"}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	got, err := f.svc.PrometheusStats(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	for _, m := range got.Metrics {
		if m.Kind == "cpu" || m.Kind == "memory" {
			assert.Nil(t, m.Total, m.Kind)
			assert.Contains(t, m.Error, "series", "the mapping has to sum to one series: "+m.Kind)
		}
	}
}

func TestACustomRangeIsAskedAtItsEndAndRefusedBackToFront(t *testing.T) {
	f := opFixture(t)
	fake := &promFake{}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	now := f.svc.now()
	got, err := f.svc.PrometheusStats(context.Background(), f.module.ID, 0, now.Add(-6*time.Hour), now)
	require.NoError(t, err)
	assert.Equal(t, "21600s", got.Window)
	assert.Equal(t, 360, got.WindowMinutes)
	_, err = f.svc.PrometheusStats(context.Background(), f.module.ID, 0, now, now.Add(-time.Hour))
	assert.ErrorIs(t, err, ErrOverprovisionWindow)
	_, err = f.svc.PrometheusStats(context.Background(), f.module.ID, 17, time.Time{}, time.Time{})
	assert.ErrorIs(t, err, ErrOverprovisionWindow)
}

// slowFake answers the metrics whose query contains fast at once and makes the others wait for the context to end, as a Prometheus that is slow on a long window.
type slowFake struct {
	promFake
	fast string
}

func (p *slowFake) QueryVector(ctx context.Context, query string, at time.Time) ([]promclient.VectorSample, error) {
	if !strings.Contains(query, p.fast) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.promFake.QueryVector(ctx, query, at)
}

func TestTheCoreScopeAsksOnlyTheCPUAndTheMemoryAndTheMoreScopeTheRest(t *testing.T) {
	f := opFixture(t)
	fake := &promFake{}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	ctx := context.Background()
	all, err := f.svc.PrometheusStatsScoped(ctx, f.module.ID, 24*60, time.Time{}, time.Time{}, PromScopeAll)
	require.NoError(t, err)
	core, err := f.svc.PrometheusStatsScoped(ctx, f.module.ID, 24*60, time.Time{}, time.Time{}, PromScopeCore)
	require.NoError(t, err)
	more, err := f.svc.PrometheusStatsScoped(ctx, f.module.ID, 24*60, time.Time{}, time.Time{}, PromScopeMore)
	require.NoError(t, err)
	kinds := func(p *PromStats) []string {
		out := []string{}
		for _, m := range p.Metrics {
			out = append(out, m.Kind)
		}
		return out
	}
	assert.Len(t, all.Metrics, len(core.Metrics)+len(more.Metrics), "the two scopes are the whole")
	for _, k := range kinds(core) {
		assert.Contains(t, []string{"cpu", "memory"}, k)
	}
	for _, k := range kinds(more) {
		assert.NotContains(t, []string{"cpu", "memory"}, k)
	}
	assert.False(t, all.Partial)
}

func TestWhenTheTimeLimitIsOverTheMetricsThatCameAreGivenAndTheOthersAreToldNotReached(t *testing.T) {
	f := opFixture(t)
	fake := &slowFake{fast: "cpu"}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	// the gate of the data source is wide: the slow metrics do not hold the places the quick one needs
	require.NoError(t, f.db.AutoMigrate(&model.SysConfig{}))
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: timing.PrometheusTotalConcurrency.Key, ConfigValue: "64", ConfigName: "x"}).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := f.svc.PrometheusStats(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err, "the time limit is an answer with what came, not an error (a 504)")
	assert.Less(t, time.Since(started), 1500*time.Millisecond, "the answer is made before the limit of the request, not at it")
	assert.True(t, got.Partial)
	assert.Positive(t, got.Skipped)
	var cpuDone, skipped int
	for _, m := range got.Metrics {
		if m.Kind == "cpu" && m.Total != nil {
			cpuDone++
		}
		if m.Skipped {
			skipped++
			assert.Contains(t, m.Error, "time limit")
			assert.Nil(t, m.Total)
		}
	}
	assert.Equal(t, 1, cpuDone, "the metric that answered is complete")
	assert.Equal(t, got.Skipped, skipped)
}

func TestAPartialAnswerIsNotKeptSoTheNextAskWorksOnWhatWasNotReached(t *testing.T) {
	f := opFixture(t)
	fake := &slowFake{fast: "cpu"}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	first, _, err := f.svc.PrometheusStatsCached(ctx, f.module.ID, 24*60, time.Time{}, time.Time{})
	cancel()
	require.NoError(t, err)
	require.True(t, first.Partial)
	fake.fast = "" // the data source is quick now: every query contains the empty text
	second, kept, err := f.svc.PrometheusStatsCached(context.Background(), f.module.ID, 24*60, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.False(t, second.Partial, "asked again, it is complete")
	assert.NotEqual(t, "hit", string(kept.Source), "the partial answer was not served from the cache")
}
