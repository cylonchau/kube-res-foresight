package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))
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

// scaler is the scaler of the module "api": between 2 and 20 replicas.
func scaler() *Scaler {
	return &Scaler{Name: "module-api", Namespace: "prod", Module: "api", Target: TargetRef{Kind: "Deployment", Name: "api"}, Spec: Spec{MinReplicas: 2, MaxReplicas: 20}}
}

type fakeDecider struct {
	d   *Decision
	err error
}

func (f fakeDecider) Decide(context.Context, *Scaler) (*Decision, error) {
	return f.d, f.err
}

type recordingReporter struct{ reports []Report }

func (r *recordingReporter) Report(_ context.Context, _ *Scaler, rep Report) error {
	r.reports = append(r.reports, rep)
	return nil
}

type harness struct {
	a        *Applier
	c        client.Client
	reporter *recordingReporter
}

func newHarness(t *testing.T, decider Decider, objs ...client.Object) *harness {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	h := &harness{c: c, reporter: &recordingReporter{}}
	h.a = &Applier{Client: c, Reporter: h.reporter, Now: func() time.Time { return now }, Decider: decider}
	return h
}

func (h *harness) evaluate(s *Scaler) {
	h.a.Evaluate(context.Background(), s, "module/"+s.Namespace+"/"+s.Name)
}

func (h *harness) replicas(t *testing.T) int32 {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &d))
	return *d.Spec.Replicas
}

func TestTheTargetIsScaledThroughTheScaleSubresource(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	s := scaler()
	h.evaluate(s)
	assert.EqualValues(t, 8, h.replicas(t))
	require.NotNil(t, s.LastScale, "the time of the write is kept on the scaler for the cooldown of the next round")
	assert.True(t, s.LastScale.Equal(now))
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "applied", h.reporter.reports[0].Result)
	assert.Equal(t, "ScaleUp", h.reporter.reports[0].Action)
	assert.Equal(t, "api", h.reporter.reports[0].TargetName)
	assert.EqualValues(t, 4, h.reporter.reports[0].Current, "the report shows what was observed before the write")
	assert.EqualValues(t, 8, h.reporter.reports[0].Desired)
}

func TestEvaluatingAgainWithTheSameInputsDoesNothingMore(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	s := scaler()
	h.evaluate(s)
	require.EqualValues(t, 8, h.replicas(t))
	// a restarted operator evaluates again with the same inputs: nothing more happens
	h.evaluate(scaler())
	assert.EqualValues(t, 8, h.replicas(t))
	assert.Len(t, h.reporter.reports, 1, "a quiet evaluation with nothing to do is not reported")
}

func TestAHorizontalPodAutoscalerOnTheSameTargetBlocksScaling(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api-hpa", Namespace: "prod"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"},
			MaxReplicas:    10,
		},
	}
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4), hpa)
	h.evaluate(scaler())
	assert.EqualValues(t, 4, h.replicas(t), "the operator must not fight the HPA")
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "conflict", h.reporter.reports[0].Result)
	assert.Contains(t, h.reporter.reports[0].Reason, "another autoscaler")
}

func TestDecisionFailureFreezesAndFallbackOnlyRaises(t *testing.T) {
	h := newHarness(t, fakeDecider{err: errors.New("platform unreachable")}, deployment(10, 10))
	h.evaluate(scaler())
	assert.EqualValues(t, 10, h.replicas(t), "no decision: frozen, never scaled down")
	assert.Empty(t, h.reporter.reports, "a frozen evaluation with nothing to do is quiet")

	withFallback := scaler()
	withFallback.Spec.FallbackReplicas = ptr(int32(14))
	h = newHarness(t, fakeDecider{err: errors.New("platform unreachable")}, deployment(10, 10))
	h.evaluate(withFallback)
	assert.EqualValues(t, 14, h.replicas(t), "the fallback raises capacity while the platform is down")
}

func TestUnreadyReplicasHoldAScaleDown(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(3)}, deployment(10, 6))
	h.evaluate(scaler())
	assert.EqualValues(t, 10, h.replicas(t))
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "held", h.reporter.reports[0].Result)
	assert.Contains(t, h.reporter.reports[0].Reason, "6 of 10")
}

func TestCooldownUsesTheLastScaleTimeOfTheScaler(t *testing.T) {
	s := scaler()
	s.Spec.Behavior.ScaleUp.Cooldown = time.Hour
	last := now.Add(-time.Minute)
	s.LastScale = &last
	h := newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	h.evaluate(s)
	assert.EqualValues(t, 4, h.replicas(t), "the scale-up waits for its cooldown")
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "held", h.reporter.reports[0].Result)
}

func TestAMissingOrUnsupportedTargetIsReportedNotFatal(t *testing.T) {
	h := newHarness(t, fakeDecider{d: fresh(8)})
	h.evaluate(scaler())
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "error", h.reporter.reports[0].Result)
	assert.Contains(t, h.reporter.reports[0].Reason, "not found")

	bad := scaler()
	bad.Target.Kind = "DaemonSet"
	h = newHarness(t, fakeDecider{d: fresh(8)}, deployment(4, 4))
	h.evaluate(bad)
	require.Len(t, h.reporter.reports, 1)
	assert.Equal(t, "error", h.reporter.reports[0].Result)
	assert.Contains(t, h.reporter.reports[0].Reason, "unsupported kind")
	assert.EqualValues(t, 4, h.replicas(t))
}

func TestStatefulSetsAreScaledToo(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3))},
		Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3},
	}
	s := scaler()
	s.Name, s.Target = "module-db", TargetRef{Kind: "StatefulSet", Name: "db"}
	h := newHarness(t, fakeDecider{d: fresh(5)}, sts)
	h.evaluate(s)
	var got appsv1.StatefulSet
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "db"}, &got))
	assert.EqualValues(t, 5, *got.Spec.Replicas)
}

func TestRepeatedIdenticalReportsAreDropped(t *testing.T) {
	// 3 replicas of 10 are wanted but only 6 are Ready: the same hold must not be reported at every evaluation
	h := newHarness(t, fakeDecider{d: fresh(3)}, deployment(10, 6))
	for i := 0; i < 3; i++ {
		h.evaluate(scaler())
	}
	assert.Len(t, h.reporter.reports, 1)

	// a change in the outcome is reported again: now 12 are wanted, a scale-up that the unready replicas do not hold
	h.a.Decider = fakeDecider{d: fresh(12)}
	h.evaluate(scaler())
	require.Len(t, h.reporter.reports, 2)
	assert.Equal(t, "applied", h.reporter.reports[1].Result)
	assert.EqualValues(t, 12, h.reporter.reports[1].Desired)
}

func TestThereIsNoDecisionSourceWithoutADecider(t *testing.T) {
	h := newHarness(t, nil, deployment(4, 4))
	h.evaluate(scaler())
	assert.EqualValues(t, 4, h.replicas(t), "nothing decides: the replicas stay")
}
