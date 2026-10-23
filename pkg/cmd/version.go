package cmd

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/version"
	"github.com/spf13/cobra"
)

func NewCmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of kubeforecast",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("KubeForecast version: %s\n", version.Version)
		},
	}
}
