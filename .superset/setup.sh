#!/usr/bin/env bash
# Superset workspace setup: make a fresh worktree usable without manual steps.
# Idempotent: safe to re-run. No .env here — the CLI reads ~/.orq profiles,
# which are shared across worktrees via $HOME.
set -euo pipefail

MAIN="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
if [ "$MAIN" = "$PWD" ]; then
  echo "setup: running in the main checkout, nothing to do"
  exit 0
fi

# Dependencies and a built binary. The Go module cache is global, so download is
# a no-op once warm; `make build` produces ./bin/orq for the workspace to run.
go mod download
make build

echo "setup: done"
