package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/cylonchau/kube-resource-fcst/pkg/cmd"
	"github.com/spf13/pflag"
)

func main() {
	command := cmd.NewKubeResourceFcstCommand(cmd.KubeResourceFcstOptions{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Restore the default signal behavior after the first cancellation so a
	// second Ctrl+C can still force termination if graceful shutdown is stuck.
	go func() {
		<-ctx.Done()
		stop()
	}()
	command.SetContext(ctx)
	flagset := flag.CommandLine
	pflag.CommandLine.AddGoFlagSet(flagset)
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
