package cmd

import (
	allcmd "github.com/cylonchau/kubeforecast/pkg/cmd/all"
	"github.com/cylonchau/kubeforecast/pkg/cmd/job"
	notifycmd "github.com/cylonchau/kubeforecast/pkg/cmd/notify"
	operatorcmd "github.com/cylonchau/kubeforecast/pkg/cmd/operator"
	"github.com/cylonchau/kubeforecast/pkg/cmd/server"
	"github.com/spf13/cobra"
)

type KubeForecastOptions struct{}

func NewKubeForecastCommand(o KubeForecastOptions) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kubeforecast",
		Short: "Kubernetes resource forecasting and Pod scaling orchestration",
		Long:  "KubeForecast provides the management foundation for Kubernetes resource forecasting and Pod scaling.",
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
