// Package aitask holds the two jobs of the AI scheduled tasks. They are the whole of the scheduling: ai.schedule (a task of the task center,
// every minute) finds the AI tasks whose time has come and queues an ai.run job for each; ai.run asks the language model the prompt of the
// task, keeps the answer and mails it (see pkg/assistant). A run is a job like any other, so it has the retries, the back-off and the audit of
// the task center.
package aitask

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const (
	// ScheduleJobType is the job of the task center that queues the AI tasks that are due.
	ScheduleJobType = "ai.schedule"
	// RunJobType is the job that runs one AI task.
	RunJobType = "ai.run"
	// TargetType is the kind of target of a run in the task center.
	TargetType = "ai_task"

	maxRetry = 2
)

// Payload is the wire contract of an ai.run job.
type Payload struct {
	TaskID uint `json:"task_id"`
	// Trigger is "schedule" or "manual".
	Trigger string `json:"trigger"`
}

// Queue queues the runs, through the job table. It is the assistant.JobQueue of the scheduler, and the way a run is asked for by hand.
type Queue struct {
	Jobs *rdb.JobDAO
}

func (q *Queue) enqueue(ctx context.Context, taskID uint, name, trigger string, key string, at *time.Time) error {
	payload, err := json.Marshal(Payload{TaskID: taskID, Trigger: trigger})
	if err != nil {
		return err
	}
	return q.Jobs.CreateIdempotent(ctx, &model.Job{JobType: RunJobType, Status: "pending", ScheduledAt: at, Payload: string(payload), MaxRetry: maxRetry,
		RetryInitialSeconds: 60, RetryBackoffFactor: 2, RetryMaxSeconds: 900, IdempotencyKey: key, TargetType: TargetType, TargetID: int64(taskID), TargetName: name})
}

// Enqueue implements assistant.JobQueue: the time the schedule fired makes the job unique, so a tick that runs twice queues one run.
func (q *Queue) Enqueue(ctx context.Context, taskID uint, name string, runAt time.Time) error {
	return q.enqueue(ctx, taskID, name, "schedule", fmt.Sprintf("ai-run-%d-%d", taskID, runAt.Unix()), &runAt)
}

// EnqueueNow queues a run that somebody asked for.
func (q *Queue) EnqueueNow(ctx context.Context, taskID uint, name string) error {
	return q.enqueue(ctx, taskID, name, "manual", fmt.Sprintf("ai-run-%d-manual-%d", taskID, time.Now().UnixNano()), nil)
}

// HasOpen implements assistant.JobQueue.
func (q *Queue) HasOpen(ctx context.Context, taskID uint) (bool, error) {
	return q.Jobs.HasOpenJob(ctx, RunJobType, TargetType, int64(taskID))
}

// Schedule is the ai.schedule job.
type Schedule struct {
	Service *assistant.Service
	Queue   *Queue
}

func (t *Schedule) Type() string { return ScheduleJobType }

// Execute queues the AI tasks that are due. An error is returned only when the tasks could not be read, so the job is tried again.
func (t *Schedule) Execute(ctx context.Context, _ *model.Job) error {
	n, err := t.Service.ScheduleDue(ctx, t.Queue)
	if n > 0 {
		logger.Info("queued AI scheduled tasks", logger.Int("count", n))
	}
	return err
}

// Run is the ai.run job.
type Run struct {
	Service *assistant.Service
}

func (t *Run) Type() string { return RunJobType }

// Execute runs the task of the payload. The error of a run that a retry can mend (the model or the mail server did not answer) makes the
// job engine try again, which continues the same run.
func (t *Run) Execute(ctx context.Context, job *model.Job) error {
	var p Payload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil || p.TaskID == 0 {
		return fmt.Errorf("invalid ai.run payload: %v", err)
	}
	return t.Service.RunTask(ctx, p.TaskID, uint(job.ID), p.Trigger)
}
