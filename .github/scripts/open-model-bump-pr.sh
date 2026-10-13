#!/bin/sh
# The only way the agent in model-bump.yml can publish: commits nothing outside
# cli/custom/launch, README.md and CHANGELOG.md, then opens the PR. git and gh
# run on the Claude GitHub App token claude-code-action set up, so the PR
# triggers CI like a human's would.
set -eu

body=.model-bump-pr.md
title="fix(launch): bump the coding agents' default models"
[ -s "$body" ] || { echo "write $body first" >&2; exit 1; }

git add -- cli/custom/launch README.md CHANGELOG.md
if git diff --cached --quiet; then
  echo "nothing to commit under cli/custom/launch, README.md or CHANGELOG.md"
  exit 0
fi

branch="model-bump/$(date -u +%Y-%m-%d)"
git switch -c "$branch"
git commit -m "$title"
git push origin "$branch"
gh pr create --base main --head "$branch" --title "$title" --body-file "$body"
