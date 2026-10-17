#!/bin/sh
# The only way the agent in schema-drift.yml can publish: commits nothing
# outside cli/custom and CHANGELOG.md, then opens the PR. git and gh run on the
# Claude GitHub App token claude-code-action set up, so the PR triggers CI like
# a human's would.
set -eu

body=.schema-drift-pr.md
title="fix: follow openapi schema changes in hand-written commands"
[ -s "$body" ] || { echo "write $body first" >&2; exit 1; }

git add -- cli/custom CHANGELOG.md
if git diff --cached --quiet; then
  echo "nothing to commit under cli/custom or CHANGELOG.md"
  exit 0
fi

branch="schema-drift/$(git rev-parse --short HEAD)"
git switch -c "$branch"
git commit -m "$title"
git push origin "$branch"
gh pr create --base main --head "$branch" --title "$title" --body-file "$body"
