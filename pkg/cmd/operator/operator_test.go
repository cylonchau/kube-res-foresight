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
		assert.NotContains(t, gvk.Group, "kube-res-foresight", "no custom resource of this project")
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

// An operator of the release before the rename and this one lead on the same lease, so they never scale the same module together.
func TestTheLeaseIsTheOneEarlierReleasesUsed(t *testing.T) {
	id, err := NewCommand().Flags().GetString("leader-election-id")
	require.NoError(t, err)
	assert.Equal(t, "kube-resource-fcst-operator.autoscaling.kube-resource-fcst.io", id)
	assert.Equal(t, DefaultLeaderElectionID, id)
}
