// Command kube-res-foresight-operator scales the workloads of the platform modules. It does not link the web assets,
// so the image stays small and needs nothing but cluster access.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	operatorcmd "github.com/cylonchau/kube-res-foresight/pkg/cmd/operator"
)

func main() {
	command := operatorcmd.NewCommand()
	command.Use = "kube-res-foresight-operator"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command.SetContext(ctx)
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
