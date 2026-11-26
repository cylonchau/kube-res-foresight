package controller

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeRegistrar struct {
	settings clusteragent.Settings
	reports  []clusteragent.RegistrationBatch
	enabled  bool
}

func (f *fakeRegistrar) ClusterSettings(context.Context) (clusteragent.Settings, error) {
	return f.settings, nil
}

func (f *fakeRegistrar) ReportRegistrations(_ context.Context, b clusteragent.RegistrationBatch) (clusteragent.RegistrationResult, error) {
	f.reports = append(f.reports, b)
	return clusteragent.RegistrationResult{Enabled: f.enabled}, nil
}

func labelled(name, ns string, labels, annotations map[string]string, containers ...corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: annotations},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(4)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}}},
	}
}

func container(name, cpu, mem string) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}}
}

func registrar(t *testing.T, p *fakeRegistrar, objs ...client.Object) *WorkloadRegistrar {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return &WorkloadRegistrar{Reader: c, Platform: p, Operator: "op"}
}

var yes = map[string]string{clusteragent.LabelRegister: "true"}

// only the workloads that carry the label with the value "true" are candidates: another value, or none, leaves a workload alone
func TestOnlyWorkloadsWithTheLabelAreCandidates(t *testing.T) {
	p := &fakeRegistrar{settings: clusteragent.Settings{AutoRegister: true}, enabled: true}
	r := registrar(t, p,
		labelled("api", "shop", yes, map[string]string{clusteragent.AnnotationCopyFrom: " ref "}, container("app", "500m", "1Gi"), container("sidecar", "100m", "128Mi")),
		labelled("no", "shop", nil, nil),
		labelled("off", "shop", map[string]string{clusteragent.LabelRegister: "false"}, nil),
		labelled("one", "ops", yes, nil, container("main", "2", "2Gi")),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", Labels: yes}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(3))}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "kube-system", Labels: yes}},
	)
	got, err := r.Candidates(context.Background(), clusteragent.Scope{})
	require.NoError(t, err)
	require.Len(t, got, 4)
	byName := map[string]clusteragent.Candidate{}
	for _, c := range got {
		byName[c.Name] = c
	}
	api := byName["api"]
	assert.Equal(t, "Deployment", api.Kind)
	assert.Equal(t, int32(4), *api.Replicas)
	assert.Equal(t, int64(600), api.Requests.CPUMilli, "summed over the containers")
	assert.Equal(t, "", api.Container, "two containers: none is chosen")
	assert.Equal(t, "ref", api.CopyFrom)
	assert.Equal(t, "main", byName["one"].Container, "the one container is named")
	assert.Equal(t, "StatefulSet", byName["db"].Kind)
	assert.Nil(t, byName["agent"].Replicas, "a DaemonSet has no replicas")
	assert.NotContains(t, byName, "no")
	assert.NotContains(t, byName, "off")
}

// the namespaces the cluster collects from limit the candidates too
func TestTheNamespaceScopeLimitsTheCandidates(t *testing.T) {
	p := &fakeRegistrar{settings: clusteragent.Settings{AutoRegister: true}, enabled: true}
	r := registrar(t, p, labelled("a", "shop", yes, nil), labelled("b", "kube-system", yes, nil))
	got, err := r.Candidates(context.Background(), clusteragent.Scope{Exclude: []string{"kube-*"}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Name)
}

// the switch off: the cluster is not read at all, and nothing is reported
func TestWhileTheSwitchIsOffNothingIsListedOrReported(t *testing.T) {
	p := &fakeRegistrar{settings: clusteragent.Settings{AutoRegister: false}, enabled: true}
	r := registrar(t, p, labelled("a", "shop", yes, nil))
	r.Once(context.Background())
	assert.Empty(t, p.reports)
}

// a report is sent once; the same workloads are not sent again, a new one is, and turning the switch off and on says it all again
func TestAReportIsSentWhenWhatItHoldsChanges(t *testing.T) {
	p := &fakeRegistrar{settings: clusteragent.Settings{AutoRegister: true}, enabled: true}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(labelled("a", "shop", yes, nil)).Build()
	r := &WorkloadRegistrar{Reader: c, Platform: p}
	ctx := context.Background()
	r.Once(ctx)
	r.Once(ctx)
	require.Len(t, p.reports, 1, "the same list is not sent twice")
	require.NoError(t, c.Create(ctx, labelled("b", "shop", yes, nil)))
	r.Once(ctx)
	require.Len(t, p.reports, 2)
	assert.Len(t, p.reports[1].Workloads, 2)
	p.settings.AutoRegister = false
	r.Once(ctx)
	p.settings.AutoRegister = true
	r.Once(ctx)
	assert.Len(t, p.reports, 3, "turned off and on again, everything is reported again")
}

// a report the platform did not take (the switch turned off meanwhile) is not remembered as sent
func TestAReportThePlatformRefusedIsSentAgain(t *testing.T) {
	p := &fakeRegistrar{settings: clusteragent.Settings{AutoRegister: true}, enabled: false}
	r := registrar(t, p, labelled("a", "shop", yes, nil))
	r.Once(context.Background())
	p.enabled = true
	r.Once(context.Background())
	assert.Len(t, p.reports, 2)
}
