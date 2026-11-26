package aitask

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// DiagnoseKind is the async task that diagnoses one change of a cluster: a model answers in tens of seconds, longer than a gateway holds a request, so the page
// queues the diagnosis and reads the progress and the answer, as it does for a question of the chat.
const DiagnoseKind = "cluster.diagnose"

// DiagnoseInput is what a diagnosis is given: the facts the platform holds (made when it was queued), the language and who asked.
type DiagnoseInput struct {
	Facts     string              `json:"facts"`
	Lang      string              `json:"lang"`
	OwnerID   uint                `json:"owner_id"`
	OwnerName string              `json:"owner_name"`
	Principal assistant.Principal `json:"principal"`
}

// DiagnoseHandler runs a queued diagnosis; the answer is shown as it is written.
func DiagnoseHandler(service *assistant.Service) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in DiagnoseInput
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || in.Facts == "" {
			return nil, errors.New("the facts of the diagnosis cannot be read")
		}
		ctx = assistant.WithOwner(ctx, assistant.Owner{ID: in.OwnerID, Name: in.OwnerName})
		ctx = assistant.WithPrincipal(ctx, in.Principal)
		ctx = assistant.WithProgress(ctx, func(kind string, data any) { rep.Event(kind, data) })
		ctx = assistant.WithTextStream(ctx, assistant.Throttled(rep.Partial, 250*time.Millisecond))
		rep.Progress(5, "asking")
		return service.Diagnose(ctx, in.Facts, in.Lang)
	}
}
