package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func ptr[T any](v T) *T { return &v }

func deployment(replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(replicas)},
		Status:     appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: ready},
	}
}

func scaler(mode v1alpha1.Mode) *v1alpha1.PredictiveScaler {
	return &v1alpha1.PredictiveScaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", Generation: 1, CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		Spec: v1alpha1.PredictiveScalerSpec{
			ScaleTargetRef: v1alpha1.TargetRef{Kind: "Deployment", Name: "api"},
			Mode:           mode, MinReplicas: 2, MaxReplicas: 20,
			Decision: v1alpha1.Decision{Source: v1alpha1.SourceStatic, Static: &v1alpha1.StaticDecision{Replicas: 8}},
		},
	}
}

type fakeDecider struct {
	d   *Decision
	err error
}

func (f fakeDecider) Decide(context.Context, *v1alpha1.PredictiveScaler) (*Decision, error) {
	return f.d, f.err
}

type recordingReporter struct{ reports []Report }

func (r *recordingReporter) Report(_ context.Context, _ *v1alpha1.PredictiveScaler, rep Report) error {
	r.reports = append(r.reports, rep)
	return nil
}

type harness struct {
	r        *Reconciler
	c        client.Client
	reporter *recordingReporter
	events   *record.FakeRecorder
}

func newHarness(t *testing.T, decider Decider, objs ...client.Object) *harness {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).WithStatusSubresource(&v1alpha1.PredictiveScaler{}).Build()
	h := &harness{c: c, reporter: &recordingReporter{}, events: record.NewFakeRecorder(20)}
	h.r = &Reconciler{Client: c, Scheme: testScheme(t), Recorder: h.events, Reporter: h.reporter,
		Now:      func() time.Time { return now },
		Deciders: map[v1alpha1.DecisionSource]Decider{v1alpha1.SourceStatic: decider}}
	return h
}

func (h *harness) reconcile(t *testing.T) (ctrl.Result, v1alpha1.PredictiveScaler) {
	t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "api"}})
	require.NoError(t, err)
	var ps v1alpha1.PredictiveScaler
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &ps))
	return res, ps
}

func (h *harness) replicas(t *testing.T) int32 {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &d))
	return *d.Spec.Replicas
}

func TestAutoModeScalesTheTargetThroughTheScaleSubresource(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeAuto))
	res, ps := h.reconcile(t)
	assert.Equal(t, defaultEvaluationInterval, res.RequeueAfter)
	assert.EqualValues(t, 8, h.replicas(t))
	assert.EqualValues(t, 8, ps.Status.DesiredReplicas)
	assert.EqualValues(t, 4, ps.Status.CurrentReplicas, "status shows what was observed before the write")
	require.NotNil(t, ps.Status.LastScaleTime)
	assert.True(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionReady))
	assert.True(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionDecisionValid))
	assert.False(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionConflict))
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "applied", h.reporter.reports[0].Result)
	assert.Equal(t, "ScaleUp", h.reporter.reports[0].Action)
	assert.Len(t, h.events.Events, 1)
}

func TestObserveModeNeverWritesTheTarget(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeObserve))
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 4, h.replicas(t))
	assert.EqualValues(t, 8, ps.Status.DesiredReplicas, "the decision is still evaluated and reported")
	assert.Nil(t, ps.Status.LastScaleTime)
	assert.Equal(t, "observed", h.reporter.reports[0].Result)
}

func TestReconcileIsIdempotentAcrossRestarts(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeAuto))
	h.reconcile(t)
	require.EqualValues(t, 8, h.replicas(t))
	// a restarted operator reconciles again with the same inputs: nothing more happens
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 8, h.replicas(t))
	assert.EqualValues(t, 8, ps.Status.CurrentReplicas)
	assert.Len(t, h.reporter.reports, 1, "a quiet evaluation with nothing to do is not reported")
	assert.Len(t, h.events.Events, 1, "no second scaling event")
}

func TestAHorizontalPodAutoscalerOnTheSameTargetBlocksScaling(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"},
			MaxReplicas:    10,
		},
	}
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeAuto), hpa)
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 4, h.replicas(t), "the operator must not fight the HPA")
	assert.True(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionConflict))
	assert.False(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionReady))
	assert.Equal(t, "conflict", h.reporter.reports[0].Result)
	assert.Contains(t, meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionConflict).Message, "HorizontalPodAutoscaler api-hpa")
}

func TestOnlyTheOlderOfTwoScalersActs(t *testing.T) {
	older := scaler(v1alpha1.ModeAuto)
	older.Name = "api-old"
	older.CreationTimestamp = metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeAuto), older)
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 4, h.replicas(t))
	assert.True(t, meta.IsStatusConditionTrue(ps.Status.Conditions, v1alpha1.ConditionConflict))

	// the older scaler is not blocked by the newer one
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "api-old"}})
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)
	assert.EqualValues(t, 8, h.replicas(t))
}

func TestDecisionFailureFreezesAndFallbackOnlyRaises(t *testing.T) {
	h := newHarness(t, fakeDecider{err: errors.New("platform unreachable")}, deployment(10, 10), scaler(v1alpha1.ModeAuto))
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 10, h.replicas(t), "no decision: frozen, never scaled down")
	cond := meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionDecisionValid)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Unavailable", cond.Reason)
	assert.Contains(t, cond.Message, "platform unreachable")

	withFallback := scaler(v1alpha1.ModeAuto)
	withFallback.Spec.FallbackReplicas = ptr(int32(14))
	h = newHarness(t, fakeDecider{err: errors.New("platform unreachable")}, deployment(10, 10), withFallback)
	h.reconcile(t)
	assert.EqualValues(t, 14, h.replicas(t), "the fallback raises capacity while the platform is down")
}

func TestUnreadyReplicasHoldAScaleDown(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(3)}, deployment(10, 6), scaler(v1alpha1.ModeAuto))
	_, ps := h.reconcile(t)
	assert.EqualValues(t, 10, h.replicas(t))
	assert.EqualValues(t, 6, ps.Status.ReadyReplicas)
	assert.Equal(t, "held", h.reporter.reports[0].Result)
	assert.Contains(t, h.reporter.reports[0].Reason, "6 of 10")
}

func TestCooldownUsesTheLastScaleTimeFromStatus(t *testing.T) {
	s := scaler(v1alpha1.ModeAuto)
	s.Spec.Behavior.ScaleUp.Cooldown = &metav1.Duration{Duration: time.Hour}
	last := metav1.NewTime(now.Add(-time.Minute))
	s.Status.LastScaleTime = &last
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), s)
	h.reconcile(t)
	assert.EqualValues(t, 4, h.replicas(t), "a restarted operator still honours the cooldown")
}

func TestMissingOrInvalidTargetsAreReportedNotFatal(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, scaler(v1alpha1.ModeAuto))
	_, ps := h.reconcile(t)
	cond := meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "TargetNotFound", cond.Reason)

	bad := scaler(v1alpha1.ModeAuto)
	bad.Spec.ScaleTargetRef.Kind = "DaemonSet"
	h = newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), bad)
	_, ps = h.reconcile(t)
	assert.Equal(t, "InvalidTarget", meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionReady).Reason)

	badVersion := scaler(v1alpha1.ModeAuto)
	badVersion.Spec.ScaleTargetRef.APIVersion = "extensions/v1beta1"
	h = newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), badVersion)
	_, ps = h.reconcile(t)
	assert.Equal(t, "InvalidTarget", meta.FindStatusCondition(ps.Status.Conditions, v1alpha1.ConditionReady).Reason)
	assert.EqualValues(t, 4, h.replicas(t))
}

func TestStatefulSetsAreScaledToo(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
		Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3},
	}
	s := scaler(v1alpha1.ModeAuto)
	s.Name = "db"
	s.Spec.ScaleTargetRef = v1alpha1.TargetRef{Kind: "StatefulSet", Name: "db"}
	h := newHarness(t, fakeDecider{d: fresh(5)}, sts, s)
	_, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "db"}})
	require.NoError(t, err)
	var got appsv1.StatefulSet
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "db"}, &got))
	assert.EqualValues(t, 5, *got.Spec.Replicas)
}

func TestDeletedScalersAreIgnored(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "gone"}})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestEvaluationIntervalHasAFloor(t *testing.T) {
	s := scaler(v1alpha1.ModeObserve)
	s.Spec.EvaluationInterval = &metav1.Duration{Duration: time.Millisecond}
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), s)
	res, _ := h.reconcile(t)
	assert.Equal(t, minEvaluationInterval, res.RequeueAfter)
}

func TestRepeatedIdenticalReportsAreDropped(t *testing.T) {
	// observe mode keeps wanting 8 while 4 run: the same report must not be sent every evaluation
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), scaler(v1alpha1.ModeObserve))
	h.reconcile(t)
	h.reconcile(t)
	h.reconcile(t)
	assert.Len(t, h.reporter.reports, 1)

	// a change in the outcome is reported again
	h.r.Deciders[v1alpha1.SourceStatic] = fakeDecider{d: fresh(12)}
	h.reconcile(t)
	require.Len(t, h.reporter.reports, 2)
	assert.EqualValues(t, 12, h.reporter.reports[1].Desired)
}
