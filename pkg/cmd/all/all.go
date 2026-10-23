// Package all implements the single-process KubeForecast deployment mode.
package all

import (
	"context"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	cmdbootstrap "github.com/cylonchau/kubeforecast/pkg/cmd/bootstrap"
	jobcmd "github.com/cylonchau/kubeforecast/pkg/cmd/job"
	servercmd "github.com/cylonchau/kubeforecast/pkg/cmd/server"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/spf13/cobra"
	"time"
)

type service struct {
	name string
	run  func(context.Context) error
}

type options struct {
	configFile   string
	sqlDriver    string
	maxWorkers   int
	pollInterval time.Duration
}

func NewCommand() *cobra.Command {
	opts := &options{maxWorkers: 10, pollInterval: 10 * time.Millisecond}
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Start all KubeForecast modules in one process",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return opts.run(cmd.Context())
		},
	}
	cmd.Flags().StringVarP(&opts.configFile, "config", "c", "./config.yaml", "The path to the configuration file.")
	cmd.Flags().StringVar(&opts.sqlDriver, "sql-driver", "sqlite", "Enable the selected SQL backend.")
	cmd.Flags().IntVar(&opts.maxWorkers, "max-workers", 10, "Maximum number of parallel job workers.")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll-interval", 10*time.Millisecond, "Interval to poll pending jobs.")
	_ = cmd.MarkFlagFilename("config", "yaml", "yml", "json")
	return cmd
}

func (o *options) run(ctx context.Context) error {
	if err := cmdbootstrap.LoadConfig(o.configFile); err != nil {
		return err
	}
	db, err := cmdbootstrap.InitializeDatabase(o.sqlDriver)
	if err != nil {
		return err
	}
	if err := utils.InitCasbin(); err != nil {
		return err
	}

	jobRuntime := jobcmd.NewRuntime(db, o.maxWorkers, o.pollInterval)
	return runServices(ctx, []service{
		{name: "platform", run: servercmd.RunPlatform},
		{name: "job", run: jobRuntime.Run},
	})
}

func runServices(parent context.Context, services []service) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(services))
	for _, current := range services {
		current := current
		go func() {
			results <- result{name: current.name, err: current.run(ctx)}
		}()
	}

	for range services {
		result := <-results
		if result.err != nil {
			cancel()
			return fmt.Errorf("%s module stopped: %w", result.name, result.err)
		}
		if parent.Err() == nil {
			logger.Warn("KubeForecast module stopped without process cancellation", logger.String("module", result.name))
			cancel()
		}
	}
	return nil
}
