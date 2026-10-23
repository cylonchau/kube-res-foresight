# Internal workflow delivery

Telegram is an internal workflow delivery and optional identity-binding adapter.
It has no independent product menu or standalone command entrypoint. Keep the existing sender, workflow chat
driver and encrypted credentials. Telegram Bot API and MTProto clients use the
existing maintained integrations; do not reimplement their protocols. Preserve
context cancellation, session secrecy, request authentication and idempotency.
