package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/message"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tele "gopkg.in/telebot.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBotAPI stands in for api.telegram.org: it answers the Bot API calls the
// adapter makes and lets a test feed updates to the bot.
type fakeBotAPI struct {
	srv *httptest.Server

	mu       sync.Mutex
	rejectMe bool
	updates  []map[string]any
	nextID   int
	requests map[string][]map[string]any
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{requests: map[string][]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.requests[method] = append(f.requests[method], body)
	reject := f.rejectMe
	var batch []map[string]any
	if method == "getUpdates" {
		batch, f.updates = f.updates, nil
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":4242,"is_bot":true,"first_name":"Fake","username":"fake_bot"}}`))
	case "getUpdates":
		if len(batch) == 0 {
			time.Sleep(30 * time.Millisecond)
		}
		out, _ := json.Marshal(map[string]any{"ok": true, "result": batch})
		_, _ = w.Write(out)
	case "sendMessage":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"date":1,"chat":{"id":1,"type":"private"},"text":"ok"}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (f *fakeBotAPI) push(update map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	update["update_id"] = f.nextID
	f.updates = append(f.updates, update)
}

func (f *fakeBotAPI) calls(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests[method]...)
}

func textUpdate(text string) map[string]any {
	msg := map[string]any{
		"message_id": 11, "date": 1,
		"chat": map[string]any{"id": 99, "type": "private"},
		"from": map[string]any{"id": 99, "is_bot": false, "first_name": "Ann", "username": "ann"},
		"text": text,
	}
	if strings.HasPrefix(text, "/") {
		end := strings.IndexByte(text, ' ')
		if end < 0 {
			end = len(text)
		}
		msg["entities"] = []map[string]any{{"type": "bot_command", "offset": 0, "length": end}}
	}
	return map[string]any{"message": msg}
}

type fakeRunner struct {
	mu      sync.Mutex
	texts   []string
	bots    []int64
	actions []string
	reply   workflow.CommandReply
	outcome workflow.ActionOutcome
}

func (r *fakeRunner) HandleIncomingText(ctx context.Context, _ workflow.ChatUser, text string) (workflow.CommandReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	id, _ := model.BotFrom(ctx)
	r.bots = append(r.bots, id)
	return r.reply, nil
}

func (r *fakeRunner) HandleIncomingAction(_ context.Context, _ workflow.ChatUser, action string, id int64) (workflow.ActionOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, fmt.Sprintf("%s:%d", action, id))
	if r.outcome.Reply == "" && !r.outcome.PromptForNote {
		return workflow.ActionOutcome{Reply: "done"}, nil
	}
	return r.outcome, nil
}

func (r *fakeRunner) snapshot() (texts []string, bots []int64, actions []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(texts, r.texts...), append(bots, r.bots...), append(actions, r.actions...)
}

func newService(t *testing.T, api *fakeBotAPI, deps BotDeps) *BotService {
	t.Helper()
	service, err := NewBotService(config.TelegramConfig{Enabled: true, BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", APIBaseURL: api.srv.URL}, deps)
	require.NoError(t, err)
	require.NotNil(t, service)
	return service
}

func TestNewBotServiceRefusesADisabledBotOrABadToken(t *testing.T) {
	service, err := NewBotService(config.TelegramConfig{Enabled: false, BotToken: "x"}, BotDeps{})
	assert.NoError(t, err)
	assert.Nil(t, service)
	service, err = NewBotService(config.TelegramConfig{Enabled: true}, BotDeps{})
	assert.NoError(t, err)
	assert.Nil(t, service)

	api := newFakeBotAPI(t)
	api.rejectMe = true
	_, err = NewBotService(config.TelegramConfig{Enabled: true, BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", APIBaseURL: api.srv.URL}, BotDeps{})
	assert.Error(t, err, "Telegram rejecting the token surfaces as an error")
}

func TestBotReportsItsIdentityAndPushesItsCommandMenu(t *testing.T) {
	api := newFakeBotAPI(t)
	service := newService(t, api, BotDeps{BotID: 3})
	username, id := service.Identity()
	assert.Equal(t, "fake_bot", username)
	assert.EqualValues(t, 4242, id)

	require.NoError(t, service.SyncCommands([]CommandSpec{
		{Command: "/Status", Description: "Show status"},
		{Command: "help"},
		{Command: "  "},
		{Command: "/with space", Description: "invalid"},
	}))
	calls := api.calls("setMyCommands")
	require.Len(t, calls, 1)
	commands, _ := calls[0]["commands"].([]any)
	got := []string{}
	for _, c := range commands {
		entry := c.(map[string]any)
		got = append(got, fmt.Sprintf("%s=%s", entry["command"], entry["description"]))
	}
	assert.Equal(t, []string{"status=Show status", "help=help"}, got, "names are lower-cased and slash-free, blanks dropped, empty descriptions filled")
}

func TestBotAnswersCommandsScopedToItself(t *testing.T) {
	api := newFakeBotAPI(t)
	runner := &fakeRunner{reply: workflow.CommandReply{Text: "all good"}}
	events := make(chan message.Event, 4)
	service := newService(t, api, BotDeps{BotID: 7, WorkflowEngine: runner, Sink: message.SinkFunc(func(e message.Event) { events <- e })})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	api.push(textUpdate("/status now"))
	require.Eventually(t, func() bool { return len(api.calls("sendMessage")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "all good", api.calls("sendMessage")[0]["text"])

	texts, bots, _ := runner.snapshot()
	assert.Equal(t, []string{"/status now"}, texts)
	assert.Equal(t, []int64{7}, bots, "command matching is limited to this bot")
	assert.NotEmpty(t, api.calls("deleteWebhook"), "polling takes over from any webhook")

	event := <-events
	assert.Equal(t, message.EventCommand, event.Type)
	assert.Equal(t, "telegram", event.Message.Platform)
	assert.Equal(t, message.SourceBot, event.Message.SourceType)
	assert.Equal(t, "/status now", event.Message.Text)
	assert.Equal(t, "ann", event.Message.Sender)
	assert.Equal(t, "7", event.Message.Account)

	// Plain text still reaches the engine (pending replies), but is not a command event.
	api.push(textUpdate("hello"))
	require.Eventually(t, func() bool { t, _, _ := runner.snapshot(); return len(t) == 2 }, 5*time.Second, 20*time.Millisecond)
	select {
	case e := <-events:
		t.Fatalf("unexpected event %+v", e)
	default:
	}
}

func TestBotRoutesButtonPressesToTheWorkflowEngine(t *testing.T) {
	api := newFakeBotAPI(t)
	runner := &fakeRunner{}
	service := newService(t, api, BotDeps{BotID: 1, WorkflowEngine: runner})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.Start(ctx)

	api.push(map[string]any{"callback_query": map[string]any{
		"id": "cb1", "chat_instance": "x", "data": "approve:55",
		"from":    map[string]any{"id": 99, "is_bot": false, "first_name": "Ann"},
		"message": map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": 99, "type": "private"}},
	}})
	require.Eventually(t, func() bool { _, _, a := runner.snapshot(); return len(a) == 1 }, 5*time.Second, 20*time.Millisecond)
	_, _, actions := runner.snapshot()
	assert.Equal(t, []string{"approve:55"}, actions)
	require.Eventually(t, func() bool { return len(api.calls("answerCallbackQuery")) == 1 }, 5*time.Second, 20*time.Millisecond)
}

func TestIdentityWithoutGetMeIsEmptyAndCommandMessagesFallBackToTheName(t *testing.T) {
	username, id := (&BotService{bot: &tele.Bot{}}).Identity()
	assert.Empty(t, username)
	assert.Zero(t, id)

	named := commandMessage(2, &tele.User{ID: 5, FirstName: "Ann", LastName: "Lee"}, 9, "/x")
	assert.Equal(t, "Ann Lee", named.Sender)
	assert.Equal(t, "5", named.Source)
	assert.Equal(t, "9", named.MessageID)
}
