package workflow

import "context"

// ChatDriver is the Sender trait: it delivers one already-rendered
// notification to one recipient on some chat platform. Telegram is the
// only implementation today (see pkg/telegram.TelegramChatDriver) —
// Teams/Slack/Feishu would each add their own implementation of this one
// interface, registered wherever that platform's driver is constructed
// (see pkg/cmd/server/server.go), without any change here. Every outbound
// send in this codebase — the engine's own pipeline notifications, the job
// engine's broadcast notifier, and any ad-hoc alert a caller wants to send
// — goes through a ChatDriver rather than each caller building its own
// platform-specific send request, so a given platform needs exactly one
// Sender implementation, not one per caller.
type ChatDriver interface {
	SendMessage(ctx context.Context, recipientID int64, text string, buttons []Button) error
}
