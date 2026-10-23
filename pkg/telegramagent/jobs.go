package telegramagent

import (
	"encoding/json"
	"sync"
	"time"
)

// Names of the TG Agent's jobs, as shown on the Agent status page.
const (
	JobForward     = "forward"     // deliver cached messages to the forward rules' targets, with retries
	JobCleanup     = "cleanup"     // delete consumed and expired cached messages
	JobConnections = "connections" // keep the client accounts connected
	JobConsumers   = "consumers"   // read the Redis and Kafka forward channels and hand their messages on
)

var allJobs = []string{JobForward, JobConsumers, JobCleanup, JobConnections}

// JobState is what one job reports about itself.
type JobState struct {
	Name      string     `json:"name"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastError string     `json:"last_error"`
	Runs      uint64     `json:"runs"`
	Failures  uint64     `json:"failures"`
	// Detail is a short human-readable summary of the last run.
	Detail string `json:"detail"`
}

// jobBoard records the state of the agent's jobs for the status heartbeat.
type jobBoard struct {
	mu   sync.Mutex
	jobs map[string]*JobState
}

func newJobBoard() *jobBoard {
	b := &jobBoard{jobs: map[string]*JobState{}}
	for _, name := range allJobs {
		b.jobs[name] = &JobState{Name: name}
	}
	return b
}

// Record notes one run of a job. A nil error clears the previous error.
func (b *jobBoard) Record(name string, err error, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	job := b.jobs[name]
	if job == nil {
		job = &JobState{Name: name}
		b.jobs[name] = job
	}
	now := time.Now()
	job.LastRunAt = &now
	job.Runs++
	job.Detail = detail
	job.LastError = ""
	if err != nil {
		job.Failures++
		job.LastError = err.Error()
	}
}

// Snapshot returns the job states in a stable order.
func (b *jobBoard) Snapshot() []JobState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]JobState, 0, len(b.jobs))
	for _, name := range allJobs {
		if job := b.jobs[name]; job != nil {
			out = append(out, *job)
		}
	}
	return out
}

// LastError returns the most recent job error, if any.
func (b *jobBoard) LastError() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, name := range allJobs {
		if job := b.jobs[name]; job != nil && job.LastError != "" {
			return job.Name + ": " + job.LastError
		}
	}
	return ""
}

func (b *jobBoard) JSON() string {
	raw, err := json.Marshal(b.Snapshot())
	if err != nil {
		return "[]"
	}
	return string(raw)
}
