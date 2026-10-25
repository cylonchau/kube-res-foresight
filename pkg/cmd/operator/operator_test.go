package operator

import (
	"testing"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

func TestSchemeKnowsThePredictiveScalerAndTheScalableKinds(t *testing.T) {
	scheme, err := NewScheme()
	require.NoError(t, err)
	assert.True(t, scheme.Recognizes(v1alpha1.GroupVersion.WithKind("PredictiveScaler")))
	assert.True(t, scheme.Recognizes(appsv1.SchemeGroupVersion.WithKind("Deployment")))
}

func TestCommandDefaultsAreSafeForAReplicatedDeployment(t *testing.T) {
	cmd := NewCommand()
	assert.Equal(t, "operator", cmd.Name())
	leader, err := cmd.Flags().GetBool("leader-elect")
	require.NoError(t, err)
	assert.True(t, leader, "leader election is on by default so two replicas never scale the same target")
	assert.Error(t, cmd.Args(cmd, []string{"extra"}))
}
