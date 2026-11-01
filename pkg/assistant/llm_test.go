package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	llm := NewOpenAI(Connection{Provider: "openai", Endpoint: srv.URL + "/v1", APIKey: "sk-secret", Model: "test-model", Timeout: 10 * time.Second})
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
		llm := NewOpenAI(Connection{Endpoint: srv.URL, APIKey: "sk-very-secret", Model: "m", Timeout: 5 * time.Second})
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
	_, err := NewOpenAI(Connection{Endpoint: url, APIKey: "sk-very-secret", Model: "m", Timeout: 2 * time.Second}).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
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

// The advanced settings of a connection reach the request: custom headers, extra fields of the body, the temperature, the reply length.
func TestTheAdvancedSettingsOfAConnectionReachTheRequest(t *testing.T) {
	f := &fakeOpenAI{reply: textReply}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	temperature := 0.3
	llm := NewOpenAI(Connection{Endpoint: srv.URL + "/v1", APIKey: "sk-secret", Model: "m", Timeout: 5 * time.Second,
		Headers:      []Header{{Key: "HTTP-Referer", Value: "https://console.example.com"}, {Key: "X-Title", Value: "kube-resource-fcst"}},
		CustomParams: map[string]any{"top_p": 0.9, "reasoning": map[string]any{"effort": "low"}}, Temperature: &temperature})
	_, err := llm.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 777)
	require.NoError(t, err)
	assert.Equal(t, "https://console.example.com", f.headers[0].Get("HTTP-Referer"))
	assert.Equal(t, "kube-resource-fcst", f.headers[0].Get("X-Title"))
	sent := f.seen[0]
	assert.EqualValues(t, 0.9, sent["top_p"])
	assert.Equal(t, map[string]any{"effort": "low"}, sent["reasoning"])
	assert.EqualValues(t, 0.3, sent["temperature"])
	assert.EqualValues(t, 777, sent["max_completion_tokens"])

	// nothing set: nothing sent that the service did not ask for
	f2 := &fakeOpenAI{reply: textReply}
	srv2 := httptest.NewServer(f2)
	t.Cleanup(srv2.Close)
	_, err = NewOpenAI(Connection{Endpoint: srv2.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second}).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
	require.NoError(t, err)
	for _, key := range []string{"temperature", "max_completion_tokens", "top_p", "tools"} {
		assert.NotContains(t, f2.seen[0], key)
	}
}

// A service with a certificate nobody trusts is reached only when the connection says so; a proxy is used when there is one.
func TestACertificateNobodyTrustsIsAcceptedOnlyWhenTheConnectionSaysSoAndAProxyIsUsed(t *testing.T) {
	f := &fakeOpenAI{reply: textReply}
	tls := httptest.NewTLSServer(f)
	t.Cleanup(tls.Close)
	base := Connection{Endpoint: tls.URL + "/v1", APIKey: "k", Model: "m", Timeout: 5 * time.Second}
	_, err := NewOpenAI(base).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
	require.Error(t, err, "the certificate of the test server is not trusted")
	assert.Contains(t, err.Error(), "could not reach")
	base.SkipTLSVerify = true
	_, err = NewOpenAI(base).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
	require.NoError(t, err)

	// a proxy: the request goes to it, not to the address of the service
	seen := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(textReply))
	}))
	t.Cleanup(proxy.Close)
	_, err = NewOpenAI(Connection{Endpoint: "http://llm.internal.invalid/v1", APIKey: "k", Model: "m", Timeout: 5 * time.Second, ProxyURL: proxy.URL}).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0)
	require.NoError(t, err)
	u, _ := url.Parse(<-seen)
	assert.Equal(t, "llm.internal.invalid", u.Host)
	assert.Equal(t, "/v1/chat/completions", u.Path)
}

func TestTheHistoryIsShortenedFromTheOldEndToFitTheContextOfTheModel(t *testing.T) {
	long := strings.Repeat("a", 3000) // about 1000 tokens
	history := []Turn{{Role: "user", Content: long}, {Role: "assistant", Content: long}, {Role: "user", Content: long}, {Role: "assistant", Content: long}, {Role: "user", Content: "the question"}}
	assert.Len(t, trimHistory(history, 0, 0), 5, "unknown context: nothing is dropped")
	// 2500 for the rules and the tools, 500 for the reply: 3500 left of 6500, room for three long messages and the question
	got := trimHistory(history, 6500, 500)
	assert.Equal(t, "the question", got[len(got)-1].Content)
	assert.Less(t, len(got), 5)
	assert.GreaterOrEqual(t, len(got), 2)
	assert.Equal(t, history[len(history)-len(got):], got, "the newest messages are the ones kept")
	// a context that cannot hold even the question still sends the question
	tiny := trimHistory(history, 1024, 500)
	assert.Equal(t, []Turn{{Role: "user", Content: "the question"}}, tiny)
}
