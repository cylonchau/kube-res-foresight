package workflow

// ChatUser is the platform-native identity of whoever sent an inbound
// message/action — passed through generically so command handlers that
// need real identity (e.g. confirm_bind) have it without the engine
// needing to know any specific chat platform's user struct shape.
//
// PlatformUserID is int64 (Telegram's native numeric user ID) because
// Telegram is the only chat driver implemented today. A platform whose
// native identity is a string (Teams' Azure AD object ID, Slack's member
// ID, ...) cannot be represented here as-is — generalizing this to a
// platform-agnostic string identity (and the DB columns/model fields that
// currently hard-code "Telegram", e.g. WorkflowInstance.
// RequesterTelegramUserID) is deliberately left for whenever a second real
// platform is actually being built, not solved speculatively now.
type ChatUser struct {
	PlatformUserID int64
	Username       string
	FirstName      string
	LastName       string
}

// ActionOutcome is what HandleIncomingAction decides should happen next in
// response to a button/action tap — the driver renders it with whatever
// platform-native UI it has (e.g. Telegram's ForceReply for PromptForNote,
// a CallbackResponse toast for Reply).
type ActionOutcome struct {
	Reply string
	// ShowAlert asks the driver to surface Reply prominently (a modal/alert)
	// rather than a passing toast — set for failure/unauthorized outcomes,
	// unset for the routine success toasts.
	ShowAlert bool
	// PromptForNote means the transition declared RequireNote and had none
	// yet: the driver should switch into "collect the user's next message
	// as the note" mode (e.g. Telegram ForceReply) and show NotePrompt.
	// The engine has already recorded the pending reply (via
	// PendingReplyStore) by the time this is returned.
	PromptForNote bool
	NotePrompt    string
}
