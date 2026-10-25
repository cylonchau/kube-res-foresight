package job

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// Task is the unified abstraction for all concrete tasks
// Implementing a new task type only requires adhering to this interface and Registry addition
type Task interface {
	// Type returns the task identifier, must match Job.JobType
	Type() string

	// Execute runs the core task logic.
	// ⚑ Idempotence: Execute must be pure and idempotent without accumulative side-effects.
	// On error, Scheduler evaluates RetryCount vs MaxRetry to potentially reset to pending.
	// Error message is dumped via Job.Result.
	Execute(ctx context.Context, job *model.Job) error
}

// Registry maintains the global task catalog
var Registry = map[string]Task{}

func Register(t Task) {
	Registry[t.Type()] = t
}

func Get(jobType string) (Task, bool) {
	t, ok := Registry[jobType]
	return t, ok
}
