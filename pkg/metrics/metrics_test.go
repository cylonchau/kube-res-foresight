package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEndpoints(t *testing.T) {
	Factory.Counter("test_total", "test").WithLabelValues().Inc()
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	ok := Handler(func() error { return nil })
	if body := get(ok, "/metrics").Body.String(); !strings.Contains(body, "kube_res_foresight_test_total 1") {
		t.Fatalf("metric missing from /metrics: %.200s", body)
	}
	if rec := get(ok, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	bad := Handler(func() error { return errors.New("stale") })
	if rec := get(bad, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy healthz = %d", rec.Code)
	}
}

func TestReadyzFollowsTheReadinessCheck(t *testing.T) {
	get := func(h http.Handler, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if code := get(Handler(func() error { return nil }), "/readyz"); code != 200 {
		t.Fatalf("readyz = %d", code)
	}
	if code := get(Handler(func() error { return errors.New("db down") }), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("not ready readyz = %d", code)
	}
}

func TestTheEarlierNamesAreExportedOnlyWhenAsked(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "kube_res_foresight_probe", Help: "probe"})
	other := prometheus.NewGauge(prometheus.GaugeOpts{Name: "go_other", Help: "not ours"})
	g.Set(7)
	reg.MustRegister(g, other)

	names := func(gt prometheus.Gatherer) map[string]float64 {
		families, err := gt.Gather()
		require.NoError(t, err)
		out := map[string]float64{}
		for _, f := range families {
			out[f.GetName()] = f.GetMetric()[0].GetGauge().GetValue()
		}
		return out
	}
	assert.Equal(t, map[string]float64{"kube_res_foresight_probe": 7, "go_other": 0}, names(reg))
	both := names(withLegacyNames(reg))
	assert.Equal(t, map[string]float64{"kube_res_foresight_probe": 7, "kube_resource_fcst_probe": 7, "go_other": 0}, both, "a copy under the old name, nothing else")
	g.Set(9)
	assert.Equal(t, 9.0, names(withLegacyNames(reg))["kube_resource_fcst_probe"], "the copy follows the value")
}

// The setting reaches the handler: /metrics carries both names when it is on.
func TestTheHandlerServesTheEarlierNamesWhenTheSettingIsOn(t *testing.T) {
	Factory.Gauge("legacy_probe", "probe").WithLabelValues().Set(3)
	serve := func() string {
		rec := httptest.NewRecorder()
		Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}
	LegacyNames = false
	assert.NotContains(t, serve(), "kube_resource_fcst_legacy_probe")
	LegacyNames = true
	t.Cleanup(func() { LegacyNames = false })
	body := serve()
	assert.Contains(t, body, "kube_res_foresight_legacy_probe 3")
	assert.Contains(t, body, "kube_resource_fcst_legacy_probe 3")
}
