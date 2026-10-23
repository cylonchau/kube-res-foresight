package model

import "context"

type botScopeKey struct{}

// WithBot scopes command matching to one bot: only commands bound to that bot
// (or to every bot) are considered. Without it all commands match.
func WithBot(ctx context.Context, botID int64) context.Context {
	return context.WithValue(ctx, botScopeKey{}, botID)
}

// BotFrom returns the bot a context is scoped to.
func BotFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(botScopeKey{}).(int64)
	return id, ok
}
