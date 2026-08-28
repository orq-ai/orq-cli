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
RELEASE_URL = "https://github.com/orq-ai/orq-cli/releases/tag/v{}"


def next_heading(body: str) -> int | None:
    """Offset of the next `## ` line, ignoring the ones inside fenced blocks."""
    fenced = False
    offset = 0
    for line in body.splitlines(keepends=True):
        # A fence inside a list item is indented; a heading never is.
        if line.lstrip().startswith("```"):
            fenced = not fenced
        elif not fenced and line.startswith("## "):
            return offset
        offset += len(line)
    return None


def stamp(text: str, version: str, date: str) -> tuple[str, str]:
    """Return (new changelog, section body). Body is '' when nothing changed."""
    unreleased = re.search(rf"^{re.escape(UNRELEASED)}\n", text, re.MULTILINE)
    if unreleased is None:
        raise SystemExit(f"error: no '{UNRELEASED}' heading in CHANGELOG.md")
    start = unreleased.start()
    body_start = unreleased.end()
    end = next_heading(text[body_start:])
    body_end = body_start + (end if end is not None else len(text) - body_start)
    body = text[body_start:body_end].strip("\n")
    if not body.strip():
        return text, ""
    heading = f"[{version}]({RELEASE_URL.format(version)})"
    stamped = f"{UNRELEASED}\n\n## {heading} — {date}\n"
    return text[:start] + stamped + text[body_start:], body


def self_test() -> None:
    before = "# Changelog\n\n## Unreleased\n\n- **Added:** a thing.\n\n## Earlier\n\nold\n"
    after, body = stamp(before, "5.1.0", "2026-01-02")
    assert body == "- **Added:** a thing.", body
    link = "## [5.1.0](https://github.com/orq-ai/orq-cli/releases/tag/v5.1.0) — 2026-01-02"
    assert f"## Unreleased\n\n{link}\n\n- **Added:** a thing." in after, after
    assert after.endswith("## Earlier\n\nold\n"), after
    # Stamping twice in a row must not invent an empty version section.
    again, body2 = stamp(after, "5.1.1", "2026-01-03")
    assert body2 == "", body2
    assert again == after
    # A section that runs to the end of the file has no following heading.
    tail = "# Changelog\n\n## Unreleased\n\n- **Fixed:** the end.\n"
    _, body3 = stamp(tail, "5.1.0", "2026-01-02")
    assert body3 == "- **Fixed:** the end.", body3
    # Prose naming the heading is not the heading: this file's own Versioning
    # section quotes `## Unreleased`, and stamping that would publish the docs.
    decoy = "# Changelog\n\nAdd entries under ## Unreleased\n\n## Unreleased\n\n- **Added:** real.\n"
    _, body4 = stamp(decoy, "5.1.0", "2026-01-02")
    assert body4 == "- **Added:** real.", body4
    # A fenced block may hold a `## ` line; it does not end the section. The
    # `## ` sits at column 0 so only the fence tracking can keep it out of the
    # body, and the fence is indented so only lstrip() can see it.
    fenced = "## Unreleased\n\n- **Added:** a thing:\n\n  ```md\n## Not a heading\n  ```\n\n## Earlier\n"
    _, body5 = stamp(fenced, "5.1.0", "2026-01-02")
    assert body5.endswith("```"), body5
    assert "## Not a heading" in body5, body5
    try:
        stamp("# Changelog\n\n## Earlier\n", "5.1.0", "2026-01-02")
    except SystemExit:
        pass
    else:
        raise AssertionError("a changelog with no Unreleased heading must fail loudly")
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
