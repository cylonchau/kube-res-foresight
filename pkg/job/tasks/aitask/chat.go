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

// ChatKind is the async task that answers one question of the assistant: the page does not hold a request open for it (a gateway would cut a long
// answer), it queues the question and reads the progress and the answer.
const ChatKind = "assistant.chat"

// ChatInput is what a chat task is given. The principal is who asked, as the permission system knows them when the question was queued: the worker
// has no request, and the tools it offers are the ones that user may use.
type ChatInput struct {
	Messages       []assistant.Turn       `json:"messages"`
	Lang           string                 `json:"lang"`
	Page           *assistant.PageContext `json:"page,omitempty"`
	ConversationID uint                   `json:"conversation_id"`
	OwnerID        uint                   `json:"owner_id"`
	OwnerName      string                 `json:"owner_name"`
	Principal      assistant.Principal    `json:"principal"`
}

// ChatHandler answers a queued question. In a stored conversation the question and the answer are kept in it, as for a question asked at once.
func ChatHandler(service *assistant.Service) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in ChatInput
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || len(in.Messages) == 0 {
			return nil, errors.New("the question of the task cannot be read")
		}
		ctx = assistant.WithOwner(ctx, assistant.Owner{ID: in.OwnerID, Name: in.OwnerName})
		ctx = assistant.WithPrincipal(ctx, in.Principal)
		ctx = assistant.WithProgress(ctx, func(kind string, data any) { rep.Event(kind, data) })
		// the answer is shown as it is written: the text so far, kept for the page at most four times a second
		ctx = assistant.WithTextStream(ctx, assistant.Throttled(rep.Partial, 250*time.Millisecond))
		rep.Progress(5, "asking")
		if in.ConversationID != 0 {
			return service.ChatConversation(ctx, in.ConversationID, in.Messages[len(in.Messages)-1].Content, in.Lang, in.Page)
		}
		return service.ChatIn(ctx, in.Messages, in.Lang, in.Page)
	}
}
