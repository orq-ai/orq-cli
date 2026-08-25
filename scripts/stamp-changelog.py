#!/usr/bin/env python3
"""Rename CHANGELOG.md's Unreleased section to a version, print it as notes.

Called by the release pipeline: `stamp-changelog.py <version> <date>` rewrites
CHANGELOG.md in place and writes the stamped section's body to stdout, which
becomes the GitHub release notes. An empty Unreleased section is left alone and
prints nothing - a release with no hand-written entries falls back to the
generated commit list rather than getting an empty heading in the file forever.
"""

import re
import sys
from pathlib import Path

CHANGELOG = Path(__file__).resolve().parent.parent / "CHANGELOG.md"
UNRELEASED = "## Unreleased"


def stamp(text: str, version: str, date: str) -> tuple[str, str]:
    """Return (new changelog, section body). Body is '' when nothing changed."""
    start = text.find(UNRELEASED + "\n")
    if start == -1:
        raise SystemExit(f"error: no '{UNRELEASED}' heading in CHANGELOG.md")
    body_start = start + len(UNRELEASED) + 1
    next_heading = re.search(r"^## ", text[body_start:], re.MULTILINE)
    body_end = body_start + (next_heading.start() if next_heading else len(text) - body_start)
    body = text[body_start:body_end].strip("\n")
    if not body.strip():
        return text, ""
    stamped = f"{UNRELEASED}\n\n## {version} — {date}\n"
    return text[:start] + stamped + text[body_start:], body


def self_test() -> None:
    before = "# Changelog\n\n## Unreleased\n\n- **Added:** a thing.\n\n## Earlier\n\nold\n"
    after, body = stamp(before, "5.1.0", "2026-01-02")
    assert body == "- **Added:** a thing.", body
    assert "## Unreleased\n\n## 5.1.0 — 2026-01-02\n\n- **Added:** a thing." in after, after
    assert after.endswith("## Earlier\n\nold\n"), after
    # Stamping twice in a row must not invent an empty version section.
    again, body2 = stamp(after, "5.1.1", "2026-01-03")
    assert body2 == "", body2
    assert again == after
    # A section that runs to the end of the file has no following heading.
    tail = "# Changelog\n\n## Unreleased\n\n- **Fixed:** the end.\n"
    _, body3 = stamp(tail, "5.1.0", "2026-01-02")
    assert body3 == "- **Fixed:** the end.", body3
    print("ok")


def main() -> None:
    if sys.argv[1:] == ["--self-test"]:
        return self_test()
    if len(sys.argv) != 3:
        raise SystemExit("usage: stamp-changelog.py <version> <date> | --self-test")
    text = CHANGELOG.read_text()
    new, body = stamp(text, sys.argv[1], sys.argv[2])
    if body:
        CHANGELOG.write_text(new)
    sys.stdout.write(body + ("\n" if body else ""))


if __name__ == "__main__":
    main()
