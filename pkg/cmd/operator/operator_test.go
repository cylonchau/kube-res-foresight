package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

// The operator creates no kind of its own: the scheme has the scalable workloads and nothing else to install in the cluster.
func TestSchemeKnowsTheScalableKindsAndNoCustomResource(t *testing.T) {
	scheme, err := NewScheme()
	require.NoError(t, err)
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("Deployment")))
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("StatefulSet")))
	for gvk := range scheme.AllKnownTypes() {
		assert.NotContains(t, gvk.Group, "kube-resource-fcst", "no custom resource of this project")
	}
}

func TestTheOperatorRefusesToStartWithoutAPlatform(t *testing.T) {
	err := Run(context.Background(), &Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--platform-url")
}

func TestThereIsNoFlagForACustomResourceController(t *testing.T) {
	assert.Nil(t, NewCommand().Flags().Lookup("predictivescaler-crd"))
}

func TestCommandDefaultsAreSafeForAReplicatedDeployment(t *testing.T) {
	cmd := NewCommand()
	assert.Equal(t, "operator", cmd.Name())
	leader, err := cmd.Flags().GetBool("leader-elect")
	require.NoError(t, err)
	assert.True(t, leader, "leader election is on by default so two replicas never scale the same target")
	assert.Error(t, cmd.Args(cmd, []string{"extra"}))
}
