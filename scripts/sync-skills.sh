#!/usr/bin/env bash
# Two-way sync of project skills between Codex (.agents/skills) and Claude (.claude/skills).
# Run it after editing a skill with either tool; the most recently modified copy of each file wins.
#
# Usage: scripts/sync-skills.sh [--check | --quiet | --mirror agents | --mirror claude]
#   (default)        copy every file that is missing or older on one side from the newer side
#   --check          report differences only, exit 1 if the two trees differ
#   --quiet          like default but print only changes (for hooks)
#   --mirror <side>  one-way copy from <side> and delete files the other side has extra (use to propagate deletions)
set -euo pipefail
cd "$(dirname "$0")/.."
A=".agents/skills"
C=".claude/skills"
mode="sync"; quiet=0; from=""
case "${1:-}" in
  --check) mode="check" ;;
  --quiet) quiet=1 ;;
  --mirror) mode="mirror"; from="${2:-}"; [ "$from" = agents ] || [ "$from" = claude ] || { echo "usage: --mirror agents|claude" >&2; exit 2; } ;;
  "") ;;
  *) echo "unknown option: $1" >&2; exit 2 ;;
esac
mkdir -p "$A" "$C"
say() { [ "$quiet" = 1 ] || echo "$@"; }
same() { diff -q --strip-trailing-cr "$1" "$2" >/dev/null 2>&1; }
status=0

if [ "$mode" = mirror ]; then
  if [ "$from" = agents ]; then src="$A"; dst="$C"; else src="$C"; dst="$A"; fi
  rm -rf "$dst"; mkdir -p "$dst"; cp -rp "$src/." "$dst/"
  echo "mirrored $src -> $dst"; exit 0
fi

files=$( { (cd "$A" && find . -type f | sed "s|^./||"); (cd "$C" && find . -type f | sed "s|^./||"); } | sort -u )
while IFS= read -r f; do
  [ -n "$f" ] || continue
  a="$A/$f"; c="$C/$f"
  if [ -f "$a" ] && [ -f "$c" ]; then
    same "$a" "$c" && continue
    if [ "$a" -nt "$c" ]; then s="$a"; d="$c"; elif [ "$c" -nt "$a" ]; then s="$c"; d="$a"; else
      echo "CONFLICT (same mtime, different content): $f"; status=1; continue; fi
  elif [ -f "$a" ]; then s="$a"; d="$c"
  else s="$c"; d="$a"; fi
  if [ "$mode" = check ]; then echo "DIFFERS: $f ($s is newer or only copy)"; status=1; continue; fi
  mkdir -p "$(dirname "$d")"; cp -p "$s" "$d"
  echo "synced: $s -> $d"
done <<< "$files"
[ "$mode" = check ] && [ "$status" = 0 ] && echo "in sync"
[ "$mode" = sync ] && [ "$quiet" = 0 ] && [ "$status" = 0 ] && echo "done"
exit $status
