package cmd

import (
	allcmd "github.com/cylonchau/kube-resource-fcst/pkg/cmd/all"
	"github.com/cylonchau/kube-resource-fcst/pkg/cmd/job"
	notifycmd "github.com/cylonchau/kube-resource-fcst/pkg/cmd/notify"
	operatorcmd "github.com/cylonchau/kube-resource-fcst/pkg/cmd/operator"
	"github.com/cylonchau/kube-resource-fcst/pkg/cmd/server"
	"github.com/spf13/cobra"
)

type KubeResourceFcstOptions struct{}

func NewKubeResourceFcstCommand(o KubeResourceFcstOptions) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kube-resource-fcst",
		Short: "Kubernetes resource forecasting and Pod scaling orchestration",
		Long:  "kube-resource-fcst provides the management foundation for Kubernetes resource forecasting and Pod scaling.",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
		},
	}

	rootCmd.AddCommand(
		NewCmdVersion(),
		allcmd.NewCommand(),
		server.NewPlatformCommand(),
		server.NewCommand(),
		job.NewCommand(),
		notifycmd.NewCommand(),
		operatorcmd.NewCommand(),
	)
	return rootCmd
}
