package job

import (
	"context"
	"flag"
	"fmt"
	cmdbootstrap "github.com/cylonchau/kube-resource-fcst/pkg/cmd/bootstrap"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	jobengine "github.com/cylonchau/kube-resource-fcst/pkg/job"
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gorm.io/gorm"
	"time"
)

type Options struct {
	ConfigFile   string
	sqlDriver    string
	MaxWorkers   int
	PollInterval time.Duration
	migration    bool
}

func NewOptions() *Options { return &Options{MaxWorkers: 10, PollInterval: 10 * time.Millisecond} }

func NewCommand() *cobra.Command {
	opts := NewOptions()
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Start the kube-resource-fcst background job engine",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(); err != nil {
				return err
			}
			return opts.Run(cmd.Context())
		},
	}
	opts.AddFlags(cmd.Flags())
	return cmd
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ConfigFile, "config", "./config.yaml", "The path to the configuration file.")
	fs.StringVar(&o.sqlDriver, "sql-driver", "sqlite", "Enable the selected SQL backend.")
	fs.IntVar(&o.MaxWorkers, "max-workers", 10, "Maximum number of parallel workers.")
	fs.DurationVar(&o.PollInterval, "poll-interval", 10*time.Millisecond, "Interval to poll pending jobs.")
	fs.BoolVar(&o.migration, "migration", false, "Migrate generic job tables, then exit.")
	fs.AddGoFlagSet(flag.CommandLine)
}

func (o *Options) Complete() error {
	if len(o.ConfigFile) == 0 {
		return nil
	}
	return cmdbootstrap.LoadConfig(o.ConfigFile)
}

func (o *Options) Run(ctx context.Context) error {
	db, err := cmdbootstrap.InitializeDatabase(o.sqlDriver)
	if err != nil {
		return err
	}
	if o.migration {
		return db.AutoMigrate(&model.Job{}, &model.TaskDefinition{})
	}

	runtime := NewRuntime(db, o.MaxWorkers, o.PollInterval)
	return runtime.Run(ctx)
}

// Runtime contains the initialized generic job engine. Construction performs
// synchronous task registration so an all-in-one process cannot race platform
// requests against notification handler registration.
type Runtime struct {
	taskDAO   *rdb.TaskDefinitionDAO
	jobDAO    *rdb.JobDAO
	scheduler *jobengine.Scheduler
}

func NewRuntime(db *gorm.DB, maxWorkers int, pollInterval time.Duration) *Runtime {
	jobDAO := rdb.NewJobDAO(db)
	taskDAO := rdb.NewTaskDefinitionDAO(db)
	tasks.RegisterAll(rdb.NewSysConfigDAO(db))
	scheduler := jobengine.NewScheduler(jobDAO, maxWorkers, pollInterval)
	return &Runtime{taskDAO: taskDAO, jobDAO: jobDAO, scheduler: scheduler}
}

func (r *Runtime) Run(ctx context.Context) error {
	logger.Info("Starting kube-resource-fcst background engine")
	// Scheduled runs that piled up while the engine was behind are closed: only the newest of a task is worth running.
	if closed, err := r.jobDAO.SupersedeStaleScheduled(ctx); err != nil {
		logger.Warn("Closing the scheduled runs that piled up failed", logger.Err(err))
	} else if closed > 0 {
		logger.Info("Closed scheduled runs that had piled up", logger.Int64("count", closed))
	}
	go runTaskScanner(ctx, r.taskDAO, r.jobDAO)
	r.scheduler.Start(ctx)
	return nil
}

func runTaskScanner(ctx context.Context, taskDAO *rdb.TaskDefinitionDAO, jobDAO *rdb.JobDAO) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		nextDelay := time.Second
		definitions, err := taskDAO.ListEnabled(ctx)
		if err != nil {
			logger.Error("Failed to list task definitions", logger.Err(err))
			timer.Reset(nextDelay)
			continue
		}

		now := time.Now()
		for i := range definitions {
			definition := &definitions[i]
			schedule, err := jobengine.ParseCron(definition.CronExpr)
			if err != nil {
				logger.Error("Invalid cron expression", logger.String("task_type", definition.TaskType), logger.String("cron", definition.CronExpr), logger.Err(err))
				continue
			}
			if definition.NextRunTime == nil {
				next := schedule.Next(now)
				if err := taskDAO.SetNextRunTime(ctx, definition.ID, next); err != nil {
					logger.Error("Failed to initialize task schedule", logger.Err(err))
					continue
				}
				definition.NextRunTime = &next
			}
			if definition.NextRunTime.After(now) {
				if delay := time.Until(*definition.NextRunTime); delay < nextDelay {
					nextDelay = delay
				}
				continue
			}

			runAt := *definition.NextRunTime
			next := schedule.Next(runAt)
			// The last run of this task has not finished: this one is skipped, not stacked behind it.
			if open, err := jobDAO.HasOpenJob(ctx, definition.TaskType, "task_definition", int64(definition.ID)); err == nil && open {
				if err := taskDAO.UpdateRunTimes(ctx, definition.ID, runAt, next); err != nil {
					logger.Error("Failed to persist task run times", logger.String("task_type", definition.TaskType), logger.Err(err))
				}
				if delay := time.Until(next); delay < nextDelay {
					nextDelay = delay
				}
				continue
			}
			queued := &model.Job{
				JobType: definition.TaskType, Status: "pending", ScheduledAt: &runAt,
				Payload: definition.Params, MaxRetry: definition.MaxRetry,
				RetryForever: definition.RetryForever, RetryInitialSeconds: definition.RetryInitialSeconds,
				RetryBackoffFactor: definition.RetryBackoffFactor, RetryMaxSeconds: definition.RetryMaxSeconds,
				IdempotencyKey: fmt.Sprintf("task-%d-%d", definition.ID, runAt.UnixNano()),
				TargetType:     "task_definition", TargetID: int64(definition.ID), TargetName: definition.Description,
			}
			if err := jobDAO.CreateIdempotent(ctx, queued); err != nil {
				logger.Error("Failed to enqueue scheduled task", logger.String("task_type", definition.TaskType), logger.Err(err))
				continue
			}
			if err := taskDAO.UpdateRunTimes(ctx, definition.ID, runAt, next); err != nil {
				logger.Error("Failed to persist task run times", logger.String("task_type", definition.TaskType), logger.Err(err))
			}
			if delay := time.Until(next); delay < nextDelay {
				nextDelay = delay
			}
		}
		if nextDelay < time.Millisecond {
			nextDelay = time.Millisecond
		}
		timer.Reset(nextDelay)
	}
}
