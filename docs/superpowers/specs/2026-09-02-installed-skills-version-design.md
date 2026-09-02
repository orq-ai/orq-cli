# Installed Skills Version Reporting

## Goal

Show the version of the locally installed orq skills bundle in `orq doctor`
and `orq connect --status`, without changing installation or refresh behavior.

## Version identity

The displayed version is the full `assistant-plugins` source commit recorded in
the installed snapshot's `SOURCE.json`. Human output abbreviates it to the first
seven characters; structured output keeps the full value.

If an older or damaged snapshot has no readable source commit, the installed
content fingerprint already recorded in `~/.orq/materialized-skills.json` is
used instead. A machine with no permanent skills installation continues to omit
the skills check and version.

This identity is preferable to the CLI version because a skills bundle can stay
unchanged across CLI releases. It is preferable to exposing only the content
fingerprint because the source commit is recognizable in the repository where
the bundle is maintained. The fingerprint remains the reliable compatibility
fallback.

## Data flow

`skills.ReadStatus` derives the installed bundle version from the manifest's
generation directory and exposes it on `skills.Status`. Commands do not parse
the manifest or `SOURCE.json` themselves.

The version read is diagnostic metadata. Failure to read `SOURCE.json` does not
make an otherwise healthy installation fail; reporting falls back to the
fingerprint.

## Command output

The `skills` check in `orq doctor` always includes `version` in `details` when a
permanent installation exists. Every human skills-check message also includes
the abbreviated installed version, regardless of whether the installation is
healthy, missing links, foreign links, or stale.

`orq connect --status` prints one `skills version <short-version>` line whenever
the requested capability set includes skills and the selected agents have at
least one recorded permanent skills link in the current scope. Agent-only or
gateway-only status requests do not show unrelated skills metadata.

No new command or flag is added, so the command surface is unchanged.

## Compatibility and errors

The manifest schema remains version 1 because no persisted field is added.
Existing installs obtain their version from the snapshot they already point to.
If that metadata is missing, the existing fingerprint guarantees a non-empty
version. An unreadable manifest retains the existing doctor failure and cannot
claim an installed version.

## Tests

- Unit-test source-commit parsing and fingerprint fallback in `cli/custom/skills`.
- Assert the doctor check includes the full version in structured details and
  the abbreviated version in its human message.
- Assert `connect --status` shows the abbreviated version for skills requests
  and omits it when skills are outside the requested capability set.
- Run the custom CLI test suite, formatting checks, and the surface gate.
