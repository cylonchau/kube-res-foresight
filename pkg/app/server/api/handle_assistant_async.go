package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	Partial    string     `json:"partial,omitempty"`
	Attempts   int        `json:"attempts"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// MaxAttempts is how many times the task is run for a failure that can be tried again; Retryable says the last failure was one, and NextRunAt when
	// the next attempt starts while the task waits for it.
	MaxAttempts int        `json:"max_attempts"`
	Retryable   bool       `json:"retryable"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	// HeartbeatAt is the last time the worker said it was alive; ExpiresAt when the result is dropped.
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// HasPartial says PartialResult holds what the task has done so far (it is there while the task runs and when it failed or was cancelled).
	HasPartial    bool            `json:"has_partial"`
	PartialResult json.RawMessage `json:"partial_result,omitempty"`
	FromCache     bool            `json:"from_cache"`
	ErrorCode     string          `json:"error_code,omitempty"`
	TraceID       string          `json:"trace_id,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
}

func asyncDTO(t *model.AsyncTask) AssistantAsyncDTO {
	out := AssistantAsyncDTO{ID: t.ID, Kind: t.Kind, Title: t.Title, Status: t.Status, Progress: t.Progress, Stage: t.Stage, Error: t.Error, Attempts: t.Attempts,
		CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt, MaxAttempts: t.MaxAttempts, Retryable: t.Retryable, HeartbeatAt: t.HeartbeatAt, ExpiresAt: t.ExpiresAt,
		FromCache: t.FromCache, ErrorCode: t.ErrorCode, TraceID: t.TraceID, HasPartial: t.HasPartial}
	if t.Status == model.AsyncQueued {
		out.NextRunAt = t.NextRunAt
	}
	if t.Status == model.AsyncRunning {
		out.Partial = t.Partial
	}
	if t.HasPartial && json.Valid([]byte(t.PartialResult)) && t.Status != model.AsyncSucceeded && t.Status != model.AsyncExpired {
		out.PartialResult = json.RawMessage(t.PartialResult)
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
	case errors.Is(err, asyncrun.ErrExpired):
		query.ErrorResponse(c, http.StatusGone, err)
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
	task, err := r.Async.SubmitSpec(ctx, asyncrun.Spec{Owner: owner.ID, OwnerName: owner.Name, Kind: aitask.ChatKind, Title: assistant.TitleOf(question),
		Dedup: "chat-" + hex.EncodeToString(sum[:8]), TraceID: c.GetString("requestId"), MaxAttempts: 2, Input: in})
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

// ListAsync serves GET /assistant/async?kinds=a,b&hours=24: the tasks of the signed-in user that have not ended and the ones created in the last hours,
// the newest first. It is how a page that was closed finds the tasks it queued, and the only way the user sees what is running in the background.
func (r *AssistantRouter) ListAsync(c *gin.Context) {
	owner, ok := asyncOwner(c)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours < 1 || hours > 720 {
		hours = 24
	}
	var kinds []string
	for _, k := range strings.Split(c.Query("kinds"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}
	rows, err := r.Async.Tasks.ListOwned(c.Request.Context(), owner.ID, kinds, time.Now().Add(-time.Duration(hours)*time.Hour), 50)
	if err != nil {
		r.asyncFailure(c, err)
		return
	}
	out := make([]AssistantAsyncDTO, 0, len(rows))
	for _, row := range rows {
		dto := asyncDTO(row)
		dto.Result = nil // the list says what exists; the result is read for one task
		out = append(out, dto)
	}
	query.SuccessResponse(c, nil, out)
}
