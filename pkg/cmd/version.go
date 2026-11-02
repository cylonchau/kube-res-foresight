package cmd

import (
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/version"
	"github.com/spf13/cobra"
)

func NewCmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of kube-res-foresight",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("kube-res-foresight version: %s\n", version.Version)
		},
	}
}
