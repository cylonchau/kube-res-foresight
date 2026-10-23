package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

type fakeBindings struct {
	err     error
	payload string
}

func (f *fakeBindings) ConfirmBindWithToken(_ context.Context, payload string, _ *TelegramUser, _ time.Time) (*model.TelegramBinding, error) {
	f.payload = payload
	if f.err != nil {
		return nil, f.err
	}
	return &model.TelegramBinding{User: &model.User{NickName: "Ann"}}, nil
}
func (f *fakeBindings) GetBindingByTelegramID(context.Context, int64) (*model.TelegramBinding, error) {
	return nil, errors.New("none")
}
func (f *fakeBindings) ListActiveAdmins(context.Context) ([]model.TelegramBinding, error) {
	return nil, nil
}
func (f *fakeBindings) ListActiveByRole(context.Context, string) ([]model.TelegramBinding, error) {
	return nil, nil
}

type fakeTemplates struct {
	tmpl *model.NotifyTemplate
	err  error
}

func (f fakeTemplates) GetByKey(context.Context, string) (*model.NotifyTemplate, error) {
	return f.tmpl, f.err
}

func sentTexts(api *fakeBotAPI) []string {
	out := []string{}
	for _, call := range api.calls("sendMessage") {
		out = append(out, call["text"].(string))
	}
	return out
}

// markupOf returns a sent message's reply markup. The Bot API client sends it
// as a JSON-encoded string; nil means the message had none.
func markupOf(t *testing.T, call map[string]any) map[string]any {
	t.Helper()
	switch v := call["reply_markup"].(type) {
	case nil:
		return nil
	case string:
		out := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(v), &out))
		return out
	case map[string]any:
		return v
	}
	t.Fatalf("unexpected reply_markup %T", call["reply_markup"])
	return nil
}

func startService(t *testing.T, api *fakeBotAPI, cfgMini string, deps BotDeps) {
	t.Helper()
	service, err := NewBotService(config.TelegramConfig{Enabled: true, BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", APIBaseURL: api.srv.URL, MiniAppURL: cfgMini}, deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.Start(ctx)
}

func TestStartBindsTheAccountOrExplainsWhyNot(t *testing.T) {
	api := newFakeBotAPI(t)
	bindings := &fakeBindings{}
	startService(t, api, "https://app.example", BotDeps{BotID: 1, BindingMgr: bindings})

	api.push(textUpdate("/start bind_abc123"))
	require.Eventually(t, func() bool { return len(sentTexts(api)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "bind_abc123", bindings.payload)
	assert.Equal(t, "Welcome to KubeForecast", sentTexts(api)[0])

	bindings.err = errors.New("token expired <b>")
	api.push(textUpdate("/start bind_old"))
	require.Eventually(t, func() bool { return len(sentTexts(api)) == 2 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "Binding failed", sentTexts(api)[1])

	// A plain /start greets and offers the Mini App.
	api.push(textUpdate("/start"))
	require.Eventually(t, func() bool { return len(sentTexts(api)) == 3 }, 5*time.Second, 20*time.Millisecond)
	assert.NotNil(t, markupOf(t, api.calls("sendMessage")[2]), "the greeting carries the Open KubeForecast button")
}

func TestStartUsesAdminTemplatesWhenTheyExist(t *testing.T) {
	api := newFakeBotAPI(t)
	tmpl := &model.NotifyTemplate{Key: model.TemplateKeyStartWelcomeMsg, BodyTemplate: "Hi there", Enabled: true}
	startService(t, api, "", BotDeps{BotID: 1, BindingMgr: &fakeBindings{}, Templates: fakeTemplates{tmpl: tmpl}})
	api.push(textUpdate("/start"))
	require.Eventually(t, func() bool { return len(sentTexts(api)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "Hi there", sentTexts(api)[0])
	assert.Nil(t, markupOf(t, api.calls("sendMessage")[0]), "no Mini App URL, no button")

	service := &BotService{deps: BotDeps{Templates: fakeTemplates{tmpl: &model.NotifyTemplate{BodyTemplate: "x", Enabled: false}}}}
	assert.Equal(t, "fallback", service.renderTextOrFallback("k", nil, "fallback"), "a disabled template is ignored")
	service.deps.Templates = fakeTemplates{err: errors.New("db down")}
	assert.Equal(t, "fallback", service.renderTextOrFallback("k", nil, "fallback"))
	service.deps.Templates = nil
	assert.Equal(t, "fallback", service.renderTextOrFallback("k", nil, "fallback"))
}

func TestRepliesCanCarryButtons(t *testing.T) {
	api := newFakeBotAPI(t)
	runner := &fakeRunner{reply: workflow.CommandReply{Text: "pick", Buttons: []workflow.Button{
		{Label: "Docs", ActionType: "url", Target: "https://example.com"},
		{Label: "Open", ActionType: "webapp", Target: "#/tickets/1"},
		{Label: "Approve", ActionType: "callback", Target: "approve:1"},
	}}}
	startService(t, api, "https://app.example", BotDeps{BotID: 1, WorkflowEngine: runner})

	api.push(textUpdate("/go"))
	require.Eventually(t, func() bool { return len(api.calls("sendMessage")) == 1 }, 5*time.Second, 20*time.Millisecond)
	rows, _ := markupOf(t, api.calls("sendMessage")[0])["inline_keyboard"].([]any)
	assert.Len(t, rows, 3)
	joined := ""
	for _, row := range rows {
		for _, btn := range row.([]any) {
			b := btn.(map[string]any)
			joined += b["text"].(string) + "|"
			if web, ok := b["web_app"].(map[string]any); ok {
				assert.Equal(t, "https://app.example#/tickets/1", web["url"], "a relative Mini App route resolves against the configured URL")
			}
		}
	}
	assert.Equal(t, "Docs|Open|Approve|", joined)

	// An empty reply sends nothing.
	runner.reply = workflow.CommandReply{}
	before := len(api.calls("sendMessage"))
	api.push(textUpdate("/quiet"))
	require.Eventually(t, func() bool { t, _, _ := runner.snapshot(); return len(t) == 2 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, before, len(api.calls("sendMessage")))
}

func TestANotePromptAsksForAReply(t *testing.T) {
	api := newFakeBotAPI(t)
	runner := &fakeRunner{outcome: workflow.ActionOutcome{PromptForNote: true}}
	startService(t, api, "", BotDeps{BotID: 1, WorkflowEngine: runner})
	api.push(map[string]any{"callback_query": map[string]any{
		"id": "cb", "chat_instance": "x", "data": "reject:9",
		"from":    map[string]any{"id": 99, "is_bot": false, "first_name": "Ann"},
		"message": map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": 99, "type": "private"}},
	}})
	require.Eventually(t, func() bool { return len(api.calls("sendMessage")) == 1 }, 5*time.Second, 20*time.Millisecond)
	sent := api.calls("sendMessage")[0]
	assert.NotEmpty(t, sent["text"], "a default prompt is used when the transition has none")
	assert.Equal(t, true, markupOf(t, sent)["force_reply"])

	// Malformed button data is acknowledged and ignored.
	for _, data := range []string{"nocolon", "approve:notanumber"} {
		api.push(map[string]any{"callback_query": map[string]any{
			"id": "cb2", "chat_instance": "x", "data": data,
			"from":    map[string]any{"id": 99, "is_bot": false, "first_name": "Ann"},
			"message": map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": 99, "type": "private"}},
		}})
	}
	require.Eventually(t, func() bool { return len(api.calls("answerCallbackQuery")) == 3 }, 5*time.Second, 20*time.Millisecond)
	_, _, actions := runner.snapshot()
	assert.Len(t, actions, 1)
	assert.True(t, strings.HasPrefix(actions[0], "reject:9"))
}
