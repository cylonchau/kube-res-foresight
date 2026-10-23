#!/usr/bin/env bash
# Mirrors the skill (source of truth: .claude) to the Codex copies so every agent follows the same rules.
# Usage: scripts/sync-skill.sh [--check]   (--check only reports differences)
set -euo pipefail
cd "$(dirname "$0")/.."
src=".claude/skills/quant-system-development"
status=0
for dst in ".agents/skills/quant-system-development" ".codex/skills/quant-system-development"; do
  if [ "${1:-}" = "--check" ]; then
    if diff -rq --strip-trailing-cr "$src" "$dst" >/dev/null 2>&1; then
      echo "in sync: $dst"
    else
      echo "DIFFERS: $dst"; diff -rq --strip-trailing-cr "$src" "$dst" || true; status=1
    fi
    continue
  fi
  mkdir -p "$dst"
  # a mirror: files that no longer exist in the source are removed from the copy
  rm -rf "$dst/references"
  mkdir -p "$dst/references" "$dst/assets"
  cp "$src/SKILL.md" "$dst/SKILL.md"
  cp -r "$src/references/." "$dst/references/"
  echo "synced: $dst"
done
exit $status
