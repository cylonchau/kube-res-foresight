// Package notify implements the independently deployable notification runtime command.
package notify

import (
	"context"
	"fmt"
	cmdbootstrap "github.com/cylonchau/kubeforecast/pkg/cmd/bootstrap"
	notifyruntime "github.com/cylonchau/kubeforecast/pkg/notify"
	"github.com/cylonchau/kubeforecast/pkg/notify/voice"
	"github.com/cylonchau/kubeforecast/pkg/notify/webhook"
	"github.com/spf13/cobra"
	"time"
)

func NewCommand() *cobra.Command {
	var configFile string
	var sqlDriver string
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Start the KubeForecast notification runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), configFile, sqlDriver)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "./config.yaml", "The path to the configuration file.")
	cmd.Flags().StringVar(&sqlDriver, "sql-driver", "sqlite", "Enable the selected SQL backend.")
	_ = cmd.MarkFlagFilename("config", "yaml", "yml", "json")
	return cmd
}

func run(ctx context.Context, configFile, sqlDriver string) error {
	if err := cmdbootstrap.LoadConfig(configFile); err != nil {
		return fmt.Errorf("initialize notify module: %w", err)
	}
	if _, err := cmdbootstrap.InitializeDatabase(sqlDriver); err != nil {
		return fmt.Errorf("initialize notify database: %w", err)
	}
	if _, ok := notifyruntime.Get(webhook.ChannelWebhook); !ok {
		notifyruntime.Register(webhook.NewNotifier(5 * time.Second))
	}
	if _, ok := notifyruntime.Get(voice.ChannelVoice); !ok {
		notifyruntime.Register(voice.NewNotifier(nil))
	}
	return notifyruntime.Run(ctx)
}
