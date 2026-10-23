package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/cylonchau/kubeforecast/pkg/cmd"
	"github.com/spf13/pflag"
)

func main() {
	// The job binary is just a specialized entry point for the 'job' sub-command.
	// This reduces binary size and allows for cleaner containerized deployments.
	command := cmd.NewKubeForecastCommand(cmd.KubeForecastOptions{})

	// Force the command to 'job' if no subcommand is provided,
	// but here we just reuse the root command for simplicity while allowing 'job' specific flags.
	command.SetArgs(append([]string{"job"}, os.Args[1:]...))

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
