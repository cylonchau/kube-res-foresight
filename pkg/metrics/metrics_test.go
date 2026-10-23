package metrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpoints(t *testing.T) {
	Factory.Counter("test_total", "test").WithLabelValues().Inc()
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	ok := Handler(func() error { return nil })
	if body := get(ok, "/metrics").Body.String(); !strings.Contains(body, "kubeforecast_test_total 1") {
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
