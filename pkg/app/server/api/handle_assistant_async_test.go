package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/aitask"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (a *assistantRig) runner() *asyncrun.Runner {
	return &asyncrun.Runner{Tasks: rdb.NewAsyncTaskDAO(model.DB), Handlers: map[string]asyncrun.Handler{aitask.ChatKind: aitask.ChatHandler(a.svc)},
		Now: func() time.Time { return runtimeNow }, CancelPoll: 10 * time.Millisecond}
}

// runJobs does what the scheduler does with the queued async jobs: it runs each once.
func (a *assistantRig) runJobs(t *testing.T) {
	t.Helper()
	var jobs []model.Job
	require.NoError(t, model.DB.Where("job_type = ? AND status = ?", asyncrun.JobType, "pending").Order("id").Find(&jobs).Error)
	for i := range jobs {
		require.NoError(t, model.DB.Model(&model.Job{}).Where("id = ?", jobs[i].ID).Update("status", "running").Error)
		require.NoError(t, a.runner().Execute(context.Background(), &jobs[i]))
	}
}

func (a *assistantRig) submit(t *testing.T, uid int, roles, text string, conversation uint) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"messages": []assistant.Turn{user(text)}, "lang": "en"}
	if conversation != 0 {
		body["conversation_id"] = conversation
	}
	return a.asRoles(t, uid, roles, http.MethodPost, "/assistant/chat/async", body)
}

func (a *assistantRig) task(t *testing.T, uid int, id any) map[string]any {
	t.Helper()
	rec := a.as(t, uid, http.MethodGet, "/assistant/async/"+idText(id), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeData(t, rec.Body.Bytes())
}

func idText(id any) string {
	switch v := id.(type) {
	case float64:
		return strconv.Itoa(int(v))
	case uint:
		return strconv.Itoa(int(v))
	}
	return id.(string)
}

func TestAQuestionIsQueuedAtOnceAndAnsweredInTheBackgroundWithItsProgress(t *testing.T) {
	a := newAssistantRig(t, true)
	cid := startConversation(t, a, 1)
	a.llm.steps = []step{ask(assistant.ToolCall{ID: "c1", Name: "list_modules", Arguments: `{}`}), say("One module, shop.")}

	rec := a.submit(t, 1, "OPERATOR", "which modules are there?", cid)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	queued := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "queued", queued["status"])
	assert.Empty(t, a.llm.calls, "the model is not asked in the request: the request returns at once")
	id := queued["id"]

	a.runJobs(t)
	done := a.task(t, 1, id)
	assert.Equal(t, "succeeded", done["status"])
	assert.EqualValues(t, 100, done["progress"])
	result := done["result"].(map[string]any)
	assert.Equal(t, "One module, shop.", result["content"])
	assert.Equal(t, "list_modules", result["tools"].([]any)[0].(map[string]any)["name"])

	events := decodeDataList(t, a.as(t, 1, http.MethodGet, "/assistant/async/"+idText(id)+"/events?after=0", nil).Body.Bytes())
	require.Len(t, events, 1)
	assert.Equal(t, "tool", events[0].(map[string]any)["type"])
	assert.Equal(t, "list_modules", events[0].(map[string]any)["data"].(map[string]any)["name"])
	none := decodeDataList(t, a.as(t, 1, http.MethodGet, "/assistant/async/"+idText(id)+"/events?after=1", nil).Body.Bytes())
	assert.Empty(t, none, "a page that reads on gets only what is new")

	// the answer was kept in the conversation, as for a question asked at once, and audited with the permissions of the one who asked
	msgs := decodeData(t, a.as(t, 1, http.MethodGet, convPath(cid), nil).Body.Bytes())["messages"].([]any)
	require.Len(t, msgs, 2)
	assert.Equal(t, "One module, shop.", msgs[1].(map[string]any)["content"])
	rows := audits(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "user1", rows[0].UserName)
}

func TestAnIdenticalQuestionWhileOneIsOpenIsTheSameTaskAndAUserHasFiveAtMost(t *testing.T) {
	a := newAssistantRig(t, true)
	first := decodeData(t, a.submit(t, 1, "", "same question", 0).Body.Bytes())["id"]
	again := decodeData(t, a.submit(t, 1, "", "same question", 0).Body.Bytes())["id"]
	assert.Equal(t, first, again)
	var n int64
	require.NoError(t, model.DB.Model(&model.Job{}).Where("job_type = ?", asyncrun.JobType).Count(&n).Error)
	assert.EqualValues(t, 1, n, "one job, not two")

	for i := 2; i <= asyncrun.MaxOpenPerUser; i++ {
		require.Equal(t, http.StatusOK, a.submit(t, 1, "", "question "+strconv.Itoa(i), 0).Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, a.submit(t, 1, "", "one too many", 0).Code)
	assert.Equal(t, http.StatusOK, a.submit(t, 2, "", "another user is not held back", 0).Code)
}

func TestATaskIsReadCancelledAndRetriedByItsOwnerOnly(t *testing.T) {
	a := newAssistantRig(t, true)
	id := decodeData(t, a.submit(t, 1, "", "a private question", 0).Body.Bytes())["id"]
	path := "/assistant/async/" + idText(id)
	for _, call := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodGet, "/events"}, {http.MethodPost, "/cancel"}, {http.MethodPost, "/retry"}} {
		assert.Equal(t, http.StatusNotFound, a.as(t, 2, call.method, path+call.suffix, nil).Code, call.method+" "+call.suffix)
	}
	assert.Equal(t, "queued", a.task(t, 1, id)["status"], "the other user changed nothing")
	assert.Equal(t, http.StatusBadRequest, a.as(t, 0, http.MethodGet, path, nil).Code, "a call with no user has no tasks")
}

func TestAQueuedTaskThatIsCancelledIsNeverRun(t *testing.T) {
	a := newAssistantRig(t, true)
	id := decodeData(t, a.submit(t, 1, "", "never asked", 0).Body.Bytes())["id"]
	rec := a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(id)+"/cancel", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	a.llm.steps = []step{say("must not be asked")}
	a.runJobs(t)
	assert.Equal(t, "cancelled", a.task(t, 1, id)["status"])
	assert.Empty(t, a.llm.calls)
	assert.Len(t, a.llm.steps, 1)
}

// blocked is a language model that waits until it is told to stop.
type blocked struct{ started atomic.Bool }

func (b *blocked) Complete(ctx context.Context, _ []assistant.Message, _ []assistant.ToolSpec, _ int) (*assistant.Completion, error) {
	b.started.Store(true)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestARunningTaskStopsWhenItsOwnerCancelsIt(t *testing.T) {
	a := newAssistantRig(t, true)
	wait := &blocked{}
	a.svc.NewLLM = func(assistant.Connection) assistant.LLM { return wait }
	row, err := a.conns.Default(context.Background())
	require.NoError(t, err)
	row.ToolMode = "native"
	require.NoError(t, a.conns.Save(context.Background(), row))

	id := decodeData(t, a.submit(t, 1, "", "a long question", 0).Body.Bytes())["id"]
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		a.runJobs(t)
	}()
	require.Eventually(t, wait.started.Load, 5*time.Second, 10*time.Millisecond, "the worker asked the model")
	assert.Equal(t, "running", a.task(t, 1, id)["status"])
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(id)+"/cancel", map[string]any{}).Code)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the task did not stop")
	}
	assert.Equal(t, "cancelled", a.task(t, 1, id)["status"])
}

func TestAFailedTaskKeepsItsReasonAndIsRetriedByItsOwner(t *testing.T) {
	a := newAssistantRig(t, true)
	a.llm.steps = nil // the model cannot be reached
	id := decodeData(t, a.submit(t, 1, "", "will fail first", 0).Body.Bytes())["id"]
	a.runJobs(t)
	failed := a.task(t, 1, id)
	assert.Equal(t, "failed", failed["status"])
	assert.NotEmpty(t, failed["error"])
	assert.Nil(t, failed["result"])

	// a task that is not finished cannot be retried
	other := decodeData(t, a.submit(t, 1, "", "still queued", 0).Body.Bytes())["id"]
	assert.Equal(t, http.StatusConflict, a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(other)+"/retry", map[string]any{}).Code)
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(other)+"/cancel", map[string]any{}).Code)

	a.llm.steps = []step{say("second time lucky")}
	rec := a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(id)+"/retry", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "queued", decodeData(t, rec.Body.Bytes())["status"])
	a.runJobs(t)
	again := a.task(t, 1, id)
	assert.Equal(t, "succeeded", again["status"])
	assert.EqualValues(t, 2, again["attempts"])
	assert.Equal(t, "second time lucky", again["result"].(map[string]any)["content"])
}

func TestAQuestionThatCannotBeAskedIsRefusedBeforeItIsQueued(t *testing.T) {
	a := newAssistantRig(t, true)
	assert.Equal(t, http.StatusBadRequest, a.submit(t, 1, "", "   ", 0).Code)
	assert.Equal(t, http.StatusNotFound, a.submit(t, 1, "", "someone else's conversation", 99999).Code)
	require.NoError(t, assistant.UpdateEnabled(context.Background(), a.config, "false"))
	assert.Equal(t, http.StatusConflict, a.submit(t, 1, "", "hello", 0).Code)
	var n int64
	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Count(&n).Error)
	assert.Zero(t, n)
	assert.Equal(t, http.StatusBadRequest, a.as(t, 0, http.MethodPost, "/assistant/chat/async", map[string]any{"messages": []assistant.Turn{user("hi")}}).Code)
}

func TestFinishedTasksOlderThanTheRetentionAreDeletedWithTheirEvents(t *testing.T) {
	a := newAssistantRig(t, true)
	assert.Equal(t, 72, timing.AsyncRetentionHours.Default)
	now := a.svc.Now().UTC()
	mk := func(age time.Duration, status string) uint {
		finished := now.Add(-age)
		task := model.AsyncTask{OwnerID: 1, Kind: "assistant.chat", Status: status, CreatedAt: finished, FinishedAt: &finished}
		if status == model.AsyncRunning {
			task.FinishedAt = nil
		}
		require.NoError(t, model.DB.Create(&task).Error)
		require.NoError(t, model.DB.Create(&model.AsyncTaskEvent{TaskID: task.ID, Seq: 1, Type: "tool", Data: "{}"}).Error)
		return task.ID
	}
	old, fresh, running := mk(80*time.Hour, "succeeded"), mk(10*time.Hour, "succeeded"), mk(500*time.Hour, "running")
	n, err := a.svc.PruneAsync(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	var ids []uint
	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Order("id").Pluck("id", &ids).Error)
	assert.Equal(t, []uint{fresh, running}, ids)
	assert.NotContains(t, ids, old)
	var events int64
	require.NoError(t, model.DB.Model(&model.AsyncTaskEvent{}).Count(&events).Error)
	assert.EqualValues(t, 2, events)
}

// asRoles sends a request as the signed-in user id with the roles (the roles the permission system knows the user by).
func (a *assistantRig) asRoles(t *testing.T, id int, roles, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if id > 0 {
		req.Header.Set("X-Test-User", strconv.Itoa(id))
	}
	if roles != "" {
		req.Header.Set("X-Test-Roles", roles)
	}
	rec := httptest.NewRecorder()
	a.r.ServeHTTP(rec, req)
	return rec
}
