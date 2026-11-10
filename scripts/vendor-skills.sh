#!/usr/bin/env bash
# Syncs the skills tree and the orq-trace plugin from orq-ai/assistant-plugins
# into the CLI for embedding.
# Run at release time; the result is committed so builds are hermetic.
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

rm -rf "$DEST"
mkdir -p "$DEST"
cp -R "$tmp/src/skills/." "$DEST/"

# Only what the plugin runs: hooks, sources and manifests. `orq launch claude
# --trace` loads it with --plugin-dir for one session, so nothing is installed.
rm -rf "$TRACE_DEST"
mkdir -p "$TRACE_DEST"
for part in hooks src package.json; do
  cp -R "$tmp/src/plugins/trace-hooks/$part" "$TRACE_DEST/"
done
# assistant-plugins 4.0.0 dropped the nested manifest: orq-trace's marketplace
# entry carries it inline (strict: false). --plugin-dir still needs the file, so
# rebuild it from that entry when the nested one is gone.
mkdir -p "$TRACE_DEST/.claude-plugin"
if [ -f "$tmp/src/plugins/trace-hooks/.claude-plugin/plugin.json" ]; then
  cp "$tmp/src/plugins/trace-hooks/.claude-plugin/plugin.json" "$TRACE_DEST/.claude-plugin/"
else
  jq '.plugins[] | select(.name == "orq-trace") | del(.source, .category, .strict)' \
    "$tmp/src/.claude-plugin/marketplace.json" > "$TRACE_DEST/.claude-plugin/plugin.json"
  jq -e '.name == "orq-trace" and .hooks' "$TRACE_DEST/.claude-plugin/plugin.json" > /dev/null \
    || { echo "error: no orq-trace entry with hooks in marketplace.json at $REF" >&2; exit 1; }
fi

resolved="$(git -C "$tmp/src" rev-parse HEAD)"
cat > "$DEST/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved"}
JSON
cat > "$TRACE_DEST/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved", "path": "plugins/trace-hooks"}
JSON

echo "vendored the orq-trace plugin"
echo "vendored $(find "$DEST" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ') skills from $resolved"
