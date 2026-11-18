package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ToolMode is how the assistant gets data through a connection. Whatever the mode, the platform runs only the read tools of its registry, and
// what the model writes is data, never a command.
type ToolMode string

const (
	// ModeAuto is the setting of a connection whose mode is found out by asking it; the answer is one of the three below.
	ModeAuto ToolMode = "auto"
	// ModeNative is the OpenAI tool calling: the model asks for the tools itself, and the answer to each goes back as a tool message.
	ModeNative ToolMode = "native"
	// ModeText is for a service that ignores the tools of a request: the model is told what the platform can read and writes a JSON plan, which
	// the platform checks against the registry and the permissions of the user before it runs it; the data goes back as a user message.
	ModeText ToolMode = "text"
	// ModeRouter is for a service that cannot even write a plan: the platform reads by rules from the question and the page, and the model only
	// explains what was read.
	ModeRouter ToolMode = "router"
)

// ParseToolMode reads the mode of a form or a row; empty is auto, anything else that is not a mode is an error.
func ParseToolMode(s string) (ToolMode, error) {
	switch m := ToolMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeNative, ModeText, ModeRouter:
		return m, nil
	}
	return "", fmt.Errorf("the tool mode %q is not one of auto, native, text, router", s)
}

// modeKnown is what the service remembers of the connections it has asked: a mode found out is kept in the database too (so a restart does not
// ask again), but a connection read for a question is read from the database each time, so the memory also covers a row that was not saved yet.
type modeKnown struct {
	mu sync.Mutex
	by map[string]ToolMode
}

func (k *modeKnown) get(key string) (ToolMode, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, ok := k.by[key]
	return m, ok
}

func (k *modeKnown) set(key string, m ToolMode) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.by == nil {
		k.by = map[string]ToolMode{}
	}
	k.by[key] = m
}

// modeKey names what the mode depends on: the connection and the service and model it points at.
func modeKey(c Connection) string { return fmt.Sprintf("%d|%s|%s", c.ID, c.Endpoint, c.Model) }

// modeOf is the mode a question to the connection is answered in: the one the administrator chose, else the one found out (asked once and
// kept). A connection that cannot be asked yet is taken as native, which is what it was before the modes existed.
func (s *Service) modeOf(ctx context.Context, conn Connection) ToolMode {
	if conn.ToolMode != "" && conn.ToolMode != ModeAuto {
		return conn.ToolMode
	}
	if conn.ToolModeDetected != "" && conn.ToolModeDetected != ModeAuto {
		return conn.ToolModeDetected
	}
	if m, ok := s.known.get(modeKey(conn)); ok {
		return m
	}
	found := s.DetectToolMode(ctx, conn)
	if found.Mode == "" {
		return ModeNative // it could not be asked (it was unreachable): not remembered, asked again next time
	}
	s.known.set(modeKey(conn), found.Mode)
	if s.Connections != nil && conn.ID != 0 {
		_ = s.Connections.SetToolModeDetected(ctx, conn.ID, string(found.Mode), s.now())
	}
	return found.Mode
}

// ToolProbe is what asking a connection about tools found.
type ToolProbe struct {
	// Mode is native, text or router; empty when the connection could not be asked.
	Mode ToolMode `json:"mode"`
	// Detail says in a sentence how it was decided.
	Detail string `json:"detail"`
}

// probeTool is the tool of the probe: it does nothing, and a service that calls it has shown that it takes tools.
var probeTool = ToolSpec{Name: "ping_tool", Description: "Returns the word pong. Call it with the value you are asked to send.",
	Parameters: object(map[string]any{"value": map[string]any{"type": "string", "description": "The value to send."}}, "value")}

// DetectToolMode asks the connection two small questions and says how it can be used. First it offers a tool and asks for a call: a service that
// answers with a tool call takes tools (native). A service that answers in words may still follow a protocol written in the prompt: it is asked
// to reply with a JSON plan for the same tool, and one that does is text. A service that does neither is router. Accepting the tools parameter
// proves nothing (a proxy that ignores it accepts it), which is why the answer is looked at.
func (s *Service) DetectToolMode(ctx context.Context, conn Connection) ToolProbe {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	llm := s.llmFor(conn)

	first, err := llm.Complete(ctx, []Message{
		{Role: "system", Content: "You answer by calling a tool. Do not answer in words."},
		{Role: "user", Content: `Call the tool ping_tool now with the value "abc".`},
	}, []ToolSpec{probeTool}, 256)
	if err != nil {
		return ToolProbe{Detail: "the connection could not be asked: " + err.Error()}
	}
	for _, call := range first.ToolCalls {
		if call.Name == probeTool.Name {
			return ToolProbe{Mode: ModeNative, Detail: "the service answered the tool with a tool call"}
		}
	}

	second, err := llm.Complete(ctx, []Message{
		{Role: "system", Content: textProtocolRules() + "\n\nTools:\n- ping_tool(value: string): returns the word pong."},
		{Role: "user", Content: `Use ping_tool with the value "abc".`},
	}, nil, 256)
	if err != nil {
		return ToolProbe{Detail: "the connection could not be asked: " + err.Error()}
	}
	if plan, ok := parsePlan(second.Content); ok && len(plan.Calls) > 0 && plan.Calls[0].Tool == probeTool.Name {
		return ToolProbe{Mode: ModeText, Detail: "the service ignores tool calls but follows the JSON plan written in the prompt"}
	}
	return ToolProbe{Mode: ModeRouter, Detail: "the service neither calls tools nor follows the JSON plan: the platform reads by rules and the model explains"}
}
