package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	cmdbootstrap "github.com/cylonchau/kubeforecast/pkg/cmd/bootstrap"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/migration"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type Options struct {
	ConfigFile string
	h          bool
	migration  bool
	upgrade    bool
	sqlDriver  string
	errCh      chan error
}

func NewOptions() *Options {
	return &Options{}
}

// NewCommand creates a *cobra.Command object with default parameters
func NewCommand() *cobra.Command {
	return newCommand("server")
}

// NewPlatformCommand exposes the same control plane under the architecture
// name used by the modular KubeForecast launcher.
func NewPlatformCommand() *cobra.Command {
	return newCommand("platform")
}

func newCommand(use string) *cobra.Command {
	opts := NewOptions()

	cmd := &cobra.Command{
		Use:   use,
		Short: "Start the KubeForecast management platform",
		Long:  `KubeForecast server provides KubeForecast platform services.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			PrintFlags(cmd.Flags())
			if err := opts.Complete(); err != nil {
				return fmt.Errorf("failed complete: %w", err)
			}

			if err := opts.Run(cmd.Context()); err != nil {
				logger.Error("Error running "+config.CONFIG.AppName, logger.Err(err))
				return err
			}

			return nil
		},
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if len(arg) > 0 {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}
			return nil
		},
	}

	fs := cmd.Flags()
	opts.AddFlags(fs)
	fs.AddGoFlagSet(flag.CommandLine)

	_ = cmd.MarkFlagFilename("config", "yaml", "yml", "json")

	return cmd
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVarP(&o.ConfigFile, "config", "c", "./config.yaml", "The path to the configuration file.")
	fs.BoolVar(&o.migration, "migration", false, "Initial database and tables.")
	fs.BoolVar(&o.upgrade, "upgrade", false, "If true, update the database schema to the latest version.")
	fs.StringVar(&o.sqlDriver, "sql-driver", "sqlite", "enable which sql backend.")
}

func PrintFlags(flags *pflag.FlagSet) {
	flags.VisitAll(func(flag *pflag.Flag) {
		logger.Debug(fmt.Sprintf("FLAG: --%s=%q", flag.Name, flag.Value))
	})
}

func (o *Options) Complete() error {
	if len(o.ConfigFile) == 0 {
		logger.Warn("Warning, all flags other than --config are deprecated")
	}
	// Load the config file here in Complete
	if len(o.ConfigFile) > 0 {
		return cmdbootstrap.LoadConfig(o.ConfigFile)
	}

	return nil
}

// jwtSecretConfigKey stores the auto-generated JWT signing key. It is
// deliberately hidden from the system-config admin API (see
// api.IsInternalSysConfigKey) — it is a credential, not a tunable.
const jwtSecretConfigKey = "sys.auth.jwt_secret"

// initJWTSecret installs the JWT signing key, in priority order:
//
//  1. server.jwt_secret / KUBEFORECAST_SERVER_JWT_SECRET — explicit and required
//     for multi-instance deployments, where every replica must agree on the
//     key or a token minted by one is rejected by the next.
//  2. a key previously persisted in sys_config — generated once on first
//     start and reused on every start after, so a single instance restarting
//     does NOT invalidate everyone's session.
//  3. a freshly generated key, persisted as (2) for next time.
//
// Only if the database is unreachable does it fall back to a per-process
// ephemeral key, and it says so loudly: that is the one mode where a restart
// logs everyone out.
func initJWTSecret(ctx context.Context) error {
	if config.CONFIG != nil && config.CONFIG.Server.JWTSecret != "" {
		utils.SetSecretKey([]byte(config.CONFIG.Server.JWTSecret))
		return nil
	}

	generate := func() (string, error) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("failed to generate a JWT secret: %w", err)
		}
		return hex.EncodeToString(raw), nil
	}

	if model.DB == nil {
		secret, err := generate()
		if err != nil {
			return err
		}
		utils.SetSecretKey([]byte(secret))
		logger.Warn("No database available to persist a JWT secret; using a random ephemeral key. " +
			"Every restart will invalidate all existing sessions. " +
			"Set server.jwt_secret (or KUBEFORECAST_SERVER_JWT_SECRET) to avoid this.")
		return nil
	}

	configDAO := rdb.NewSysConfigDAO(model.DB)
	if cfg, err := configDAO.GetByKey(ctx, jwtSecretConfigKey); err == nil && cfg != nil && cfg.ConfigValue != "" {
		utils.SetSecretKey([]byte(cfg.ConfigValue))
		return nil
	}

	secret, err := generate()
	if err != nil {
		return err
	}
	if err := configDAO.UpdateValue(ctx, jwtSecretConfigKey, secret); err != nil {
		// Persisting failed, so this key only lives for this process. Still
		// usable — the server can serve requests — but warn that sessions
		// won't survive the next restart.
		logger.Warn("Failed to persist the generated JWT secret; sessions will not survive a restart",
			logger.Any("err", err))
	} else {
		// UpdateValue has no name parameter, so the row it just inserted
		// carries ConfigName = jwtSecretConfigKey — fix that up to a real
		// label. Harmless either way since this key is hidden from the
		// admin UI, but worth getting right if it's ever inspected directly.
		if err := model.DB.Model(&model.SysConfig{}).
			Where("config_key = ?", jwtSecretConfigKey).
			Update("config_name", "JWT Signing Key").Error; err != nil {
			logger.Warn("Failed to set a display name for the generated JWT secret config row", logger.Any("err", err))
		}
		logger.Info("Generated and persisted a new JWT signing key (server.jwt_secret was not configured). " +
			"Sessions now survive restarts. For multi-instance deployments, set server.jwt_secret explicitly " +
			"so every replica agrees on the same key.")
	}
	utils.SetSecretKey([]byte(secret))
	return nil
}

func (o *Options) Run(ctx context.Context) error {
	// 1. Initialize DB Store first.
	if _, err := cmdbootstrap.InitializeDatabase(o.sqlDriver); err != nil {
		return err
	}
	if err := utils.InitCasbin(); err != nil {
		return err
	}

	// 2. Handle migration/upgrade commands
	if o.migration {
		return migration.Migrate(o.sqlDriver)
	}

	if o.upgrade {
		return migration.Upgrade(o.sqlDriver)
	}

	return RunPlatform(ctx)
}

// RunPlatform starts the control-plane services against an already initialized
// database. It is shared by the standalone platform command and the combined
// all-in-one runtime.
func RunPlatform(ctx context.Context) error {
	// Install the JWT signing key. This runs after the DB is up because an unconfigured key
	// is generated once and persisted, so restarting the server does not log
	// every user out.
	if err := initJWTSecret(ctx); err != nil {
		return err
	}

	// Operation log retention: intentionally NOT part of the separate
	// job engine (pkg/job/pkg/cmd/job) — the user asked for something that
	// just lives inside the main server process, so it's a plain background
	// goroutine tied to this process's lifetime.
	startOperationLogRetentionLoop(ctx, rdb.NewSysConfigDAO(model.DB), rdb.NewOperationLogDAO(model.DB), rdb.NewLoginAuditLogDAO(model.DB))

	// Start Application.
	startForecastMetrics(ctx)

	return app.NewHTTPServer(ctx)
}
