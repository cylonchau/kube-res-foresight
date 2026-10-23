# Repository development instructions

Use the `quant-system-development` skill for development in this repository.
Its historical name is retained; the product is now a generic automation platform.

Only Workflow Engine, System Management, Authorization Management and Task Center
are supported. Inspect the existing implementation and matching skill references
before editing. Follow existing architecture and shared UI conventions. Prefer
official SDKs and maintained libraries for integrations, and verify real API behavior.

Edit the canonical skill in `.claude/skills/quant-system-development`, then run
`scripts/sync-skill.sh` to update `.agents` and `.codex`; `--check` verifies mirrors.
