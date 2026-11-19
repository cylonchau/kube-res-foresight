package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/aitask"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The async tasks of the assistant (P11 B1, B2, C5): a question that may take longer than a gateway allows is queued and answered in a few
// seconds with a task id; the page reads the progress and the answer, can cancel it, and picks it up again after a reload. Every call is bound to
// the user who asked: another user's task is a task that does not exist.

// AssistantAsyncDTO is a task as the page reads it.
type AssistantAsyncDTO struct {
	ID       uint   `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Stage    string `json:"stage"`
	Error    string `json:"error"`
	// Partial is the answer written so far, while the task runs.
	Partial    string          `json:"partial,omitempty"`
	Attempts   int             `json:"attempts"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Result     json.RawMessage `json:"result,omitempty"`
}

func asyncDTO(t *model.AsyncTask) AssistantAsyncDTO {
	out := AssistantAsyncDTO{ID: t.ID, Kind: t.Kind, Title: t.Title, Status: t.Status, Progress: t.Progress, Stage: t.Stage, Error: t.Error, Attempts: t.Attempts,
		CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt}
	if t.Status == model.AsyncRunning {
		out.Partial = t.Partial
	}
	if t.Status == model.AsyncSucceeded && json.Valid([]byte(t.Result)) {
		out.Result = json.RawMessage(t.Result)
	}
	return out
}

// asyncOwner is the signed-in user of the request, who the tasks belong to.
func asyncOwner(c *gin.Context) (assistant.Owner, bool) {
	o, ok := ownerOfRequest(c)
	if !ok {
		query.BadRequest(c, errors.New("async tasks are for a signed-in user only"))
	}
	return o, ok
}

func (r *AssistantRouter) asyncFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		query.ErrorResponse(c, http.StatusNotFound, errors.New("the task does not exist"))
	case errors.Is(err, asyncrun.ErrTooMany):
		query.ErrorResponse(c, http.StatusTooManyRequests, err)
	case errors.Is(err, asyncrun.ErrNotFinal):
		query.ErrorResponse(c, http.StatusConflict, err)
	default:
		r.reply(c, nil, err)
	}
}

// ChatAsync serves POST /assistant/chat/async: the same request as /assistant/chat, queued. It answers at once with the task.
func (r *AssistantRouter) ChatAsync(c *gin.Context) {
	if r.Async == nil {
		query.InternalError(c, errors.New("async tasks are not set up"))
		return
	}
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	var req AssistantChatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := withOwner(c)
	// a question that cannot be asked (off, no model, not valid, not the user's conversation) is refused now, not after a wait
	if err := r.Service.Preflight(ctx, req.Messages, req.ConversationID); err != nil {
		r.asyncFailure(c, err)
		return
	}
	principal, _ := principalOfRequest(c)
	in := aitask.ChatInput{Messages: req.Messages, Lang: assistantLang(req.Lang), Page: req.Context.Clean(), ConversationID: req.ConversationID,
		OwnerID: owner.ID, OwnerName: owner.Name, Principal: principal}
	question := req.Messages[len(req.Messages)-1].Content
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s", owner.ID, req.ConversationID, question)))
	task, err := r.Async.Submit(ctx, owner.ID, owner.Name, aitask.ChatKind, assistant.TitleOf(question), "chat-"+hex.EncodeToString(sum[:8]), in)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, asyncDTO(task))
}

// GetAsync serves GET /assistant/async/:id.
func (r *AssistantRouter) GetAsync(c *gin.Context) {
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	task, err := r.Async.Tasks.GetOwned(c.Request.Context(), owner.ID, id)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, asyncDTO(task))
}

// AsyncEvents serves GET /assistant/async/:id/events?after=<seq>: what the task reported after the last step the page saw.
func (r *AssistantRouter) AsyncEvents(c *gin.Context) {
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	after, _ := strconv.Atoi(c.DefaultQuery("after", "0"))
	rows, err := r.Async.Tasks.EventsAfter(c.Request.Context(), owner.ID, id, after, 200)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	type event struct {
		Seq  int             `json:"seq"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	out := make([]event, 0, len(rows))
	for _, row := range rows {
		item := event{Seq: row.Seq, Type: row.Type}
		if json.Valid([]byte(row.Data)) {
			item.Data = json.RawMessage(row.Data)
		}
		out = append(out, item)
	}
	query.SuccessResponse(c, nil, out)
}

// CancelAsync serves POST /assistant/async/:id/cancel.
func (r *AssistantRouter) CancelAsync(c *gin.Context) {
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	task, err := r.Async.Cancel(c.Request.Context(), owner.ID, id)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, asyncDTO(task))
}

// RetryAsync serves POST /assistant/async/:id/retry: a failed or cancelled task is queued again with the same question.
func (r *AssistantRouter) RetryAsync(c *gin.Context) {
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	task, err := r.Async.Retry(c.Request.Context(), owner.ID, id)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	query.SuccessResponse(c, nil, asyncDTO(task))
}
