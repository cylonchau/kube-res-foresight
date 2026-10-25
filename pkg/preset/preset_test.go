package preset_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/demo"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/preset"
)

var target = collector.Target{Namespace: "prod", Workload: "api", Container: "app", Service: "api-svc"}

func TestEveryPresetIsAValidMapping(t *testing.T) {
	require.NotEmpty(t, preset.All())
	ids := map[string]bool{}
	for _, p := range preset.All() {
		assert.False(t, ids[p.ID], "duplicate id %s", p.ID)
		ids[p.ID] = true
		m := p.Mapping(1, "", 0)
		assert.NoError(t, model.ValidateMetricMapping(m), p.ID)
		assert.Equal(t, p.ID, m.PresetID, "the mapping remembers its preset")
		assert.Equal(t, p.Unit, m.Unit)
	}
}

func TestPresetsCoverEveryLevelAndRequiredVariable(t *testing.T) {
	levels, metrics := map[string]bool{}, map[string]bool{}
	for _, p := range preset.All() {
		levels[p.Level], metrics[p.Metric] = true, true
		used := model.TemplateVariables(p.Query)
		for _, required := range model.MetricLevels[p.Level] {
			assert.Contains(t, used, required, "%s must use {{%s}} at level %s", p.ID, required, p.Level)
		}
	}
	for _, level := range []string{"workload", "service", "container"} {
		assert.True(t, levels[level], "no preset at level %s", level)
	}
	for _, metric := range []string{"cpu", "memory", "rps", "latency", "oom", "replicas"} {
		assert.True(t, metrics[metric], "no preset for %s", metric)
	}
}

func TestEveryPresetHasBilingualDocumentationAndASource(t *testing.T) {
	for _, p := range preset.All() {
		for name, text := range map[string]preset.Text{"name": p.Name, "scenario": p.Scenario, "usage": p.Usage, "limitations": p.Limitations} {
			assert.NotEmpty(t, strings.TrimSpace(text.ZH), "%s %s zh", p.ID, name)
			assert.NotEmpty(t, strings.TrimSpace(text.EN), "%s %s en", p.ID, name)
		}
		assert.NotEmpty(t, p.Source.Component, p.ID)
		assert.NotEmpty(t, p.Source.Metrics, p.ID)
		assert.NotEmpty(t, p.Source.Labels, p.ID)
		assert.False(t, p.Verified, "%s has not been run against a real Prometheus", p.ID)
		for _, series := range p.Source.Metrics {
			assert.Contains(t, p.Query, series, "%s documents a series its query does not read", p.ID)
		}
	}
}

// a recording querier proves the rendered text reaches the datasource and nothing is left unreplaced
type recordingQuerier struct{ queries []string }

func (r *recordingQuerier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	r.queries = append(r.queries, query)
	return demo.Querier{}.QueryRange(ctx, query, start, end, step)
}

func TestEveryPresetRendersAndTheDemoGeneratorReturnsOneSeries(t *testing.T) {
	start := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for _, p := range preset.All() {
		m := p.Mapping(1, "", 0)
		query, err := collector.RenderMapping(m, target)
		require.NoError(t, err, p.ID)
		assert.NotContains(t, query, "{{", p.ID)
		if strings.Contains(p.Query, "{{namespace}}") {
			assert.Contains(t, query, `"prod"`, p.ID)
		}

		rec := &recordingQuerier{}
		points, err := rec.QueryRange(context.Background(), query, start, start.Add(2*time.Hour), time.Minute)
		require.NoError(t, err, p.ID)
		assert.Len(t, points, 121, "%s: one point per step", p.ID)
		assert.Equal(t, []string{query}, rec.queries)
		for _, pt := range points {
			assert.GreaterOrEqual(t, pt.Value, 0.0, p.ID)
		}
	}
}

func TestUnknownPresetIsRejected(t *testing.T) {
	_, err := preset.Get("nope")
	assert.Error(t, err)
	p, err := preset.Get("istio.rps.service")
	require.NoError(t, err)
	assert.Equal(t, "rps", p.Metric)
}
