package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

func withContainer(d *appsv1.Deployment) *appsv1.Deployment {
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}}}}
	d.Spec.Template.Spec.NodeSelector = map[string]string{"pool": "old"}
	return d
}

func specOf(t *testing.T, h *harness) corev1.PodSpec {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &d))
	return d.Spec.Template.Spec
}

func decisionWithSpec(replicas int32, spec *PodSpecChange) *Decision {
	d := fresh(replicas)
	d.PodSpec = spec
	return d
}

var wanted = &PodSpecChange{Version: 3, CPUMilli: 500, MemoryBytes: 1 << 30, LimitMemoryBytes: 2 << 30, NodePool: "mem-32g",
	NodeSelector: map[string]string{"pool": "mem"}, DropSelectorKeys: []string{"gpu"}}

// The operator writes the replicas and nothing else unless it was started to adjust the Pod spec.
func TestThePodSpecIsLeftAloneUnlessTheOperatorIsToAdjustIt(t *testing.T) {
	h := newHarness(t, fakeDecider{d: decisionWithSpec(4, wanted)}, withContainer(deployment(4, 4)))
	h.evaluate(scaler())
	got := specOf(t, h)
	assert.Equal(t, "100m", got.Containers[0].Resources.Requests.Cpu().String())
	assert.Equal(t, "old", got.NodeSelector["pool"])
}

func TestThePodSpecOfAModuleInAutoModeIsWrittenWhenTheOperatorIsToAdjustIt(t *testing.T) {
	h := newHarness(t, fakeDecider{d: decisionWithSpec(4, wanted)}, withContainer(deployment(4, 4)))
	h.a.AdjustPodSpec = true
	h.evaluate(scaler())
	got := specOf(t, h)
	c := got.Containers[0].Resources
	assert.Equal(t, "500m", c.Requests.Cpu().String())
	assert.EqualValues(t, 1<<30, c.Requests.Memory().Value())
	assert.EqualValues(t, 2<<30, c.Limits.Memory().Value(), "the Limit the module manages")
	assert.True(t, c.Limits.Cpu().IsZero(), "a Limit the module does not manage is not invented")
	assert.Equal(t, "mem", got.NodeSelector["pool"])
	require.Len(t, h.reporter.reports, 1)
	assert.Contains(t, h.reporter.reports[0].Reason, "Pod spec written")
	assert.Equal(t, "applied", h.reporter.reports[0].Result)

	// the same answer again changes nothing and says nothing
	h.evaluate(scaler())
	assert.Len(t, h.reporter.reports, 1)
}

func TestAPodSpecFromAnAnswerTheOperatorRemembersIsNeverWritten(t *testing.T) {
	d := decisionWithSpec(4, wanted)
	d.Cached = true
	h := newHarness(t, fakeDecider{d: d}, withContainer(deployment(4, 4)))
	h.a.AdjustPodSpec = true
	h.evaluate(scaler())
	assert.Equal(t, "100m", specOf(t, h).Containers[0].Resources.Requests.Cpu().String())
	d.Cached, d.Recovering = false, true
	h.evaluate(scaler())
	assert.Equal(t, "100m", specOf(t, h).Containers[0].Resources.Requests.Cpu().String(), "an answer from a platform that has just come back is not followed either")
}

func TestAdjustingAPodSpecMovesTheNodeSelectorAndKeepsARequestBelowItsLimit(t *testing.T) {
	spec := corev1.PodSpec{NodeSelector: map[string]string{"gpu": "a100", "pool": "old", "zone": "z1"}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")}}}}}
	changed := adjustPodSpec(&spec, &PodSpecChange{CPUMilli: 500, NodePool: "mem", NodeSelector: map[string]string{"pool": "mem"}, DropSelectorKeys: []string{"gpu", "pool"}})
	assert.Equal(t, map[string]string{"pool": "mem", "zone": "z1"}, spec.NodeSelector, "the labels of the pool it leaves are dropped, the others stay")
	assert.True(t, spec.Containers[0].Resources.Requests.Cpu().IsZero(), "a Request above the Limit of the container is not written")
	require.NotEmpty(t, changed)
	joined := ""
	for _, c := range changed {
		joined += c + ";"
	}
	assert.Contains(t, joined, leftAlone)
	assert.Contains(t, joined, "-gpu")
}
