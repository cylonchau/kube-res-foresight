---
name: quant-system-development
description: Development rules for the existing generic automation platform; the historical skill identifier is retained for repository instruction compatibility.
---

# Platform development

The owner set the product scope on 2026-10-23 to exactly four modules:
Workflow Engine, System Management, Authorization Management and Task Center.
Do not restore retired business modules. Telegram adapters are internal workflow
delivery infrastructure and have no separate management menu or standalone CLI
commands. Do not add Telegram agent entrypoints under cmd or pkg/cmd.

Before changing code, inspect the repository, locate the nearest existing
implementation, read the matching reference and follow its architecture.
Keep the existing Go/Gin/GORM/Cobra backend and Vue/Naive UI admin conventions.
Reuse shared components, helpers, request wrappers, middleware and persistence.
Do not independently redesign pages or create parallel infrastructure.

Read references/frontend.md for pages; references/bankend.md for storage, jobs and
delivery; references/telegram.md for internal Telegram adapters; and
references/SOFTWARE_ARCHITECTURE.md plus SOFTWARE_FUNCTION.md for module ownership.

For external integrations, check official SDKs and mature maintained libraries
first. Prefer existing dependencies, use adapters and do not reimplement SDK
authentication, protocols or connection management. Verify external API behavior
with an actual call before designing against it. State only verified conclusions.

Keep frontend and backend changes together. Test the affected behavior and run
appropriate build/type checks. Test schema cleanup on scratch data and preserve
user accounts, generic tasks, custom permissions and audit data. Normal startup
must not perform destructive migrations.

The canonical skill lives in .claude. Run scripts/sync-skill.sh after editing it;
--check verifies the .agents and .codex mirrors. Update current architecture and
progress documentation when product scope changes.
