package assistant

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// Message is one turn of the conversation as the assistant keeps it: a user or assistant text, an assistant turn that asked for tools, or
// the answer to one tool call.
type Message struct {
	Role       string // system, user, assistant or tool
	Content    string
	ToolCalls  []ToolCall // on an assistant turn that asked for tools
	ToolCallID string     // on a tool turn: the call it answers
}

// ToolCall is the model asking for one tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON, as the model wrote it: it is checked before use
}

// ToolSpec is what the model is told about a tool.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema of an object
}

// Completion is one answer of the model: text, or the tools it wants before it can answer.
type Completion struct {
	Content   string
	ToolCalls []ToolCall
}

// LLM is a language model that can call tools.
type LLM interface {
	Complete(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int) (*Completion, error)
}

// openAI talks the OpenAI chat completions protocol with the official SDK; the endpoint decides which service answers.
type openAI struct {
	client      openai.Client
	model       string
	temperature *float64
}

// NewOpenAI builds the client of a service that speaks the OpenAI chat completions protocol, as the connection describes it: the address,
// the key, how it is reached (a proxy, no certificate check), the custom headers and the custom fields of the body. The SDK retries a
// failed request once on its own (it respects Retry-After); the timeout of the connection bounds one request, and the deadline of the whole
// question is the context's.
func NewOpenAI(c Connection) LLM {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.SkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // the administrator asked for it, per connection
	}
	if c.ProxyURL != "" {
		if u, err := url.Parse(c.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	opts := []option.RequestOption{option.WithBaseURL(c.Endpoint), option.WithAPIKey(c.APIKey), option.WithMaxRetries(1),
		option.WithHTTPClient(&http.Client{Transport: transport}), option.WithRequestTimeout(c.Timeout),
		option.WithHeader("User-Agent", "kube-res-foresight-assistant")}
	for _, h := range c.Headers {
		opts = append(opts, option.WithHeader(h.Key, h.Value))
	}
	for key, value := range c.CustomParams {
		opts = append(opts, option.WithJSONSet(key, value))
	}
	return &openAI{client: openai.NewClient(opts...), model: c.Model, temperature: c.Temperature}
}

func toParams(messages []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "system":
			out = append(out, openai.SystemMessage(m.Content))
		case "user":
			out = append(out, openai.UserMessage(m.Content))
		case "tool":
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		case "assistant":
			a := openai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" {
				a.Content.OfString = openai.String(m.Content)
			}
			for _, call := range m.ToolCalls {
				a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: call.ID, Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: call.Name, Arguments: call.Arguments}}})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
		}
	}
	return out
}

// Complete implements LLM.
func (o *openAI) Complete(ctx context.Context, messages []Message, tools []ToolSpec, maxTokens int) (*Completion, error) {
	params := openai.ChatCompletionNewParams{Model: o.model, Messages: toParams(messages)}
	if maxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(maxTokens))
	}
	if o.temperature != nil {
		params.Temperature = openai.Float(*o.temperature)
	}
	for _, t := range tools {
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: t.Name, Description: openai.String(t.Description), Parameters: shared.FunctionParameters(t.Parameters)}))
	}
	res, err := o.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, friendly(err)
	}
	if len(res.Choices) == 0 {
		return nil, errors.New("the language model answered nothing")
	}
	msg := res.Choices[0].Message
	out := &Completion{Content: msg.Content}
	for _, call := range msg.ToolCalls {
		if fn := call.AsFunction(); fn.ID != "" && fn.Function.Name != "" {
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: fn.ID, Name: fn.Function.Name, Arguments: fn.Function.Arguments})
		}
	}
	return out, nil
}

// friendly turns an SDK error into a sentence for the user. It never repeats the request, so the key cannot appear in it.
func friendly(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("the language model refused the key (%d): check the key of the connection", apiErr.StatusCode)
		case http.StatusNotFound:
			return fmt.Errorf("the language model service does not know the endpoint or the model (404): check the endpoint and the model of the connection")
		case http.StatusTooManyRequests:
			return errors.New("the language model service is limiting requests (429): try again in a moment")
		}
		return fmt.Errorf("the language model service answered %d: %s", apiErr.StatusCode, clip(apiErr.Message, 300))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("the language model did not answer in time")
	}
	return fmt.Errorf("could not reach the language model service: %s", clip(err.Error(), 300))
}

// clip cuts text to at most n bytes at a character boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
