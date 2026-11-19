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
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/steprun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (a *assistantRig) runner() *asyncrun.Runner {
	return &asyncrun.Runner{Tasks: rdb.NewAsyncTaskDAO(model.DB), Handlers: map[string]asyncrun.Handler{aitask.ChatKind: aitask.ChatHandler(a.svc), steprun.Kind: steprun.Handler(a.svc.Forecast), steprun.PreviewKind: steprun.PreviewHandler(a.svc.Forecast)},
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
	require.Len(t, events, 2, "a step is announced when it starts and reported when it ends")
	assert.Equal(t, "tool_start", events[0].(map[string]any)["type"])
	assert.Equal(t, "tool", events[1].(map[string]any)["type"])
	assert.Equal(t, "list_modules", events[1].(map[string]any)["data"].(map[string]any)["name"])
	assert.Contains(t, events[1].(map[string]any)["data"].(map[string]any)["result"], "shop", "the step carries a preview of what it read")
	none := decodeDataList(t, a.as(t, 1, http.MethodGet, "/assistant/async/"+idText(id)+"/events?after=2", nil).Body.Bytes())
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
	assert.EqualValues(t, 1, again["attempts"], "a retry of the owner starts the attempts again: this is the first run of the new attempt")
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

func TestFinishedTasksOlderThanTheRetentionExpireAndTheirRecordsAreDeletedAMonthLaterWithTheirEvents(t *testing.T) {
	a := newAssistantRig(t, true)
	assert.Equal(t, 72, timing.AsyncRetentionHours.Default)
	now := a.svc.Now().UTC()
	mk := func(age time.Duration, status string) uint {
		finished := now.Add(-age)
		task := model.AsyncTask{OwnerID: 1, Kind: "assistant.chat", Status: status, CreatedAt: finished, FinishedAt: &finished, Result: "{\"answer\":1}", Payload: "{}"}
		if status == model.AsyncRunning {
			task.FinishedAt = nil
		}
		require.NoError(t, model.DB.Create(&task).Error)
		require.NoError(t, model.DB.Create(&model.AsyncTaskEvent{TaskID: task.ID, Seq: 1, Type: "tool", Data: "{}"}).Error)
		return task.ID
	}
	old, fresh, running := mk(80*time.Hour, "succeeded"), mk(10*time.Hour, "succeeded"), mk(500*time.Hour, "running")
	gone := mk(80*time.Hour+time.Duration(assistant.AsyncRecordDays)*24*time.Hour, "failed")
	n, err := a.svc.PruneAsync(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "only the record that expired a month ago is deleted")
	var tasks []model.AsyncTask
	require.NoError(t, model.DB.Order("id").Find(&tasks).Error)
	status := map[uint]string{}
	for _, task := range tasks {
		status[task.ID] = task.Status
	}
	assert.Equal(t, map[uint]string{old: model.AsyncExpired, fresh: model.AsyncSucceeded, running: model.AsyncRunning}, status)
	assert.NotContains(t, status, gone)
	for _, task := range tasks {
		if task.ID == old {
			assert.Empty(t, task.Result, "the result of an expired task is gone")
			assert.Empty(t, task.Payload)
		}
		if task.ID == fresh {
			assert.NotEmpty(t, task.Result)
		}
	}
	var events int64
	require.NoError(t, model.DB.Model(&model.AsyncTaskEvent{}).Count(&events).Error)
	assert.EqualValues(t, 2, events, "the events of the expired and the deleted tasks went")
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

func TestALongStepOfAModuleRunsInTheBackgroundAndItsResultIsTheOneTheSynchronousStepGives(t *testing.T) {
	a := newAssistantRig(t, true)
	module := strconv.Itoa(int(a.module.ID))
	rec := a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/steps/evaluate", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	queued := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "queued", queued["status"])
	assert.Equal(t, "forecast.step", queued["kind"])
	assert.Contains(t, queued["title"], "evaluate")

	// the same step of the same module while it is open is the same task
	again := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/steps/evaluate", map[string]any{}).Body.Bytes())
	assert.Equal(t, queued["id"], again["id"])

	a.runJobs(t)
	done := a.task(t, 1, queued["id"])
	assert.Equal(t, "succeeded", done["status"], done["error"])
	assert.Contains(t, done, "result", "the result is what the synchronous endpoint answers")

	// only its owner reads it; the task calls are the same as for a question
	assert.Equal(t, http.StatusNotFound, a.as(t, 2, http.MethodGet, "/assistant/async/"+idText(queued["id"]), nil).Code)
}

func TestAStepThatCannotBeRunInTheBackgroundIsRefusedBeforeItIsQueued(t *testing.T) {
	a := newAssistantRig(t, true)
	module := strconv.Itoa(int(a.module.ID))
	for name, c := range map[string]struct {
		uid  int
		path string
		body any
		want int
	}{
		"a step that does not exist":       {1, "/modules/" + module + "/steps/delete", map[string]any{}, http.StatusBadRequest},
		"a backtest with no metric":        {1, "/modules/" + module + "/steps/backtest", nil, http.StatusBadRequest},
		"a module that does not exist":     {1, "/modules/99999/steps/evaluate", map[string]any{}, http.StatusNotFound},
		"a call with no user":              {0, "/modules/" + module + "/steps/evaluate", map[string]any{}, http.StatusBadRequest},
		"a module id that is not a number": {1, "/modules/abc/steps/evaluate", map[string]any{}, http.StatusBadRequest},
	} {
		rec := a.asRoles(t, c.uid, "", http.MethodPost, c.path, c.body)
		assert.Equal(t, c.want, rec.Code, name+": "+rec.Body.String())
	}
	var n int64
	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Count(&n).Error)
	assert.Zero(t, n)

	rec := a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/steps/backtest", map[string]any{"metric": "cpu", "cutoffs": 3})
	assert.Equal(t, http.StatusOK, rec.Code, "a backtest with its metric is queued")
}

// writing is a language model that writes its answer in pieces and waits, after the first, until the test has looked at the task.
type writing struct {
	started atomic.Bool
	release chan struct{}
}

func (w *writing) Complete(context.Context, []assistant.Message, []assistant.ToolSpec, int) (*assistant.Completion, error) {
	return &assistant.Completion{Content: "Hello world."}, nil
}

func (w *writing) Stream(_ context.Context, _ []assistant.Message, _ []assistant.ToolSpec, _ int, onText func(string)) (*assistant.Completion, error) {
	onText("Hello wor")
	w.started.Store(true)
	<-w.release
	onText("Hello world.")
	return &assistant.Completion{Content: "Hello world."}, nil
}

func TestTheAnswerOfABackgroundQuestionCanBeReadWhileItIsBeingWritten(t *testing.T) {
	a := newAssistantRig(t, true)
	w := &writing{release: make(chan struct{})}
	a.svc.NewLLM = func(assistant.Connection) assistant.LLM { return w }
	a.setMode(t, "native")
	id := decodeData(t, a.submit(t, 1, "", "say hello", 0).Body.Bytes())["id"]

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		a.runJobs(t)
	}()
	require.Eventually(t, w.started.Load, 5*time.Second, 10*time.Millisecond)
	var partial any
	require.Eventually(t, func() bool {
		partial = a.task(t, 1, id)["partial"]
		return partial == "Hello wor"
	}, 5*time.Second, 20*time.Millisecond, "the text written so far is in the task, for the page to show")
	assert.Equal(t, "running", a.task(t, 1, id)["status"])

	close(w.release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the task did not finish")
	}
	done := a.task(t, 1, id)
	assert.Equal(t, "succeeded", done["status"])
	assert.Equal(t, "Hello world.", done["result"].(map[string]any)["content"])
	assert.Nil(t, done["partial"], "once it is done the answer is the result, not the partial text")
}

func TestTheChangePreviewRunsInTheBackgroundAndAnEmptyChangeFailsOnceAndIsNotTriedAgain(t *testing.T) {
	a := newAssistantRig(t, true)
	module := strconv.Itoa(int(a.module.ID))
	rec := a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/change-preview/async", map[string]any{"since_hours": 6, "headroom_ratio": 1.2})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	queued := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "queued", queued["status"])
	assert.Equal(t, "forecast.preview", queued["kind"])
	assert.EqualValues(t, steprun.PreviewAttempts, queued["max_attempts"])
	assert.NotEmpty(t, queued["trace_id"], "the request id of the request that queued it")

	again := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/change-preview/async", map[string]any{"since_hours": 6, "headroom_ratio": 1.2}).Body.Bytes())
	assert.Equal(t, queued["id"], again["id"], "the same preview while it is open is the same task")
	other := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/change-preview/async", map[string]any{"since_hours": 6, "headroom_ratio": 1.5}).Body.Bytes())
	assert.NotEqual(t, queued["id"], other["id"], "another change is another task")

	empty := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/change-preview/async", map[string]any{"since_hours": 6}).Body.Bytes())
	a.runJobs(t)
	failed := a.task(t, 1, empty["id"])
	assert.Equal(t, "failed", failed["status"])
	assert.Contains(t, failed["error"], "nothing to compare")
	assert.EqualValues(t, 1, failed["attempts"], "a request that is not valid is not tried again")
	assert.Equal(t, false, failed["retryable"])

	assert.Equal(t, http.StatusNotFound, a.asRoles(t, 1, "", http.MethodPost, "/modules/99999/change-preview/async", map[string]any{"headroom_ratio": 1.2}).Code)
	assert.Equal(t, http.StatusBadRequest, a.asRoles(t, 0, "", http.MethodPost, "/modules/"+module+"/change-preview/async", map[string]any{"headroom_ratio": 1.2}).Code)
}

func TestTheTasksOfTheSignedInUserAreListedSoAClosedPageFindsThemAgain(t *testing.T) {
	a := newAssistantRig(t, true)
	module := strconv.Itoa(int(a.module.ID))
	mine := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/steps/evaluate", map[string]any{}).Body.Bytes())
	theirs := decodeData(t, a.asRoles(t, 2, "", http.MethodPost, "/modules/"+module+"/steps/collect", map[string]any{}).Body.Bytes())
	require.NotEqual(t, mine["id"], theirs["id"])

	list := func(uid int, query string) []map[string]any {
		rec := a.as(t, uid, http.MethodGet, "/assistant/async"+query, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Data
	}
	got := list(1, "")
	require.Len(t, got, 1, "only the tasks of the signed-in user")
	assert.Equal(t, mine["id"], got[0]["id"])
	assert.NotContains(t, got[0], "result", "the list says what exists, the result is read for one task")
	assert.Len(t, list(1, "?kinds=forecast.step"), 1)
	assert.Empty(t, list(1, "?kinds=assistant.chat"))
	assert.Equal(t, http.StatusBadRequest, a.as(t, 0, http.MethodGet, "/assistant/async", nil).Code)
}

func TestTheViewOfATaskSaysWhatItHasDoneSoFarWhileItRunsAndAfterItFailedButNotOnceItSucceeded(t *testing.T) {
	a := newAssistantRig(t, true)
	module := strconv.Itoa(int(a.module.ID))
	queued := decodeData(t, a.asRoles(t, 1, "", http.MethodPost, "/modules/"+module+"/steps/evaluate", map[string]any{}).Body.Bytes())
	id := uint(queued["id"].(float64))
	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(map[string]any{"status": "failed", "has_partial": true, "partial_result": `{"done":2}`,
		"error_code": "timeout", "retryable": true, "next_run_at": time.Now()}).Error)
	failed := a.task(t, 1, queued["id"])
	assert.Equal(t, true, failed["has_partial"])
	assert.JSONEq(t, `{"done":2}`, string(mustJSON(t, failed["partial_result"])))
	assert.Equal(t, "timeout", failed["error_code"])
	assert.Equal(t, true, failed["retryable"])
	assert.NotContains(t, failed, "next_run_at", "only a task that is waiting for its next attempt shows when")

	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(map[string]any{"status": "queued"}).Error)
	waiting := a.task(t, 1, queued["id"])
	assert.Contains(t, waiting, "next_run_at")

	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(map[string]any{"status": "succeeded", "result": `{"ok":true}`}).Error)
	ok := a.task(t, 1, queued["id"])
	assert.NotContains(t, ok, "partial_result", "a result that is complete needs no part of itself")

	require.NoError(t, model.DB.Model(&model.AsyncTask{}).Where("id = ?", id).UpdateColumns(map[string]any{"status": "expired", "result": ""}).Error)
	gone := a.task(t, 1, queued["id"])
	assert.Equal(t, "expired", gone["status"])
	assert.NotContains(t, gone, "result")
	assert.Equal(t, http.StatusGone, a.as(t, 1, http.MethodPost, "/assistant/async/"+idText(queued["id"])+"/retry", nil).Code, "there is nothing left to run again")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
