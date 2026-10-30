package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func metricsHarness(t *testing.T, d Decider, extra ...*autoscalingv2.HorizontalPodAutoscaler) (*harness, *Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	h := newHarness(t, d, deployment(4, 3))
	if len(extra) > 0 {
		h = newHarness(t, d, deployment(4, 3), extra[0])
	}
	h.a.Metrics = m
	return h, m, reg
}

func TestOperatorMetricsAfterOneAppliedEvaluation(t *testing.T) {
	h, m, reg := metricsHarness(t, fakeDecider{d: fresh(8)})
	h.evaluate(scaler())

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
		assert.NotContains(t, mf.GetName(), "prometheus_read", "the Prometheus decision source is gone")
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

func TestOperatorMetricsCountAHeldScaleDownApartFromAnAppliedOne(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	h := newHarness(t, fakeDecider{d: fresh(2)}, deployment(4, 3)) // only 3 of 4 are Ready: the scale-down is held
	h.a.Metrics = m
	h.evaluate(scaler())
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("held")))
	assert.EqualValues(t, 0, testutil.ToFloat64(m.scaleActions.WithLabelValues("down", "applied")))
}

func TestOperatorMetricsCountConflictsBySource(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"}, MaxReplicas: 10,
		},
	}
	h, m, _ := metricsHarness(t, fakeDecider{d: fresh(8)}, hpa)
	h.evaluate(scaler())
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("conflict")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.conflicts.WithLabelValues("hpa")))
	assert.EqualValues(t, 0, testutil.ToFloat64(m.scaleActions.WithLabelValues("up", "applied")), "a conflicting target is left alone")

	assert.Equal(t, "keda", conflictSource("KEDA ScaledObject x also targets Deployment api"))
	assert.Equal(t, "other", conflictSource("something else"))
}

func TestOperatorMetricsCountAnErrorWithoutInventingReplicas(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	h := newHarness(t, fakeDecider{d: fresh(8)}) // the Deployment does not exist
	h.a.Metrics = m
	h.evaluate(scaler())
	assert.EqualValues(t, 1, testutil.ToFloat64(m.reconciles.WithLabelValues("error")))
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		assert.NotEqual(t, "kube_resource_fcst_operator_current_replicas", mf.GetName(), "no replica gauge for a target that could not be read")
	}
}

func TestOperatorMetricsAreOptional(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	h.evaluate(scaler()) // the applier has no Metrics: nothing panics
	assert.EqualValues(t, 8, h.replicas(t))
}
