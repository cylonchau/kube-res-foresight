package cmd

import (
	allcmd "github.com/cylonchau/kube-res-foresight/pkg/cmd/all"
	"github.com/cylonchau/kube-res-foresight/pkg/cmd/job"
	notifycmd "github.com/cylonchau/kube-res-foresight/pkg/cmd/notify"
	operatorcmd "github.com/cylonchau/kube-res-foresight/pkg/cmd/operator"
	"github.com/cylonchau/kube-res-foresight/pkg/cmd/server"
	"github.com/spf13/cobra"
)

type KubeResForesightOptions struct{}

func NewKubeResForesightCommand(o KubeResForesightOptions) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kube-res-foresight",
		Short: "Kubernetes resource forecasting and Pod scaling orchestration",
		Long:  "kube-res-foresight provides the management foundation for Kubernetes resource forecasting and Pod scaling.",
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
