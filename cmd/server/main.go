package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	servercmd "github.com/cylonchau/kubeforecast/pkg/cmd/server"
)

func main() {
	command := servercmd.NewCommand()
	command.Use = "kubeforecast-server"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	command.SetContext(ctx)

	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
