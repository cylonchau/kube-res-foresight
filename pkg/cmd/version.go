package cmd

import (
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/version"
	"github.com/spf13/cobra"
)

func NewCmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of kube-resource-fcst",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("kube-resource-fcst version: %s\n", version.Version)
		},
	}
}
