#!/usr/bin/env python3
"""Find newer gateway models for the `orq launch` default model constants.

Every `Default*Model = "<id>"` literal in cli/custom/launch is a family: the id
with its version number swapped for a wildcard, so anthropic/claude-haiku-4-5
matches anthropic/claude-haiku-5-5 and openai/gpt-5.6-terra matches only other
*-terra ids. A constant moves only to a newer id of the same shape that the
public catalogue serves on every endpoint the current one does. When the
successor is unclear (gpt-6 ships sol/luna/astra but no terra) nothing matches
and a human decides.

Writes the candidates as JSON and a markdown table for the run summary.
"""

import json
import re
import sys
import time
import urllib.request
from pathlib import Path

CATALOGUE_URL = "https://api.orq.ai/v2/model-catalog"
LAUNCH_DIR = Path("cli/custom/launch")
CONSTANT = re.compile(r'^\s*(?:const\s+)?(Default\w*Model)\s*=\s*"([^"]+)"', re.M)
# One or two numeric parts, the second at most two digits, so a dated snapshot
# (claude-opus-4-20250514) never reads as version 4.20250514.
VERSION = r"\d+(?:[.-]\d{1,2})?"
# The catalogue lists ~700 models; far fewer means a broken response, not a
# quiet week.
MIN_CATALOGUE = 100


def family(model_id):
    """Regex matching model_id with its first version number replaced, or None."""
    m = re.search(VERSION, model_id)
    if not m:
        return None
    head, tail = model_id[: m.start()], model_id[m.end() :]
    return re.compile(re.escape(head) + f"({VERSION})" + re.escape(tail))


def version_key(v):
    return tuple(int(p) for p in re.split(r"[.-]", v))


def constants(launch_dir):
    """[(name, file, id)] for every Default*Model string literal."""
    found = []
    for path in sorted(launch_dir.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for name, model_id in CONSTANT.findall(path.read_text()):
            found.append((name, str(path), model_id))
    return found


def candidates(consts, catalogue):
    """Rows of {constant, file, current, newest, bump}; newest is None when nothing matches."""
    endpoints = {m["id"]: set(m.get("endpoints") or []) for m in catalogue}
    rows = []
    for name, path, current in consts:
        pattern = family(current)
        newest = None
        if pattern:
            cur = pattern.fullmatch(current)
            best = version_key(cur.group(1))
            for model_id, eps in endpoints.items():
                m = pattern.fullmatch(model_id)
                if m and version_key(m.group(1)) > best and endpoints.get(current, set()) <= eps:
                    best, newest = version_key(m.group(1)), model_id
        rows.append({"constant": name, "file": path, "current": current,
                     "newest": newest, "served": current in endpoints})
    return rows


def fetch():
    for attempt in range(3):
        try:
            with urllib.request.urlopen(CATALOGUE_URL, timeout=30) as resp:
                data = json.load(resp)["data"]
            return [m for m in data if not m.get("deprecated")]
        except Exception as err:  # noqa: BLE001 - any failure is retried, then raised
            if attempt == 2:
                raise
            print(f"catalogue fetch failed ({err}), retrying", file=sys.stderr)
            time.sleep(10)


def summary(rows):
    lines = ["| constant | current | newer in catalogue |", "|---|---|---|"]
    for r in rows:
        note = f"`{r['newest']}`" if r["newest"] else "none"
        if not r["served"]:
            note += " (current id is not in the catalogue)"
        lines.append(f"| `{r['constant']}` | `{r['current']}` | {note} |")
    return "\n".join(lines) + "\n"


def self_test():
    catalogue = [
        {"id": "anthropic/claude-opus-5", "endpoints": ["chat", "responses"]},
        {"id": "anthropic/claude-opus-5-5", "endpoints": ["chat", "responses"]},
        {"id": "anthropic/claude-opus-4-20250514", "endpoints": ["chat", "responses"]},
        {"id": "anthropic/claude-haiku-4-5", "endpoints": ["chat"]},
        {"id": "anthropic/claude-haiku-5-5", "endpoints": ["chat"]},
        {"id": "anthropic/claude-haiku-4-5-20251001", "endpoints": ["chat"]},
        {"id": "openai/gpt-5.6-terra", "endpoints": ["chat", "responses"]},
        {"id": "openai/gpt-6-sol", "endpoints": ["chat", "responses"]},
        {"id": "openai/eu.gpt-7-terra", "endpoints": ["chat", "responses"]},
        {"id": "moonshotai/kimi-k2.7-code", "endpoints": ["chat", "responses"]},
        {"id": "moonshotai/kimi-k3-code", "endpoints": ["chat"]},
    ]
    consts = [("Opus", "a.go", "anthropic/claude-opus-5"),
              ("Haiku", "a.go", "anthropic/claude-haiku-4-5"),
              ("Codex", "b.go", "openai/gpt-5.6-terra"),
              ("Kimi", "c.go", "moonshotai/kimi-k2.7-code")]
    got = {r["constant"]: r["newest"] for r in candidates(consts, catalogue)}
    assert got == {
        "Opus": "anthropic/claude-opus-5-5",  # not the dated 4-20250514 snapshot
        "Haiku": "anthropic/claude-haiku-5-5",  # not the dated 4-5 snapshot
        "Codex": None,  # gpt-6 has no terra; the eu. region id is not the same shape
        "Kimi": None,  # k3 drops the responses endpoint
    }, got
    print("self-test ok")


def main():
    if sys.argv[1:] == ["--self-test"]:
        return self_test()
    if len(sys.argv) != 3:
        raise SystemExit("usage: model-candidates.py <out.json> <summary.md> | --self-test")
    consts = constants(LAUNCH_DIR)
    if not consts:
        raise SystemExit(f"no Default*Model string constants under {LAUNCH_DIR}")
    catalogue = fetch()
    if len(catalogue) < MIN_CATALOGUE:
        raise SystemExit(f"catalogue returned {len(catalogue)} models, expected at least {MIN_CATALOGUE}")
    rows = candidates(consts, catalogue)
    bumps = [r for r in rows if r["newest"]]
    Path(sys.argv[1]).write_text(json.dumps(bumps, indent=2) + "\n")
    with open(sys.argv[2], "a") as out:
        out.write(summary(rows))
    print(summary(rows))


if __name__ == "__main__":
    main()
