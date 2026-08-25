#!/usr/bin/env bash
# Syncs the skills tree from orq-ai/assistant-plugins into the CLI for embedding.
# Run at release time; the result is committed so builds are hermetic.
set -euo pipefail

REPO="${ORQ_SKILLS_REPO:-https://github.com/orq-ai/assistant-plugins.git}"
REF="${1:?usage: vendor-skills.sh <git-ref>}"
DEST="cli/custom/skills/assets"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git clone --quiet --depth 1 "$REPO" "$tmp/src"
git -C "$tmp/src" fetch --quiet --depth 1 origin "$REF"
git -C "$tmp/src" checkout --quiet FETCH_HEAD

rm -rf "$DEST"
mkdir -p "$DEST"
cp -R "$tmp/src/skills/." "$DEST/"

resolved="$(git -C "$tmp/src" rev-parse HEAD)"
cat > "$DEST/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved"}
JSON

echo "vendored $(find "$DEST" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ') skills from $resolved"
