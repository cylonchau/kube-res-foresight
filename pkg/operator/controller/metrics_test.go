package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
)

func metricsHarness(t *testing.T, d Decider, mode v1alpha1.Mode, extra ...*autoscalingv2.HorizontalPodAutoscaler) (*harness, *Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	var h *harness
	if len(extra) > 0 {
		h = newHarness(t, d, deployment(4, 3), scaler(mode), extra[0])
	} else {
		h = newHarness(t, d, deployment(4, 3), scaler(mode))
	}
	h.r.Metrics = m
	return h, m, reg
}

func TestOperatorMetricsAfterOneAppliedReconcile(t *testing.T) {
	h, m, reg := metricsHarness(t, fakeDecider{d: fresh(8)}, v1alpha1.ModeAuto)
	h.reconcile(t)

	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("applied")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.scaleActions.WithLabelValues("up", "applied")))
	assert.EqualValues(t, 0, testutil.ToFloat64(m.scaleActions.WithLabelValues("down", "applied")))
	assert.EqualValues(t, 8, testutil.ToFloat64(m.desired.WithLabelValues("api")))
	assert.EqualValues(t, 4, testutil.ToFloat64(m.current.WithLabelValues("api")), "the replicas observed before the write")
	assert.EqualValues(t, 3, testutil.ToFloat64(m.ready.WithLabelValues("api")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.decisionValid.WithLabelValues("api")))
	assert.Greater(t, testutil.ToFloat64(m.decisionExpiresIn.WithLabelValues("api")), 0.0)

	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, mf := range families {
		names[mf.GetName()] = true
		assert.True(t, strings.HasPrefix(mf.GetName(), "kube_resource_fcst_operator_"), mf.GetName())
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				assert.Contains(t, []string{"module", "direction", "result", "source", "le"}, l.GetName(), "%s has an unbounded label", mf.GetName())
			}
		}
	}
	for _, want := range []string{"kube_resource_fcst_operator_reconciles_total", "kube_resource_fcst_operator_reconcile_duration_seconds",
		"kube_resource_fcst_operator_scale_actions_total", "kube_resource_fcst_operator_desired_replicas", "kube_resource_fcst_operator_current_replicas",
		"kube_resource_fcst_operator_ready_replicas", "kube_resource_fcst_operator_decision_valid", "kube_resource_fcst_operator_decision_expires_in_seconds"} {
		assert.True(t, names[want], "%s is not exposed", want)
	}
}

func TestOperatorMetricsCountScaleDownAndObservedActionsApart(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	h := newHarness(t, fakeDecider{d: fresh(2)}, deployment(4, 4), scaler(v1alpha1.ModeObserve))
	h.r.Metrics = m
	h.reconcile(t)
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("observed")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.scaleActions.WithLabelValues("down", "observed")), "observe mode reports the action it would take")
	assert.EqualValues(t, 0, testutil.ToFloat64(m.scaleActions.WithLabelValues("down", "applied")))
}

func TestOperatorMetricsCountConflictsBySource(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"}, MaxReplicas: 10,
		},
	}
	h, m, _ := metricsHarness(t, fakeDecider{d: fresh(8)}, v1alpha1.ModeAuto, hpa)
	h.reconcile(t)
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("conflict")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.conflicts.WithLabelValues("hpa")))
	assert.EqualValues(t, 0, testutil.ToFloat64(m.scaleActions.WithLabelValues("up", "applied")), "a conflicting target is left alone")

	assert.Equal(t, "keda", conflictSource("KEDA ScaledObject x also targets Deployment api"))
	assert.Equal(t, "predictivescaler", conflictSource("PredictiveScaler a is the older scaler of Deployment api"))
	assert.Equal(t, "other", conflictSource("something else"))
}

func TestOperatorMetricsCountAnErrorWithoutInventingReplicas(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	h := newHarness(t, fakeDecider{d: fresh(8)}, scaler(v1alpha1.ModeAuto)) // the Deployment does not exist
	h.r.Metrics = m
	h.reconcile(t)
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("error")))
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		assert.NotEqual(t, "kube_resource_fcst_operator_current_replicas", mf.GetName(), "no replica gauge for a target that could not be read")
	}
}

func TestOperatorMetricsAreOptional(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeAuto))
	h.reconcile(t) // r.Metrics is nil: nothing panics
	assert.EqualValues(t, 8, h.replicas(t))
}

// ---- the Prometheus decision source

func promServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func promScaler(url string) *v1alpha1.PredictiveScaler {
	ps := scaler(v1alpha1.ModeAuto)
	ps.Spec.Decision = v1alpha1.Decision{Source: v1alpha1.SourcePrometheus, Prometheus: &v1alpha1.PrometheusDecision{
		URL: url, Query: `sum(desired_replicas{app="api"})`, MaxStalenessSeconds: 120}}
	return ps
}

func promBody(now time.Time, results string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + results + `]}}`
}

func sample(ts time.Time, value string) string {
	return `{"metric":{},"value":[` + formatTS(ts) + `,"` + value + `"]}`
}

func formatTS(ts time.Time) string { return strconv.FormatInt(ts.Unix(), 10) }

func TestPrometheusDeciderReadsOneSeriesAndRoundsUp(t *testing.T) {
	var gotQuery, gotAuth string
	srv := promServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotAuth = r.URL.Query().Get("query"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(promBody(now, sample(now.Add(-10*time.Second), "7.2"))))
	})
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "prom-token", Namespace: "prod"}, Data: map[string][]byte{"token": []byte("s3cret\n")}}
	reader := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(secret).Build()
	ps := promScaler(srv.URL)
	ps.Spec.Decision.Prometheus.BearerTokenSecretRef = &v1alpha1.SecretKeyRef{Name: "prom-token"}

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	d := &PrometheusDecider{Reader: reader, Metrics: m, Now: func() time.Time { return now }, HTTPClient: srv.Client()}
	decision, err := d.Decide(context.Background(), ps)
	require.NoError(t, err)
	assert.EqualValues(t, 8, decision.Replicas, "7.2 is rounded up")
	assert.Equal(t, "prometheus", decision.Source)
	require.NotNil(t, decision.ValidUntil)
	assert.WithinDuration(t, now.Add(-10*time.Second).Add(120*time.Second), *decision.ValidUntil, time.Second)
	assert.Equal(t, `sum(desired_replicas{app="api"})`, gotQuery)
	assert.Equal(t, "Bearer s3cret", gotAuth, "the token is read from the Secret and trimmed")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.prometheusReads.WithLabelValues("ok")))
}

func TestPrometheusDeciderRefusesWhatItCannotTrust(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    func() string
		wantErr string
	}{
		{"no series", 200, func() string { return promBody(now, "") }, "no series"},
		{"several series", 200, func() string { return promBody(now, sample(now, "3")+","+sample(now, "4")) }, "2 series"},
		{"NaN", 200, func() string { return promBody(now, sample(now, "NaN")) }, "not a finite number"},
		{"infinity", 200, func() string { return promBody(now, sample(now, "+Inf")) }, "not a finite number"},
		{"negative", 200, func() string { return promBody(now, sample(now, "-1")) }, "negative"},
		{"absurd", 200, func() string { return promBody(now, sample(now, "9999999")) }, "implausible"},
		{"stale", 200, func() string { return promBody(now, sample(now.Add(-10*time.Minute), "5")) }, "older than the allowed"},
		{"matrix", 200, func() string { return `{"status":"success","data":{"resultType":"matrix","result":[]}}` }, "instant vector"},
		{"query error", 200, func() string { return `{"status":"error","errorType":"bad_data","error":"parse error"}` }, "parse error"},
		{"permission denied", 403, func() string { return `forbidden` }, "refused the request"},
		{"unauthorized", 401, func() string { return `no` }, "refused the request"},
		{"server error", 500, func() string { return `boom` }, "answered 500"},
		{"garbage", 200, func() string { return `<html>` }, "unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := promServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body()))
			})
			m := NewMetrics(prometheus.NewRegistry())
			d := &PrometheusDecider{Reader: fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), Metrics: m, Now: func() time.Time { return now }, HTTPClient: srv.Client()}
			decision, err := d.Decide(context.Background(), promScaler(srv.URL))
			require.Error(t, err)
			assert.Nil(t, decision)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.EqualValues(t, 1, testutil.ToFloat64(m.prometheusReads.WithLabelValues("error")))
		})
	}
}

func TestPrometheusSourceFreezesOrFallsBackWhenTheDecisionIsUnavailable(t *testing.T) {
	srv := promServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	ps := promScaler(srv.URL)

	// without a fallback the replicas are frozen
	h := newHarness(t, nil, deployment(4, 4), ps)
	h.r.Deciders[v1alpha1.SourcePrometheus] = &PrometheusDecider{Reader: h.c, Now: func() time.Time { return now }, HTTPClient: srv.Client()}
	_, got := h.reconcile(t)
	assert.EqualValues(t, 4, h.replicas(t), "no decision and no fallback: the replicas are left alone")
	assert.False(t, isConditionTrue(got, v1alpha1.ConditionDecisionValid))

	// a fallback above the current replicas is applied
	ps2 := promScaler(srv.URL)
	ps2.Spec.FallbackReplicas = ptr(int32(6))
	h2 := newHarness(t, nil, deployment(4, 4), ps2)
	h2.r.Deciders[v1alpha1.SourcePrometheus] = &PrometheusDecider{Reader: h2.c, Now: func() time.Time { return now }, HTTPClient: srv.Client()}
	h2.reconcile(t)
	assert.EqualValues(t, 6, h2.replicas(t))
}

func TestPrometheusDeciderRequiresItsConfiguration(t *testing.T) {
	d := &PrometheusDecider{Reader: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	_, err := d.Decide(context.Background(), scaler(v1alpha1.ModeAuto))
	assert.Error(t, err)
	ps := promScaler("ftp://nope")
	_, err = d.Decide(context.Background(), ps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid prometheus url")
}

func isConditionTrue(ps v1alpha1.PredictiveScaler, t string) bool {
	for _, c := range ps.Status.Conditions {
		if c.Type == t {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}
