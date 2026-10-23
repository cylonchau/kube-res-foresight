package workflow

import "context"

// PendingReplyStore tracks a chat user who was just asked (via the
// driver's platform-native "reply to this message" mechanism) to type a
// note/reason for a transition that declared RequireNote — consumed by
// that user's next text message in HandleIncomingText, or ignored once it
// expires on its own. One pending reply per user: a new prompt replaces
// whatever was pending before.
type PendingReplyStore interface {
	Begin(ctx context.Context, platformUserID, instanceID int64, event string) error
	Consume(ctx context.Context, platformUserID int64) (instanceID int64, event string, ok bool, err error)
}
