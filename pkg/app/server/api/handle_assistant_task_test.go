package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mailbox is the e-mail channel of the tests: it keeps what is sent, and can be told to fail for an address.
type mailbox struct {
	mu   sync.Mutex
	sent []sentMail
	fail map[string]bool
}

type sentMail struct {
	to  string
	msg notify.Message
}

func (m *mailbox) Channel() string                              { return email.Channel }
func (m *mailbox) Recipients(context.Context) ([]string, error) { return nil, nil }
func (m *mailbox) Send(_ context.Context, to string, msg notify.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail[to] {
		return errors.New("the SMTP server is down")
	}
	m.sent = append(m.sent, sentMail{to, msg})
	return nil
}

func (m *mailbox) to() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sent {
		out = append(out, s.to)
	}
	return out
}

type fakeQueue struct {
	queued []time.Time
	open   bool
}

func (q *fakeQueue) Enqueue(_ context.Context, _ uint, _ string, at time.Time) error {
	q.queued = append(q.queued, at)
	return nil
}
func (q *fakeQueue) HasOpen(context.Context, uint) (bool, error) { return q.open, nil }

func taskRig(t *testing.T) (*assistantRig, *mailbox) {
	t.Helper()
	a := newAssistantRig(t, true)
	box := &mailbox{fail: map[string]bool{}}
	notify.Register(box)
	require.NoError(t, model.DB.Create(&model.User{ID: 1, Username: "ann", PasswordHash: "x", Email: "ann@example.com"}).Error)
	require.NoError(t, model.DB.Create(&model.User{ID: 2, Username: "bob", PasswordHash: "x", Email: "bob@example.com"}).Error)
	return a, box
}

// as sends a request as the signed-in user id (0: a call with no user, like a developer credential).
func (a *assistantRig) as(t *testing.T, id int, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if id > 0 {
		req.Header.Set("X-Test-User", strconv.Itoa(id))
	}
	rec := httptest.NewRecorder()
	a.r.ServeHTTP(rec, req)
	return rec
}

func taskBody(name, schedule string) map[string]any {
	return map[string]any{"name": name, "prompt": "Report the usage status of all modules over the last 7 days.", "schedule": schedule, "lang": "en", "enabled": true,
		"llm_id": 0, "recipients": []string{"ann@example.com"}}
}

func create(t *testing.T, a *assistantRig, user int, body map[string]any) map[string]any {
	t.Helper()
	rec := a.as(t, user, http.MethodPost, "/assistant/tasks", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeData(t, rec.Body.Bytes())
}

func TestATaskIsMadeOnThePageWithAScheduleThatIsNotMoreOftenThanOnceAnHour(t *testing.T) {
	a, _ := taskRig(t)
	got := create(t, a, 1, taskBody("Weekly", "0 9 * * 1"))
	assert.Equal(t, "user1", got["owner_name"])
	assert.Equal(t, "page", got["source"])
	assert.NotNil(t, got["next_run_at"], "the next time is worked out")
	assert.Equal(t, []any{"ann@example.com"}, got["recipients"])

	for name, mutate := range map[string]func(map[string]any){
		"every minute":          func(b map[string]any) { b["schedule"] = "* * * * *" },
		"every 10 minutes":      func(b map[string]any) { b["schedule"] = "@every 10m" },
		"not a schedule":        func(b map[string]any) { b["schedule"] = "whenever" },
		"no prompt":             func(b map[string]any) { b["prompt"] = " " },
		"a recipient that isnt": func(b map[string]any) { b["recipients"] = []string{"ann@example.com\r\nBcc: x@example.com"} },
		"a model that is gone":  func(b map[string]any) { b["llm_id"] = 999 },
		"a language":            func(b map[string]any) { b["lang"] = "fr" },
		"a long prompt":         func(b map[string]any) { b["prompt"] = strings.Repeat("x", 2001) },
	} {
		body := taskBody("Bad", "0 9 * * 1")
		mutate(body)
		assert.Equal(t, http.StatusBadRequest, a.as(t, 1, http.MethodPost, "/assistant/tasks", body).Code, name)
	}
	assert.Equal(t, http.StatusUnauthorized, a.as(t, 0, http.MethodPost, "/assistant/tasks", taskBody("x", "0 9 * * 1")).Code, "a task has an owner")

	// update keeps the owner; delete removes the task and its runs
	id := strconv.Itoa(int(got["id"].(float64)))
	body := taskBody("Weekly (renamed)", "0 8 * * *")
	rec := a.as(t, 2, http.MethodPut, "/assistant/tasks/"+id, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	updated := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "user1", updated["owner_name"], "an administrator who edits it does not become its owner")
	assert.Equal(t, "0 8 * * *", updated["schedule"])
	require.NoError(t, model.DB.Create(&model.AssistantTaskRun{TaskID: uint(got["id"].(float64)), Status: model.AIRunSuccess}).Error)
	assert.Equal(t, http.StatusOK, a.as(t, 1, http.MethodDelete, "/assistant/tasks/"+id, nil).Code)
	var n int64
	require.NoError(t, model.DB.Model(&model.AssistantTaskRun{}).Count(&n).Error)
	assert.Zero(t, n)
	assert.Equal(t, http.StatusNotFound, a.as(t, 1, http.MethodDelete, "/assistant/tasks/"+id, nil).Code)
}

func TestThePreviewShowsTheNextTimesOrWhyTheScheduleIsRefused(t *testing.T) {
	a, _ := taskRig(t)
	rec := a.as(t, 1, http.MethodGet, "/assistant/tasks/preview?schedule="+strings.ReplaceAll("0 9 * * 1", " ", "%20"), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	d := decodeData(t, rec.Body.Bytes())
	assert.Len(t, d["next"], 5)
	assert.Equal(t, "", d["error"])
	rec = a.as(t, 1, http.MethodGet, "/assistant/tasks/preview?schedule=*%20*%20*%20*%20*", nil)
	d = decodeData(t, rec.Body.Bytes())
	assert.Contains(t, d["error"], "once an hour")
	assert.Empty(t, d["next"])
}

func TestAUserHasAtMostTwentyTasks(t *testing.T) {
	a, _ := taskRig(t)
	for i := 0; i < assistant.MaxTasksPerOwner; i++ {
		create(t, a, 1, taskBody("t"+strconv.Itoa(i), "0 9 * * 1"))
	}
	assert.Equal(t, http.StatusBadRequest, a.as(t, 1, http.MethodPost, "/assistant/tasks", taskBody("one more", "0 9 * * 1")).Code)
	create(t, a, 2, taskBody("bob's", "0 9 * * 1"))
}

func TestTheChatMakesATaskForTheUserAndMailsOnlyTheirOwnAddress(t *testing.T) {
	a, _ := taskRig(t)
	args := `{"name":"Weekly usage","schedule":"0 9 * * 1","prompt":"Report the usage status of all modules.","language":"en","recipients":["evil@example.com"],"to":"evil@example.com"}`
	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){
		ask(assistant.ToolCall{ID: "1", Name: "create_scheduled_task", Arguments: args}),
		say("Done: every Monday at 09:00."),
	}
	rec := a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("send me the usage of all modules every week")}, "lang": "en"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	names := toolNames(a.llm.tools[0])
	assert.Contains(t, names, "create_scheduled_task")

	rows, total, err := a.tasks.List(context.Background(), rdbFilter(1), 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, model.AITaskFromChat, rows[0].Source)
	assert.Equal(t, []string{"ann@example.com"}, assistant.ParseRecipients(rows[0].Recipients), "whatever the model wrote, the report goes to the user's own address")
	assert.True(t, rows[0].Enabled)
	assert.NotNil(t, rows[0].NextRunAt)

	// the tool result does not show the whole address
	last := a.llm.calls[len(a.llm.calls)-1]
	assert.NotContains(t, last[len(last)-1].Content, "ann@example.com")
	assert.NotContains(t, last[len(last)-1].Content, "evil@example.com")
}

func TestWithoutASignedInUserOrAnAddressNoTaskIsMade(t *testing.T) {
	a, _ := taskRig(t)
	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){say("ok")}
	rec := a.chat(t, user("hello"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, toolNames(a.llm.tools[0]), "create_scheduled_task", "a call with no user has no task tools")

	// a user without an address is told so
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = 2").Update("email", "").Error)
	args := `{"name":"w","schedule":"0 9 * * 1","prompt":"p"}`
	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){
		ask(assistant.ToolCall{ID: "1", Name: "create_scheduled_task", Arguments: args}), say("no address"),
	}
	rec = a.as(t, 2, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("mail me weekly")}, "lang": "en"})
	require.Equal(t, http.StatusOK, rec.Code)
	var n int64
	require.NoError(t, model.DB.Model(&model.AssistantTask{}).Count(&n).Error)
	assert.Zero(t, n)
	last := a.llm.calls[len(a.llm.calls)-1]
	assert.Contains(t, last[len(last)-1].Content, "no e-mail address")
}

func TestTheChatListsAndDeletesOnlyTheTasksOfTheUser(t *testing.T) {
	a, _ := taskRig(t)
	mine := create(t, a, 1, taskBody("mine", "0 9 * * 1"))
	theirs := create(t, a, 2, taskBody("theirs", "0 9 * * 1"))

	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){
		ask(assistant.ToolCall{ID: "1", Name: "list_scheduled_tasks", Arguments: `{}`}), say("listed"),
	}
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("my tasks")}, "lang": "en"}).Code)
	last := a.llm.calls[len(a.llm.calls)-1]
	assert.Contains(t, last[len(last)-1].Content, `"mine"`)
	assert.NotContains(t, last[len(last)-1].Content, "theirs")

	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){
		ask(assistant.ToolCall{ID: "1", Name: "delete_scheduled_task", Arguments: `{"id":` + strconv.Itoa(int(theirs["id"].(float64))) + `}`}), say("could not"),
	}
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("delete it")}, "lang": "en"}).Code)
	_, err := a.tasks.Get(context.Background(), uint(theirs["id"].(float64)))
	require.NoError(t, err, "the task of another user is still there")

	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){
		ask(assistant.ToolCall{ID: "1", Name: "delete_scheduled_task", Arguments: `{"id":` + strconv.Itoa(int(mine["id"].(float64))) + `}`}), say("deleted"),
	}
	require.Equal(t, http.StatusOK, a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user("delete mine")}, "lang": "en"}).Code)
	_, err = a.tasks.Get(context.Background(), uint(mine["id"].(float64)))
	assert.Error(t, err)

	// the skill shows the table of the user's own
	create(t, a, 1, taskBody("again", "0 9 * * 1"))
	rec := a.as(t, 1, http.MethodPost, "/assistant/skill", map[string]any{"name": "tasks", "lang": "en"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "again")
	assert.NotContains(t, rec.Body.String(), "theirs")
}

func TestOnlyTheTasksThatAreDueAreQueuedOnceAndAMissedTimeIsNotCaughtUp(t *testing.T) {
	a, _ := taskRig(t)
	ctx := context.Background()
	due := create(t, a, 1, taskBody("due", "0 9 * * 1"))
	later := create(t, a, 1, taskBody("later", "0 9 * * 1"))
	off := create(t, a, 1, taskBody("off", "0 9 * * 1"))
	busy := create(t, a, 1, taskBody("busy", "0 9 * * 1"))
	id := func(m map[string]any) uint { return uint(m["id"].(float64)) }
	past := runtimeNow.Add(-3 * 24 * time.Hour) // the schedule fired three days ago: the platform was away
	future := runtimeNow.Add(time.Hour)
	require.NoError(t, a.tasks.SetNextRun(ctx, id(due), past))
	require.NoError(t, a.tasks.SetNextRun(ctx, id(later), future))
	require.NoError(t, a.tasks.SetNextRun(ctx, id(off), past))
	require.NoError(t, model.DB.Model(&model.AssistantTask{}).Where("id = ?", id(off)).Update("enabled", false).Error)
	require.NoError(t, a.tasks.SetNextRun(ctx, id(busy), past))

	q := &fakeQueue{}
	n, err := a.svc.ScheduleDue(ctx, q)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "due and busy are queued (busy is told apart below)")
	assert.Len(t, q.queued, 2)
	again, err := a.svc.ScheduleDue(ctx, q)
	require.NoError(t, err)
	assert.Zero(t, again, "their next time is in the future now: a second tick queues nothing")
	row, _ := a.tasks.Get(ctx, id(due))
	assert.True(t, row.NextRunAt.After(runtimeNow), "the next time is after now, not after the missed one")

	// a task whose last run is still going waits for the next time instead of piling up
	require.NoError(t, a.tasks.SetNextRun(ctx, id(busy), past))
	q2 := &fakeQueue{open: true}
	n, err = a.svc.ScheduleDue(ctx, q2)
	require.NoError(t, err)
	assert.Zero(t, n)
	row, _ = a.tasks.Get(ctx, id(busy))
	assert.True(t, row.NextRunAt.After(runtimeNow))

	// a schedule that cannot be read is recorded as the failure of the task, and does not stop the others
	require.NoError(t, model.DB.Model(&model.AssistantTask{}).Where("id = ?", id(later)).Updates(map[string]any{"schedule": "garbage", "next_run_at": past}).Error)
	_, err = a.svc.ScheduleDue(ctx, &fakeQueue{})
	require.NoError(t, err)
	row, _ = a.tasks.Get(ctx, id(later))
	assert.Equal(t, model.AIRunFailed, row.LastStatus)
	assert.True(t, row.NextRunAt.After(runtimeNow))
}

func TestARunAsksTheModelKeepsTheAnswerAndMailsTheRecipients(t *testing.T) {
	a, box := taskRig(t)
	ctx := context.Background()
	body := taskBody("Weekly", "0 9 * * 1")
	body["recipients"] = []string{"ann@example.com", "ops@example.com"}
	task := create(t, a, 1, body)
	id := uint(task["id"].(float64))

	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){say("# Usage\nAll modules are healthy.")}
	require.NoError(t, a.svc.RunTask(ctx, id, 7, "schedule"))
	assert.ElementsMatch(t, []string{"ann@example.com", "ops@example.com"}, box.to())
	assert.Contains(t, box.sent[0].msg.Content, "All modules are healthy.")
	assert.Contains(t, box.sent[0].msg.Title, "Weekly")
	// the model is asked the prompt of the task, with the read-only tools and no tool to make tasks
	first := a.llm.calls[0]
	assert.Contains(t, first[len(first)-1].Content, "Report the usage status of all modules")
	assert.NotContains(t, toolNames(a.llm.tools[0]), "create_scheduled_task", "a scheduled run cannot make more tasks")
	assert.Contains(t, toolNames(a.llm.tools[0]), "get_service_status")

	row, _ := a.tasks.Get(ctx, id)
	assert.Equal(t, model.AIRunSuccess, row.LastStatus)
	runs, total, err := a.tasks.Runs(ctx, id, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, "# Usage\nAll modules are healthy.", runs[0].Output)
	assert.Equal(t, model.AIRunSuccess, runs[0].Status)
	assert.NotNil(t, runs[0].FinishedAt)

	// the page shows the run with its output and who got it
	rec := a.as(t, 1, http.MethodGet, "/assistant/tasks/"+strconv.Itoa(int(id))+"/runs", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "All modules are healthy.")
	assert.Contains(t, rec.Body.String(), "ops@example.com")
}

func TestARetryOfTheJobContinuesTheRunWithoutAskingTheModelAgainOrMailingTwice(t *testing.T) {
	a, box := taskRig(t)
	ctx := context.Background()
	body := taskBody("Weekly", "0 9 * * 1")
	body["recipients"] = []string{"ann@example.com", "ops@example.com"}
	id := uint(create(t, a, 1, body)["id"].(float64))
	box.fail["ops@example.com"] = true

	a.llm.steps = []func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error){say("report")}
	err := a.svc.RunTask(ctx, id, 9, "schedule")
	require.Error(t, err, "the mail did not go to everyone: the job engine tries again")
	assert.Equal(t, []string{"ann@example.com"}, box.to())
	row, _ := a.tasks.Get(ctx, id)
	assert.Equal(t, model.AIRunFailed, row.LastStatus)
	assert.Contains(t, row.LastError, "could not be mailed")

	delete(box.fail, "ops@example.com")
	require.NoError(t, a.svc.RunTask(ctx, id, 9, "schedule"))
	assert.Len(t, a.llm.calls, 1, "the model was asked once")
	assert.Equal(t, []string{"ann@example.com", "ops@example.com"}, box.to(), "ann was not mailed again")
	_, total, _ := a.tasks.Runs(ctx, id, 1, 10)
	assert.EqualValues(t, 1, total, "one run, not two")
	row, _ = a.tasks.Get(ctx, id)
	assert.Equal(t, model.AIRunSuccess, row.LastStatus)
}

func TestAModelThatFailsIsRetriedAndAnAssistantThatIsNotSetUpIsNot(t *testing.T) {
	a, box := taskRig(t)
	ctx := context.Background()
	id := uint(create(t, a, 1, taskBody("Weekly", "0 9 * * 1"))["id"].(float64))

	a.llm.steps = nil // the script has no answer: the model "does not answer"
	require.Error(t, a.svc.RunTask(ctx, id, 11, "schedule"))
	row, _ := a.tasks.Get(ctx, id)
	assert.Equal(t, model.AIRunFailed, row.LastStatus)
	assert.Empty(t, box.to())

	// switched off: nothing to retry, the failure says why
	require.NoError(t, assistant.UpdateEnabled(ctx, a.config, "false"))
	require.NoError(t, a.svc.RunTask(ctx, id, 12, "schedule"), "an error that no retry mends is recorded, not returned")
	row, _ = a.tasks.Get(ctx, id)
	assert.Equal(t, model.AIRunFailed, row.LastStatus)
	assert.NotEmpty(t, row.LastError)

	// a task deleted after its run was queued is not an error
	require.NoError(t, a.svc.RunTask(ctx, 99999, 13, "schedule"))
}

func TestAPageCreatedTaskIsListedWithItsStateForTheAdministrator(t *testing.T) {
	a, _ := taskRig(t)
	create(t, a, 1, taskBody("alpha", "0 9 * * 1"))
	create(t, a, 2, taskBody("beta", "0 9 * * 1"))
	rec := a.as(t, 1, http.MethodGet, "/assistant/tasks?keyword=alp", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	d := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 1, d["total"])
	rec = a.as(t, 1, http.MethodGet, "/assistant/tasks", nil)
	assert.EqualValues(t, 2, decodeData(t, rec.Body.Bytes())["total"], "the administrator sees every user's tasks")
}

func toolNames(specs []assistant.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func rdbFilter(owner uint) rdb.TaskFilter { return rdb.TaskFilter{OwnerID: owner} }
