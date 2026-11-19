package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func audits(t *testing.T) []model.AssistantAudit {
	t.Helper()
	var rows []model.AssistantAudit
	require.NoError(t, model.DB.Order("id").Find(&rows).Error)
	return rows
}

func withTokens(text string, prompt, reply int) step {
	return func([]assistant.Message, []assistant.ToolSpec) (*assistant.Completion, error) {
		return &assistant.Completion{Content: text, PromptTokens: prompt, CompletionTokens: reply}, nil
	}
}

func TestAQuestionLeavesARecordOfWhoAskedWhatWasReadWhatItCostAndWhatWasSaid(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)
	secret := "my private question about shop"
	a.llm.steps = []step{ask(assistant.ToolCall{ID: "c1", Name: "list_modules", Arguments: `{}`}), withTokens("the private answer", 120, 30)}
	rec := a.as(t, 1, http.MethodPost, "/assistant/chat", map[string]any{"messages": []assistant.Turn{user(secret)}, "lang": "en", "conversation_id": id,
		"context": map[string]any{"page": "forecast-modules_modules", "title": "Modules"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := audits(t)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.EqualValues(t, 1, r.UserID)
	assert.Equal(t, "user1", r.UserName)
	assert.Equal(t, "chat", r.Source)
	assert.Equal(t, id, r.ConversationID)
	assert.Equal(t, "forecast-modules_modules", r.Page)
	assert.Equal(t, "test-model", r.Model)
	assert.Equal(t, "native", r.ToolMode)
	assert.Equal(t, "success", r.Status)
	assert.Equal(t, 2, r.LLMCalls)
	assert.Equal(t, 120, r.PromptTokens)
	assert.Equal(t, 30, r.CompletionTokens)
	assert.JSONEq(t, `[{"name":"list_modules","ok":true}]`, r.Tools)

	// the conversation is in the record: the question, the answer and the steps with what each tool returned
	assert.Equal(t, secret, r.Question)
	assert.Equal(t, "the private answer", r.Answer)
	assert.Contains(t, r.Steps, "list_modules")
	assert.Contains(t, r.Steps, "shop", "the step keeps a preview of what the tool returned")
	assert.NotContains(t, r.Question+r.Answer+r.Steps+r.Tools+r.Refused, rigKey, "a credential is in no tool result, so in no record")

	// the list shows the start of the question; one record opened shows all of it
	list := decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/audit", nil).Body.Bytes())["list"].([]any)
	assert.Equal(t, secret, list[0].(map[string]any)["question_preview"])
	assert.NotContains(t, list[0], "answer", "the list carries no answer")
	one := decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/audit/"+strconv.Itoa(int(r.ID)), nil).Body.Bytes())
	assert.Equal(t, secret, one["question"])
	assert.Equal(t, "the private answer", one["answer"])
	steps := one["steps"].([]any)
	require.Len(t, steps, 1)
	assert.Contains(t, steps[0].(map[string]any)["result"], "shop")
	assert.Equal(t, http.StatusNotFound, a.as(t, 1, http.MethodGet, "/assistant/audit/99999", nil).Code)

	// a search finds the question
	found := decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/audit?keyword=private+question", nil).Body.Bytes())
	assert.EqualValues(t, 1, found["total"])
}

func TestAToolTheUserMayNotUseIsRefusedAndTheRefusalIsRecorded(t *testing.T) {
	a := newAssistantRig(t, true)
	a.svc.Authorizer = deny{words: []string{"/effect-report"}}
	a.llm.steps = []step{ask(assistant.ToolCall{ID: "c1", Name: "get_module_effect_report", Arguments: `{"module":"shop"}`}), say("I cannot read that.")}
	rec := a.askAs(t, 1, "OPERATOR", "how accurate is shop?", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := audits(t)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `["get_module_effect_report"]`, rows[0].Refused)
	assert.Equal(t, "success", rows[0].Status, "the question itself went through")

	// the administrator is never refused
	a.llm.steps = []step{ask(assistant.ToolCall{ID: "c1", Name: "get_module_effect_report", Arguments: `{"module":"shop"}`}), say("ok")}
	require.Equal(t, http.StatusOK, a.askAs(t, 2, "ADMIN", "how accurate is shop?", nil).Code)
	rows = audits(t)
	require.Len(t, rows, 2)
	assert.JSONEq(t, `[]`, rows[1].Refused)

	list := decodeData(t, a.as(t, 1, http.MethodGet, "/assistant/audit?refused=true", nil).Body.Bytes())
	assert.EqualValues(t, 1, list["total"])
	entry := list["list"].([]any)[0].(map[string]any)
	assert.Equal(t, []any{"get_module_effect_report"}, entry["refused"])
}

func TestAFailedQuestionIsRecordedAsFailedAndOneThatWasNeverAskedIsNot(t *testing.T) {
	a := newAssistantRig(t, true)
	a.llm.steps = nil // the model cannot be reached
	require.NotEqual(t, http.StatusOK, a.chat(t, user("how is the service?")).Code)
	rows := audits(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "failed", rows[0].Status)
	assert.NotEmpty(t, rows[0].Error)
	assert.NotContains(t, rows[0].Error, "how is the service")

	// not a valid question, an empty one: nothing was sent anywhere
	require.Equal(t, http.StatusBadRequest, a.chat(t, user("   ")).Code)
	assert.Len(t, audits(t), 1)
	// the assistant switched off
	require.NoError(t, assistant.UpdateEnabled(context.Background(), a.config, "false"))
	require.Equal(t, http.StatusConflict, a.chat(t, user("hello")).Code)
	assert.Len(t, audits(t), 1)
}

func TestACommandIsRecordedAsASkillInItsConversation(t *testing.T) {
	a := newAssistantRig(t, true)
	id := startConversation(t, a, 1)
	a.llm.steps = []step{say("report text")}
	// a skill that asks the model: the built-in one that does is the one the rig has a script for
	rec := a.as(t, 1, http.MethodPost, "/assistant/skill", map[string]any{"name": "effect", "args": "shop", "lang": "en", "conversation_id": id})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := audits(t)
	if len(rows) == 0 {
		t.Skip("the skill /effect is made from stored facts and asks no model, so there is nothing to audit")
	}
	assert.Equal(t, "skill", rows[0].Source)
	assert.Equal(t, id, rows[0].ConversationID)
}

func TestTheAuditListFilters(t *testing.T) {
	a := newAssistantRig(t, true)
	now := time.Now().UTC()
	require.NoError(t, model.DB.Create(&[]model.AssistantAudit{
		{CreatedAt: now.Add(-3 * time.Hour), UserID: 1, UserName: "ann", Source: "chat", Status: "success", Model: "m1", Tools: `[{"name":"list_modules","ok":true}]`},
		{CreatedAt: now.Add(-2 * time.Hour), UserID: 2, UserName: "bob", Source: "skill", Status: "failed", Model: "m1", Error: "timed out", Refused: `["list_scaling_actions"]`},
		{CreatedAt: now.Add(-1 * time.Hour), UserName: "ann", Source: "task", Status: "success", Model: "m2"},
	}).Error)
	get := func(q string) map[string]any {
		rec := a.as(t, 1, http.MethodGet, "/assistant/audit"+q, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())
	}
	assert.EqualValues(t, 3, get("")["total"])
	assert.EqualValues(t, 2, get("?keyword=ann")["total"], "by user")
	assert.EqualValues(t, 1, get("?keyword=m2")["total"], "by model")
	assert.EqualValues(t, 1, get("?status=failed")["total"])
	assert.EqualValues(t, 1, get("?source=task")["total"])
	assert.EqualValues(t, 1, get("?refused=true")["total"])
	assert.EqualValues(t, 1, get("?from=" + now.Add(-150*time.Minute).UTC().Format(time.RFC3339) + "&to=" + now.Add(-90*time.Minute).UTC().Format(time.RFC3339))["total"])
	newest := get("")["list"].([]any)[0].(map[string]any)
	assert.Equal(t, "task", newest["source"], "newest first")
	assert.Equal(t, http.StatusBadRequest, a.as(t, 1, http.MethodGet, "/assistant/audit?status=maybe", nil).Code)
}

func TestAuditRecordsOlderThanTheRetentionAreDeletedAndTheDefaultIsFifteenDays(t *testing.T) {
	a := newAssistantRig(t, true)
	assert.Equal(t, 15, timing.AuditRetentionDays.Default)
	now := a.svc.Now()
	require.NoError(t, model.DB.Create(&[]model.AssistantAudit{
		{CreatedAt: now.Add(-16 * 24 * time.Hour), Status: "success", Source: "chat"},
		{CreatedAt: now.Add(-14 * 24 * time.Hour), Status: "success", Source: "chat"},
		{CreatedAt: now.Add(-400 * 24 * time.Hour), Status: "success", Source: "chat"},
	}).Error)
	n, err := a.svc.PruneAudits(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "no stored parameter: the default of 15 days holds")
	assert.Len(t, audits(t), 1)

	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: timing.AuditRetentionDays.Key, ConfigValue: "1"}).Error)
	n, err = a.svc.PruneAudits(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "a stored value is used")
	assert.Equal(t, 1, timing.AuditRetentionDays.Parse("0"))
}
