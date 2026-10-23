# Current Architecture

Hermes Platform is a generic automation platform with exactly four visible modules:
Workflow Engine, System Management, Authorization Management and Task Center.

The existing dependency flow is unchanged:

`command -> router -> handler -> DAO -> GORM model -> database`

- `pkg/workflow`: JSON state machines, transitions, action registries and pending replies.
- `pkg/job`: durable jobs, claims, heartbeat, schedules and retries.
- `pkg/app`, `pkg/apis`: HTTP, authentication, RBAC, configuration and audit.
- `pkg/model`, `pkg/dao/rdb`, `pkg/migration`, `pkg/store`: shared persistence.
- `web`: Vue 3 / Naive UI admin, shared list and column settings, dynamic RBAC menus.
- Telegram packages remain internal workflow delivery adapters; no independent admin module is exposed.

The `hermes all` command composes the platform and job workers. Server and job can also run separately.
Migration runs only through explicit migration/upgrade flags. Existing data cleanup is idempotent.
The hidden profile route supports the authenticated user's own profile, password and OTP settings.
