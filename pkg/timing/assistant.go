package timing

// ConversationRetentionDays is how long a conversation of the AI assistant is kept after its last message. The conversations are stored so a
// user can come back to them; they are not an archive, and the hourly clean-up deletes the ones that have been quiet for longer than this.
var ConversationRetentionDays = Param{Key: "assistant.conversation_retention_days", Default: 90, Min: 1, Max: 3650,
	Name:   "AI Conversation Retention (days)",
	Remark: "How many days a conversation of the AI assistant is kept after its last message. The hourly clean-up deletes the conversations that have been quiet for longer, with their messages. Only the user who held a conversation can read it; an administrator sees no conversation text (1-3650)."}

// AuditRetentionDays is how long the audit records of the AI assistant are kept.
var AuditRetentionDays = Param{Key: "assistant.audit_retention_days", Default: 15, Min: 1, Max: 3650,
	Name:   "AI Audit Retention (days)",
	Remark: "How many days the audit records of the AI assistant are kept: who asked, from which page, which model and mode, which tools were read or refused, how long it took and the tokens it used. A record holds no question, answer or tool result. The hourly clean-up deletes the older ones (1-3650)."}
