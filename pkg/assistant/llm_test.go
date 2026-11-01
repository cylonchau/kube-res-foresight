package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenAI is a service that speaks the OpenAI chat completions protocol, enough to see what the client sends and to answer it.
type fakeOpenAI struct {
	status  int
	reply   string
	seen    []map[string]any
	headers []http.Header
	paths   []string
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	f.seen, f.headers, f.paths = append(f.seen, m), append(f.headers, r.Header.Clone()), append(f.paths, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"error":{"message":"nope","type":"invalid_request_error"}}`)
		return
	}
	_, _ = io.WriteString(w, f.reply)
}

const toolCallReply = `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_service_status","arguments":"{\"hours\":6}"}}]}}]}`

const textReply = `{"id":"c2","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"All modules are healthy."}}]}`

func TestTheClientSpeaksTheOpenAIProtocolWithTheToolsAndReadsAToolCall(t *testing.T) {
	f := &fakeOpenAI{reply: toolCallReply}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	llm := NewOpenAI(srv.URL+"/v1", "sk-secret", "test-model", 10*time.Second)
	tools := []ToolSpec{{Name: "get_service_status", Description: "status", Parameters: object(map[string]any{"hours": hoursProp})}}

	got, err := llm.Complete(context.Background(), []Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "how is it?"}}, tools, 500)
	require.NoError(t, err)
	require.Len(t, got.ToolCalls, 1)
	assert.Equal(t, ToolCall{ID: "call_1", Name: "get_service_status", Arguments: `{"hours":6}`}, got.ToolCalls[0])

	assert.Equal(t, "/v1/chat/completions", f.paths[0])
	assert.Equal(t, "Bearer sk-secret", f.headers[0].Get("Authorization"))
	assert.Equal(t, "kube-resource-fcst-assistant", f.headers[0].Get("User-Agent"))
	sent := f.seen[0]
	assert.Equal(t, "test-model", sent["model"])
	assert.EqualValues(t, 500, sent["max_completion_tokens"])
	msgs := sent["messages"].([]any)
	assert.Equal(t, "system", msgs[0].(map[string]any)["role"])
	assert.Equal(t, "user", msgs[1].(map[string]any)["role"])
	offered := sent["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, "function", offered["type"])
	assert.Equal(t, "get_service_status", offered["function"].(map[string]any)["name"])

	// the next turn carries the call and its answer back, the way the protocol wants them
	f.reply = textReply
	got, err = llm.Complete(context.Background(), []Message{{Role: "user", Content: "how is it?"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "get_service_status", Arguments: `{"hours":6}`}}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"ok":true}`}}, tools, 500)
	require.NoError(t, err)
	assert.Equal(t, "All modules are healthy.", got.Content)
	assert.Empty(t, got.ToolCalls)
	second := f.seen[1]["messages"].([]any)
	asked := second[1].(map[string]any)
	assert.Equal(t, "assistant", asked["role"])
	call := asked["tool_calls"].([]any)[0].(map[string]any)
	assert.Equal(t, "call_1", call["id"])
	assert.Equal(t, "get_service_status", call["function"].(map[string]any)["name"])
	answered := second[2].(map[string]any)
	assert.Equal(t, "tool", answered["role"])
	assert.Equal(t, "call_1", answered["tool_call_id"])
}

func TestAFailureOfTheLanguageModelServiceIsSaidInWordsAndNeverCarriesTheKey(t *testing.T) {
	for status, want := range map[int]string{401: "refused the key", 403: "refused the key", 404: "endpoint or the model", 429: "limiting requests", 500: "answered 500"} {
		f := &fakeOpenAI{status: status}
		srv := httptest.NewServer(f)
		llm := NewOpenAI(srv.URL, "sk-very-secret", "m", 5*time.Second)
		_, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
		srv.Close()
		require.Error(t, err, status)
		assert.Contains(t, err.Error(), want, status)
		assert.NotContains(t, err.Error(), "sk-very-secret")
	}

	// nothing listens: the error says it could not be reached, and still not the key
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := NewOpenAI(url, "sk-very-secret", "m", 2*time.Second).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not reach")
	assert.NotContains(t, err.Error(), "sk-very-secret")
}

func TestAnEndpointIsCheckedAndATrailingSlashIsDropped(t *testing.T) {
	got, err := NormalizeEndpoint(" https://api.example.com/v1/ ")
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com/v1", got)
	for _, bad := range []string{"", "api.example.com", "ftp://x/v1", "https://user:pw@x/v1", "https://x/v1?key=1", "https://x/v1#a", "http://"} {
		_, err := NormalizeEndpoint(bad)
		assert.Error(t, err, bad)
	}
	assert.Equal(t, "ab…", clip("abcdef", 2))
	assert.True(t, strings.HasSuffix(clip(strings.Repeat("中", 10), 4), "…"), "cut at a character boundary")
}
