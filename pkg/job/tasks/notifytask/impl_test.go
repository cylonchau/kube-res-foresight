package notifytask

import (
	"context"
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/job/tasks/notifyjob"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	"testing"
)

type recordingNotifier struct {
	channel   string
	recipient string
	msg       notify.Message
	sendErr   error
}

func (n *recordingNotifier) Channel() string { return n.channel }

func (n *recordingNotifier) Recipients(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (n *recordingNotifier) Send(ctx context.Context, recipient string, msg notify.Message) error {
	n.recipient = recipient
	n.msg = msg
	return n.sendErr
}

func TestTaskExecuteDispatchesToRegisteredChannel(t *testing.T) {
	rec := &recordingNotifier{channel: "test-channel"}
	notify.Register(rec)

	payload, err := json.Marshal(notifyjob.Payload{
		Channel:   "test-channel",
		Recipient: "12345",
		Title:     "t",
		Content:   "c",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	task := New()
	job := &model.Job{JobType: JobType, Payload: string(payload)}
	if err := task.Execute(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.recipient != "12345" || rec.msg.Content != "c" {
		t.Fatalf("channel did not receive expected message: %#v", rec)
	}
}

func TestTaskExecuteUnknownChannel(t *testing.T) {
	payload, _ := json.Marshal(notifyjob.Payload{Channel: "does-not-exist", Recipient: "x"})
	task := New()
	job := &model.Job{JobType: JobType, Payload: string(payload)}
	if err := task.Execute(context.Background(), job); err == nil {
		t.Fatal("expected error for unknown notify channel")
	}
}

func TestTaskExecuteMissingFields(t *testing.T) {
	task := New()
	job := &model.Job{JobType: JobType, Payload: `{}`}
	if err := task.Execute(context.Background(), job); err == nil {
		t.Fatal("expected error when channel/recipient are missing")
	}
}

func TestTaskExecuteWithEngine(t *testing.T) {
	rec := &recordingNotifier{channel: "engine-channel"}
	notify.Register(rec)

	engine := notify.NewEngine(nil)
	notify.SetEngine(engine)
	defer notify.SetEngine(nil)

	payload, err := json.Marshal(notifyjob.Payload{
		Channel:   "engine-channel",
		Recipient: "67890",
		Title:     "EngTitle",
		Content:   "EngContent",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	task := New()
	job := &model.Job{ID: 99, JobType: JobType, Payload: string(payload)}
	if err := task.Execute(context.Background(), job); err != nil {
		t.Fatalf("unexpected error with engine: %v", err)
	}
	if rec.recipient != "67890" || rec.msg.Content != "EngContent" {
		t.Fatalf("channel did not receive expected message via engine: %#v", rec)
	}
}
