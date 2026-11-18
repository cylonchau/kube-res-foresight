package timing

// ConversationRetentionDays is how long a conversation of the AI assistant is kept after its last message. The conversations are stored so a
// user can come back to them; they are not an archive, and the hourly clean-up deletes the ones that have been quiet for longer than this.
var ConversationRetentionDays = Param{Key: "assistant.conversation_retention_days", Default: 90, Min: 1, Max: 3650,
	Name:   "AI Conversation Retention (days)",
	Remark: "How many days a conversation of the AI assistant is kept after its last message. The hourly clean-up deletes the conversations that have been quiet for longer, with their messages. Only the user who held a conversation can read it; an administrator sees no conversation text (1-3650)."}
