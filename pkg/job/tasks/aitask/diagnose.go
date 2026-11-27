package aitask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// DiagnoseKind is the async task that diagnoses one change of a cluster: a model answers in tens of seconds, longer than a gateway holds a request, so the page
// queues the diagnosis and reads the progress and the answer, as it does for a question of the chat.
const DiagnoseKind = "cluster.diagnose"

// DiagnoseInput is what a diagnosis is given: the change (the facts about it are made by the task, so the request that queues it does no more than check that it exists and
// answers at once), the language and who asked. A task queued by an earlier version carries the facts themselves.
type DiagnoseInput struct {
	ClusterID uint                `json:"cluster_id,omitempty"`
	ChangeID  uint                `json:"change_id,omitempty"`
	Facts     string              `json:"facts,omitempty"`
	Lang      string              `json:"lang"`
	OwnerID   uint                `json:"owner_id"`
	OwnerName string              `json:"owner_name"`
	Principal assistant.Principal `json:"principal"`
}

// FactsFunc makes the facts about one change of a cluster, as the text the model is given.
type FactsFunc func(ctx context.Context, clusterID, changeID uint) (string, error)

// DiagnoseHandler runs a queued diagnosis; the answer is shown as it is written. The facts are made here, in the task, from the stored change and the events and changes around it.
func DiagnoseHandler(service *assistant.Service, facts FactsFunc) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in DiagnoseInput
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || (in.Facts == "" && (in.ClusterID == 0 || in.ChangeID == 0)) {
			return nil, errors.New("the change to diagnose cannot be read")
		}
		if in.Facts == "" {
			if facts == nil {
				return nil, errors.New("the facts of a change cannot be made here")
			}
			rep.Progress(2, "reading")
			made, err := facts(ctx, in.ClusterID, in.ChangeID)
			if err != nil {
				return nil, fmt.Errorf("read the change: %w", err)
			}
			in.Facts = made
		}
		ctx = assistant.WithOwner(ctx, assistant.Owner{ID: in.OwnerID, Name: in.OwnerName})
		ctx = assistant.WithPrincipal(ctx, in.Principal)
		ctx = assistant.WithProgress(ctx, func(kind string, data any) { rep.Event(kind, data) })
		ctx = assistant.WithTextStream(ctx, assistant.Throttled(rep.Partial, 250*time.Millisecond))
		rep.Progress(5, "asking")
		return service.Diagnose(ctx, in.Facts, in.Lang)
	}
}
