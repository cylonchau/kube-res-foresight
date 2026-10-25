package notifytask

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks/notifyjob"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/notify"
)

// Task implements job.Task for JobType.
type Task struct{}

func New() *Task { return &Task{} }

func (t *Task) Type() string { return JobType }

func (t *Task) Execute(ctx context.Context, job *model.Job) error {
	var payload notifyjob.Payload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("invalid notify payload: %w", err)
	}
	if payload.Channel == "" || payload.Recipient == "" {
		return fmt.Errorf("notify payload missing channel or recipient")
	}

	engine := notify.GetEngine()
	if engine != nil {
		_, err := engine.Dispatch(ctx, notify.DeliveryRequest{
			NotificationID: fmt.Sprintf("job_%d", job.ID),
			Channel:        payload.Channel,
			Recipient:      payload.Recipient,
			Title:          payload.Title,
			Content:        payload.Content,
			Buttons:        payload.Buttons,

			Severity:        payload.Severity,
			DedupKey:        payload.DedupKey,
			EscalateChannel: payload.EscalateChannel,
		})
		return err
	}

	channel, ok := notify.Get(payload.Channel)
	if !ok {
		return fmt.Errorf("unknown notify channel %q", payload.Channel)
	}
	return channel.Send(ctx, payload.Recipient, notify.Message{
		Title:   payload.Title,
		Content: payload.Content,
		Buttons: payload.Buttons,
	})
}
