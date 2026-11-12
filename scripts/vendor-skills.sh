#!/usr/bin/env bash
# Syncs the skills tree and the orq-trace plugin from orq-ai/assistant-plugins
# into the CLI for embedding. The result is committed so builds are hermetic.
# Callers: .github/workflows/vendor-skills.yml opens a PR when assistant-plugins
# releases, and CI re-runs it at the committed ref to prove the tree is
# reproducible. Needs git and jq.
set -euo pipefail

REPO="${ORQ_SKILLS_REPO:-https://github.com/orq-ai/assistant-plugins.git}"
REF="${1:?usage: vendor-skills.sh <git-ref>}"
DEST="cli/custom/skills/assets"
TRACE_DEST="cli/custom/launch/assets/orq-trace"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git clone --quiet --depth 1 "$REPO" "$tmp/src"
git -C "$tmp/src" fetch --quiet --depth 1 origin "$REF"
git -C "$tmp/src" checkout --quiet FETCH_HEAD
resolved="$(git -C "$tmp/src" rev-parse HEAD)"

# Both trees are built and checked under $tmp, then swapped in at the end, so a
# failed run leaves the committed trees untouched.
skills="$tmp/out/skills"
trace="$tmp/out/orq-trace"
mkdir -p "$skills" "$trace/.claude-plugin"
cp -R "$tmp/src/skills/." "$skills/"

# Only what the plugin runs: hooks and sources here, the manifest below.
# `orq launch claude --trace` loads it with --plugin-dir for one session, so
# nothing is installed.
for part in hooks src package.json; do
  cp -R "$tmp/src/plugins/trace-hooks/$part" "$trace/"
done
# Since assistant-plugins 3.9.2 the manifest lives only in orq-trace's
# marketplace entry (strict: false). --plugin-dir still needs the file.
jq -e '[.plugins[] | select(.name == "orq-trace")]
  | if length == 1 and (.[0].hooks | type) == "object"
    then .[0] | del(.source, .category, .strict)
    else error("expected one orq-trace entry with hooks, found \(length)") end' \
  "$tmp/src/.claude-plugin/marketplace.json" > "$trace/.claude-plugin/plugin.json"

cat > "$skills/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved"}
JSON
cat > "$trace/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved", "path": "plugins/trace-hooks"}
JSON

rm -rf "$DEST" "$TRACE_DEST"
mkdir -p "$(dirname "$DEST")" "$(dirname "$TRACE_DEST")"
mv "$skills" "$DEST"
mv "$trace" "$TRACE_DEST"

# The binary embeds the working tree, but git commits only what it does not
# ignore. A vendored file matching .gitignore would ship without being on main.
ignored="$(git ls-files --others --ignored --exclude-standard -- "$DEST" "$TRACE_DEST")"
if [ -n "$ignored" ]; then
  printf 'error: vendored files are git-ignored, so they would not be committed:\n%s\n' "$ignored" >&2
  exit 1
fi

echo "vendored the orq-trace plugin"
echo "vendored $(find "$DEST" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ') skills from $resolved"
