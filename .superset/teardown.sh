#!/usr/bin/env bash
# Superset workspace teardown: archive anything that would be lost, then free disk.
# Runs when the workspace is deleted — the worktree directory disappears right after.
set -euo pipefail

MAIN="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
if [ "$MAIN" = "$PWD" ]; then
  echo "teardown: running in the main checkout, refusing to touch it"
  exit 0
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached)"
ARCHIVE_ROOT="${SUPERSET_ARCHIVE_ROOT:-$HOME/.superset/archive}"
DEST="$ARCHIVE_ROOT/$(basename "$MAIN")/$(basename "$PWD")-$(date +%Y%m%d-%H%M%S)"

# Archive uncommitted work outside the checkout. Committed work survives through
# the shared object store. Do not suppress errors: cleanup must fail closed.
if [ -n "$(git status --porcelain)" ]; then
  mkdir -p "$DEST"
  {
    echo "branch: $BRANCH"
    echo "head:   $(git rev-parse HEAD)"
    echo "path:   $PWD"
    echo "date:   $(date -Iseconds)"
  } >"$DEST/MANIFEST.txt"
  git status --porcelain >"$DEST/status.txt"
  git diff --binary HEAD >"$DEST/uncommitted.patch"
  if [ -n "$(git ls-files --others --exclude-standard)" ]; then
    git ls-files --others --exclude-standard -z |
      tar -czf "$DEST/untracked.tar.gz" --null -T -
  fi
  echo "teardown: archived uncommitted work to $DEST"
fi

# Free disk: the built binary is rebuildable with `make build`.
rm -rf bin
echo "teardown: done"
