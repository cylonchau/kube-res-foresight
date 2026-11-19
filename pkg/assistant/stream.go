package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
)

// The answer is shown as it is written, not when it is whole. A question that is answered in the background gives the page a function
// (WithTextStream); a model that can stream (the OpenAI protocol with stream=true) calls it with the text so far each time a piece arrives, and the
// page shows it. A service that cannot stream (a proxy that answers one JSON body, whatever the request says) is asked again without streaming, so
// nothing depends on it: the answer then comes whole, as before.

// errNoStream says the service did not stream: it answered nothing in pieces. The caller asks again, whole.
var errNoStream = errors.New("the service does not stream")

// Streamer is an LLM that can give its answer in pieces. onText is called with the whole text so far (not the piece), so a caller that rewrites
// the text (the names put back after they were hidden) never has to join pieces.
type Streamer interface {
	Stream(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int, onText func(full string)) (*Completion, error)
}

type textStreamKey struct{}

// WithTextStream gives a question the function that is told the text of the answer being written. An empty text says a new answer begins (the
// model asked for a tool, and writes again after it).
func WithTextStream(ctx context.Context, fn func(full string)) context.Context {
	return context.WithValue(ctx, textStreamKey{}, fn)
}

func textStreamOf(ctx context.Context) func(string) {
	fn, _ := ctx.Value(textStreamKey{}).(func(string))
	return fn
}

// completeOn asks the model for one answer, in pieces when the question has somewhere to show them and the model can, whole otherwise.
func completeOn(ctx context.Context, llm LLM, messages []Message, tools []ToolSpec, maxTokens int) (*Completion, error) {
	if fn := textStreamOf(ctx); fn != nil {
		if st, ok := llm.(Streamer); ok {
			fn("")
			done, err := st.Stream(ctx, messages, tools, maxTokens, fn)
			if !errors.Is(err, errNoStream) {
				return done, err
			}
		}
	}
	done, err := llm.Complete(ctx, messages, tools, maxTokens)
	if err == nil && done != nil && done.Content != "" {
		if fn := textStreamOf(ctx); fn != nil {
			fn(done.Content)
		}
	}
	return done, err
}

// planFilter keeps the page from showing a plan: a reply that starts like JSON (or a code fence) is a request for tools, not an answer.
func planFilter(fn func(string)) func(string) {
	return func(full string) {
		t := strings.TrimLeft(full, " \r\n\t")
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```") {
			return
		}
		fn(full)
	}
}

// Stream implements Streamer with the streaming of the chat completions protocol.
func (o *openAI) Stream(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int, onText func(string)) (*Completion, error) {
	stream := o.client.Chat.Completions.NewStreaming(ctx, o.params(messages, tools, maxTokens))
	defer stream.Close()
	acc := openai.ChatCompletionAccumulator{}
	var text strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 {
			if piece := chunk.Choices[0].Delta.Content; piece != "" {
				text.WriteString(piece)
				onText(text.String())
			}
		}
	}
	if err := stream.Err(); err != nil {
		if text.Len() == 0 && len(acc.Choices) == 0 {
			// it did not get as far as an answer: a service that does not stream, or one that refuses; asking whole says which
			return nil, errNoStream
		}
		return nil, friendly(err)
	}
	if len(acc.Choices) == 0 {
		return nil, errNoStream
	}
	msg := acc.Choices[0].Message
	out := &Completion{Content: msg.Content, PromptTokens: int(acc.Usage.PromptTokens), CompletionTokens: int(acc.Usage.CompletionTokens)}
	// the accumulator builds the union from the pieces, with no raw JSON behind it, so its fields are read as they are (AsFunction reads the JSON)
	for _, call := range msg.ToolCalls {
		if call.ID != "" && call.Function.Name != "" {
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return nil, errNoStream
	}
	return out, nil
}

// Stream implements Streamer for the counting wrapper.
func (m metered) Stream(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int, onText func(string)) (*Completion, error) {
	st, ok := m.inner.(Streamer)
	if !ok {
		return nil, errNoStream
	}
	done, err := st.Stream(ctx, messages, tools, maxTokens, onText)
	if errors.Is(err, errNoStream) {
		return nil, err
	}
	m.trail.mu.Lock()
	m.trail.calls++
	if done != nil {
		m.trail.prompt += done.PromptTokens
		m.trail.reply += done.CompletionTokens
	}
	m.trail.mu.Unlock()
	return done, err
}

// Stream implements Streamer for the wrapper that hides the names: the text so far is shown with the names put back each time.
func (l redactingLLM) Stream(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int, onText func(string)) (*Completion, error) {
	st, ok := l.inner.(Streamer)
	if !ok {
		return nil, errNoStream
	}
	done, err := st.Stream(ctx, l.hide(messages), tools, maxTokens, func(full string) { onText(l.r.Show(full)) })
	if err != nil || done == nil {
		return done, err
	}
	return l.show(done), nil
}

// Throttled passes the text to write at most once in each interval, and the last text when the interval is over, so a page that reads four times a
// second is not written to a hundred times a second by a model that writes fast. A text that is empty (a new answer begins) is written at once.
func Throttled(write func(string), every time.Duration) func(string) {
	var mu sync.Mutex
	var last time.Time
	var pending string
	var timer *time.Timer
	return func(text string) {
		mu.Lock()
		defer mu.Unlock()
		pending = text
		if text == "" || time.Since(last) >= every {
			last = time.Now()
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			write(text)
			return
		}
		if timer == nil {
			timer = time.AfterFunc(every-time.Since(last), func() {
				mu.Lock()
				defer mu.Unlock()
				timer = nil
				last = time.Now()
				write(pending)
			})
		}
	}
}
