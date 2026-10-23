// Package message holds the platform-neutral message every chat platform
// adapter produces. Modules downstream of an adapter (forwarding, LLM jobs,
// pipelines) depend on this type, never on a platform SDK object.
package message

import "time"

// Source types.
const (
	SourceChannel = "channel"
	SourceGroup   = "group"
	SourcePrivate = "private"
	SourceSystem  = "system"
	SourceBot     = "bot"
)

// Event types published by the Telegram Agent.
const (
	EventChannelMessage = "telegram.channel_message" // channel and group messages
	EventPrivateMessage = "telegram.private_message"
	EventSystemMessage  = "telegram.system_message" // Telegram service messages, login codes included
	EventCommand        = "telegram.command"
	EventAccountStatus  = "telegram.account_status"
)

// Message is the standard message. Platform-specific fields live in Metadata
// so they never leak into the shared schema.
type Message struct {
	Platform   string            `json:"platform"`
	Account    string            `json:"account"`
	Source     string            `json:"source"`
	SourceType string            `json:"source_type"`
	MessageID  string            `json:"message_id"`
	Sender     string            `json:"sender"`
	Text       string            `json:"text"`
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Event is what the agent dispatches internally.
type Event struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}

// Sink receives events from an adapter.
type Sink interface {
	Handle(event Event)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Event)

func (f SinkFunc) Handle(event Event) { f(event) }
