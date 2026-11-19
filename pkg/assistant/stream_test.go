package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func chunk(delta string) string {
	return `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{` + delta + `},"finish_reason":null}]}`
}

func connTo(url string) Connection {
	return Connection{Endpoint: url, APIKey: "k", Model: "m", Timeout: 10 * time.Second}
}

func TestTheAnswerIsGivenToTheCallerAsItIsWrittenWithTheTextSoFar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, readBody(r), `"stream":true`)
		sse(w, chunk(`"role":"assistant","content":"One "`), chunk(`"content":"module, "`), chunk(`"content":"shop."`))
	}))
	defer srv.Close()
	var seen []string
	done, err := NewOpenAI(connTo(srv.URL)).(Streamer).Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 100, func(full string) { seen = append(seen, full) })
	require.NoError(t, err)
	assert.Equal(t, []string{"One ", "One module, ", "One module, shop."}, seen, "each call has the whole text so far")
	assert.Equal(t, "One module, shop.", done.Content)
}

func TestAToolCallIsAssembledFromThePiecesOfAStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			chunk(`"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_modules","arguments":""}}]`),
			chunk(`"tool_calls":[{"index":0,"function":{"arguments":"{\"mod"}}]`),
			chunk(`"tool_calls":[{"index":0,"function":{"arguments":"ule\":\"shop\"}"}}]`),
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	done, err := NewOpenAI(connTo(srv.URL)).(Streamer).Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, []ToolSpec{{Name: "list_modules", Parameters: object(map[string]any{})}}, 100, func(string) {})
	require.NoError(t, err)
	require.Len(t, done.ToolCalls, 1)
	assert.Equal(t, "list_modules", done.ToolCalls[0].Name)
	assert.JSONEq(t, `{"module":"shop"}`, done.ToolCalls[0].Arguments)
}

func TestAServiceThatDoesNotStreamIsAskedAgainWholeAndTheAnswerStillComes(t *testing.T) {
	asked := []bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streaming := strings.Contains(readBody(r), `"stream":true`)
		asked = append(asked, streaming)
		w.Header().Set("Content-Type", "application/json")
		// whatever the request says, one JSON body: like a proxy that ignores stream
		fmt.Fprint(w, `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"whole answer"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	var shown []string
	ctx := WithTextStream(context.Background(), func(full string) { shown = append(shown, full) })
	done, err := completeOn(ctx, NewOpenAI(connTo(srv.URL)), []Message{{Role: "user", Content: "hi"}}, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, "whole answer", done.Content)
	assert.Equal(t, []bool{true, false}, asked, "it tried to stream, then asked whole")
	assert.Equal(t, "whole answer", shown[len(shown)-1], "the page still gets the answer")
}

func TestAReplyThatStartsLikeAPlanIsNotShownAndAnAnswerIs(t *testing.T) {
	var shown []string
	fn := planFilter(func(s string) { shown = append(shown, s) })
	for _, text := range []string{"", `{"calls":[`, "```json\n{", `  {"answer"`, "All ", "All quiet."} {
		fn(text)
	}
	assert.Equal(t, []string{"", "All ", "All quiet."}, shown)
}

type streamingFake struct {
	pieces []string
}

func (s *streamingFake) Complete(context.Context, []Message, []ToolSpec, int) (*Completion, error) {
	return &Completion{Content: strings.Join(s.pieces, "")}, nil
}

func (s *streamingFake) Stream(_ context.Context, _ []Message, _ []ToolSpec, _ int, onText func(string)) (*Completion, error) {
	full := ""
	for _, p := range s.pieces {
		full += p
		onText(full)
	}
	return &Completion{Content: full}, nil
}

func TestTheNamesAreShownInTheTextWhileTheModelWritesTheAliases(t *testing.T) {
	r := redactorOf(mods())
	alias := r.Hide("shop")
	llm := redactingLLM{inner: &streamingFake{pieces: []string{"The module ", alias, " is fine."}}, r: r}
	var shown []string
	done, err := llm.Stream(context.Background(), []Message{{Role: "user", Content: "how is shop?"}}, nil, 100, func(full string) { shown = append(shown, full) })
	require.NoError(t, err)
	assert.Equal(t, "The module shop is fine.", done.Content)
	assert.Equal(t, "The module shop is fine.", shown[len(shown)-1])
	for _, s := range shown {
		assert.NotContains(t, s, "module-", "the user is never shown an alias")
	}
}

func TestTheTextIsWrittenAtMostOnceInAnIntervalAndTheLastOneIsNeverLost(t *testing.T) {
	var mu sync.Mutex
	var written []string
	fn := Throttled(func(s string) { mu.Lock(); written = append(written, s); mu.Unlock() }, 60*time.Millisecond)
	fn("")
	for i := 1; i <= 20; i++ {
		fn(strings.Repeat("x", i))
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Less(t, len(written), 12, "twenty pieces in a hundred milliseconds are not twenty writes")
	assert.Equal(t, strings.Repeat("x", 20), written[len(written)-1], "the last text is written")
}

func readBody(r *http.Request) string {
	b := make([]byte, 1<<16)
	n, _ := r.Body.Read(b)
	return string(b[:n])
}
