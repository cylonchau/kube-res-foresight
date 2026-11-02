package cmd

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestKubeResForesightCommandExposesArchitectureModules(t *testing.T) {
	root := NewKubeResForesightCommand(KubeResForesightOptions{})
	names := make([]string, 0, len(root.Commands()))
	for _, command := range root.Commands() {
		names = append(names, command.Name())
	}

	assert.Equal(t, "kube-res-foresight", root.Name())
	assert.ElementsMatch(t, []string{"all", "platform", "job", "server", "notify", "operator", "version"}, names)
}

func TestKubeResForesightCommandWithoutSubcommandPrintsHelp(t *testing.T) {
	root := NewKubeResForesightCommand(KubeResForesightOptions{})
	output := &bytes.Buffer{}
	root.SetOut(output)
	// Cobra treats nil as "read process arguments", which makes this test
	// accidentally parse go test's -coverprofile value as a subcommand.
	root.SetArgs([]string{})

	assert.NoError(t, root.Execute())
	assert.Contains(t, output.String(), "Kubernetes resource forecasting")
}
