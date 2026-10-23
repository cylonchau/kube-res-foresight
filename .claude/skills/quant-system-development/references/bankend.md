# Backend

Follow command -> router -> handler -> DAO -> model -> store. Reuse interfaces and
implementations for scheduler tasks and notification delivery. Keep database jobs,
claims, heartbeat, idempotency, retries and context cancellation. Shared helpers
belong in shared files rather than concrete business implementations.
Use maintained SDKs and verify actual external behavior before integrating.
Changes to registered models and menus require explicit, idempotent migrations.
Backups and scratch database tests precede testing destructive upgrades. Preserve
generic custom data. Build binaries under target/bin and run relevant Go tests.
