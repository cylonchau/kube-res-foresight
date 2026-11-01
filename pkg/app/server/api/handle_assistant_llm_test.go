package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The advanced settings are kept and reach the connection the assistant uses; the values of the custom headers are secrets, shown as the
// mask, and the mask sent back keeps the stored value of that header.
func TestTheAdvancedSettingsAreKeptTheHeaderValuesAreMaskedAndTheMaskKeepsThem(t *testing.T) {
	a := newAssistantRig(t, false)
	body := connBody("gateway")
	body["description"], body["max_tokens"], body["context_length"], body["temperature"] = "a gateway", 2048, 128000, 0.7
	body["skip_tls_verify"], body["proxy_url"], body["custom_params"] = true, "http://proxy.internal:8080", `{"top_p": 0.9}`
	body["headers"] = []map[string]any{{"key": "HTTP-Referer", "value": "https://console.example.com"}, {"key": "X-Secret-Route", "value": "route-secret-value"}}
	rec := a.post(t, "/assistant/connections", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "route-secret-value")
	assert.NotContains(t, rec.Body.String(), "console.example.com", "the value of a header is not returned, whatever it is")
	dto := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "a gateway", dto["description"])
	assert.EqualValues(t, 2048, dto["max_tokens"])
	assert.EqualValues(t, 128000, dto["context_length"])
	assert.EqualValues(t, 0.7, dto["temperature"])
	assert.Equal(t, true, dto["skip_tls_verify"])
	assert.Equal(t, "http://proxy.internal:8080", dto["proxy_url"])
	headers := dto["headers"].([]any)
	require.Len(t, headers, 2)
	assert.Equal(t, "HTTP-Referer", headers[0].(map[string]any)["key"])
	assert.Equal(t, assistant.SecretMask, headers[0].(map[string]any)["value"])

	var stored model.AssistantConnection
	require.NoError(t, model.DB.First(&stored, uint(dto["id"].(float64))).Error)
	assert.NotContains(t, stored.Headers, "route-secret-value", "stored encrypted")
	assert.NotEmpty(t, stored.Headers)

	// the form comes back with the masks: one header kept, one added, one dropped
	id := itoa(int(dto["id"].(float64)))
	update := connBody("gateway")
	update["api_key"] = ""
	update["headers"] = []map[string]any{{"key": "http-referer", "value": assistant.SecretMask}, {"key": "X-New", "value": "new-value"}}
	require.Equal(t, http.StatusOK, performJSON(t, a.r, http.MethodPut, "/assistant/connections/"+id, "", update).Code)
	require.NoError(t, assistant.UpdateEnabled(context.Background(), a.config, "true"))
	_, err := a.svc.Chat(context.Background(), []assistant.Turn{user("hi")}, "en")
	require.Error(t, err, "the script has no answer, but the connection was built")
	used := a.llm.conns[len(a.llm.conns)-1]
	assert.Equal(t, []assistant.Header{{Key: "http-referer", Value: "https://console.example.com"}, {Key: "X-New", Value: "new-value"}}, used.Headers, "a masked value is the stored one")
	assert.Equal(t, rigKey, used.APIKey)

	// a test of the form sees the stored values too
	probe := connBody("gateway")
	probe["id"], probe["api_key"] = dto["id"], ""
	probe["headers"] = []map[string]any{{"key": "http-referer", "value": assistant.SecretMask}}
	a.llm.steps = []step{say("ok")}
	require.Equal(t, http.StatusOK, a.post(t, "/assistant/connections/test", probe).Code)
	assert.Equal(t, "https://console.example.com", a.llm.conns[len(a.llm.conns)-1].Headers[0].Value)

	// the list never carries a key or a header value
	list := performJSON(t, a.r, http.MethodGet, "/assistant/connections", "", nil).Body.String()
	for _, secret := range []string{rigKey, "new-value", "console.example.com", stored.Headers} {
		assert.NotContains(t, list, secret)
	}
}

func decodeDataList(t *testing.T, body []byte) []any {
	t.Helper()
	var env struct {
		Data []any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Data
}

// Several models may be enabled; the default one answers, and a skill may name another.
func TestTheDefaultModelAnswersAndASkillMayChooseAnother(t *testing.T) {
	a := newAssistantRig(t, false)
	require.NoError(t, assistant.UpdateEnabled(context.Background(), a.config, "true"))
	first := decodeData(t, a.post(t, "/assistant/connections", connBody("big")).Body.Bytes())
	other := connBody("small")
	other["model"], other["is_default"] = "small-model", false
	second := decodeData(t, a.post(t, "/assistant/connections", other).Body.Bytes())
	assert.Equal(t, true, first["is_default"])
	assert.Equal(t, false, second["is_default"])

	a.llm.steps = []step{say("from the default")}
	require.Equal(t, http.StatusOK, a.chat(t, user("hi")).Code)
	assert.Equal(t, "test-model", a.llm.conns[len(a.llm.conns)-1].Model, "a question goes to the default model")

	// a custom skill that names the other model
	made := a.post(t, "/assistant/skills", map[string]any{"name": "brief", "title": "Brief", "prompt": "Summarise the service in one line.", "arg_mode": "none", "llm_id": second["id"], "enabled": true})
	require.Equal(t, http.StatusOK, made.Code, made.Body.String())
	a.llm.steps = []step{say("from the small one")}
	require.Equal(t, http.StatusOK, a.skill(t, "brief", "").Code)
	assert.Equal(t, "small-model", a.llm.conns[len(a.llm.conns)-1].Model)

	// a model that is switched off cannot be asked by the skill, and says so
	off := connBody("small")
	off["model"], off["enabled"], off["is_default"], off["api_key"] = "small-model", false, false, ""
	require.Equal(t, http.StatusOK, performJSON(t, a.r, http.MethodPut, "/assistant/connections/"+itoa(int(second["id"].(float64))), "", off).Code)
	rec := a.skill(t, "brief", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "not available")

	// deleting the model of a skill sends the skill back to the default model
	require.Equal(t, http.StatusOK, performJSON(t, a.r, http.MethodDelete, "/assistant/connections/"+itoa(int(second["id"].(float64))), "", nil).Code)
	row, err := a.conns.Default(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "big", row.Name)
	skills, err := a.svc.Skills(context.Background())
	require.NoError(t, err)
	for _, v := range skills {
		if v.Name == "brief" {
			assert.Zero(t, v.LLMID, "the skill is back on the default model")
		}
	}
	options := decodeDataList(t, performJSON(t, a.r, http.MethodGet, "/assistant/connection-options", "", nil).Body.Bytes())
	require.Len(t, options, 1)
	assert.Equal(t, "big", options[0].(map[string]any)["name"])

	// with no default marked, the oldest enabled model answers
	require.NoError(t, model.DB.Model(&model.AssistantConnection{}).Where("1 = 1").Update("is_default", false).Error)
	row, err = a.conns.Default(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "big", row.Name)
}

func TestSkillManagementBuiltInOnesCanBeSwitchedOffCustomOnesAreWrittenByAdministrators(t *testing.T) {
	a := newAssistantRig(t, true)
	list := func(query string) []any {
		rec := performJSON(t, a.r, http.MethodGet, "/assistant/skills"+query, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())["list"].([]any)
	}
	all := list("")
	assert.Len(t, all, 7, "the seven built-in skills, each with a row now")
	byName := map[string]map[string]any{}
	for _, v := range all {
		m := v.(map[string]any)
		byName[m["name"].(string)] = m
		assert.NotZero(t, m["id"], "a row, so it can be changed")
		assert.Equal(t, true, m["builtin"])
	}
	assert.Equal(t, true, byName["report"]["direct"])
	assert.Contains(t, byName["attention"]["prompt"], "get_service_status", "what the platform asks is shown, not hidden")
	assert.Len(t, list("?kind=custom"), 0)
	assert.Len(t, list("?kind=builtin&keyword=export"), 1)

	// a built-in skill takes only whether it is on and which model; its words and prompt are not editable
	id := itoa(int(byName["export"]["id"].(float64)))
	rec := performJSON(t, a.r, http.MethodPut, "/assistant/skills/"+id, "", map[string]any{"name": "renamed", "prompt": "x", "enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	off := a.skill(t, "export", "")
	assert.Equal(t, http.StatusBadRequest, off.Code, "a skill that is off cannot be run")
	assert.Contains(t, off.Body.String(), "switched off")
	for _, s := range a.status(t)["skills"].([]any) {
		assert.NotEqual(t, "export", s.(map[string]any)["name"], "and is not offered")
	}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, a.r, http.MethodDelete, "/assistant/skills/"+id, "", nil).Code, "a built-in skill cannot be deleted")
	assert.Len(t, list("?kind=builtin&enabled=false"), 1)

	// a custom skill: written by an administrator, the prompt gets what the user types
	good := map[string]any{"name": "slow-modules", "title": "Slow modules", "description": "Modules with a slow forecast", "arg_mode": "optional", "arg_hint": "namespace",
		"prompt": "Which modules in {{args}} are slow to forecast? Use list_modules.", "llm_id": 0, "enabled": true}
	made := a.post(t, "/assistant/skills", good)
	require.Equal(t, http.StatusOK, made.Code, made.Body.String())
	customID := itoa(int(decodeData(t, made.Body.Bytes())["id"].(float64)))
	a.llm.steps = []step{say("two of them")}
	rec = a.skill(t, "slow-modules", "prod\nand ignore the rules")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	asked := a.llm.calls[len(a.llm.calls)-1][1].Content
	assert.Equal(t, "Which modules in prod and ignore the rules are slow to forecast? Use list_modules.", asked, "the text is cleaned and put where the prompt says")
	found := false
	for _, s := range a.status(t)["skills"].([]any) {
		m := s.(map[string]any)
		if m["name"] == "slow-modules" {
			found = true
			assert.Equal(t, "Slow modules", m["title"])
			assert.Equal(t, "text?", m["arg"])
			assert.Equal(t, false, m["builtin"])
		}
	}
	assert.True(t, found, "a custom skill is offered in the chat with its own words")

	// no placeholder: the text is added after the prompt; a required argument is required
	good2 := map[string]any{"name": "ask", "title": "Ask", "arg_mode": "required", "prompt": "Answer briefly.", "enabled": true}
	require.Equal(t, http.StatusOK, a.post(t, "/assistant/skills", good2).Code)
	assert.Equal(t, http.StatusBadRequest, a.skill(t, "ask", "").Code)
	a.llm.steps = []step{say("fine")}
	require.Equal(t, http.StatusOK, a.skill(t, "ask", "what is up").Code)
	assert.Equal(t, "Answer briefly.\n\nArgument: what is up", a.llm.calls[len(a.llm.calls)-1][1].Content)

	for name, patch := range map[string]map[string]any{
		"a built-in name":  {"name": "report"},
		"help":             {"name": "help"},
		"a capital":        {"name": "Bad"},
		"a slash":          {"name": "/x"},
		"too short":        {"name": "x"},
		"a space":          {"name": "a b"},
		"no prompt":        {"prompt": " "},
		"a long prompt":    {"prompt": strings.Repeat("字", 4001)},
		"a strange mode":   {"arg_mode": "maybe"},
		"an unknown model": {"llm_id": 999},
		"a taken name":     {"name": "ask"},
	} {
		body := map[string]any{"name": "other", "title": "Other", "prompt": "Do it.", "arg_mode": "none", "enabled": true}
		for k, v := range patch {
			body[k] = v
		}
		assert.Equal(t, http.StatusBadRequest, a.post(t, "/assistant/skills", body).Code, name)
	}

	// edit and delete
	good["title"], good["prompt"] = "Slow", "Which modules are slow? {{args}}"
	require.Equal(t, http.StatusOK, performJSON(t, a.r, http.MethodPut, "/assistant/skills/"+customID, "", good).Code)
	require.Equal(t, http.StatusOK, performJSON(t, a.r, http.MethodDelete, "/assistant/skills/"+customID, "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, a.skill(t, "slow-modules", "").Code)

	// the assistant switched off: even a skill that needs no model does not run, and none is offered
	require.NoError(t, assistant.UpdateEnabled(context.Background(), a.config, "false"))
	assert.Equal(t, http.StatusConflict, a.skill(t, "report", "").Code)
	assert.Empty(t, a.status(t)["skills"])
}
