// Command kubeforecast-operator runs only the PredictiveScaler controller. It does not link the web assets,
// so the image stays small and needs nothing but cluster access.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	operatorcmd "github.com/cylonchau/kubeforecast/pkg/cmd/operator"
)

func main() {
	command := operatorcmd.NewCommand()
	command.Use = "kubeforecast-operator"
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command.SetContext(ctx)
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
