# Changelog

All notable, user-visible changes to the `orq` CLI belong here: new commands,
removed or renamed commands and flags, output-format changes, and behavior
changes scripts could observe. Internal refactors do not.

## Stability contract

What you may depend on, and what you may not:

- **`--json` output on stdout is the machine contract.** Field names and
  structure follow the orq API response for the endpoint behind the command.
  Scripts should parse this and nothing else. Caveat on what CI enforces: the
  `surface.json` gate below covers command paths and flags only, not response
  field shapes. A renamed or dropped API response field flows through
  regeneration into `--json` with nothing in CI failing, so response
  field-shape changes are announced here by hand, not caught automatically.
  Fingerprinting response types per command so the gate covers them too is
  tracked in RES-1133.
- **TOON (the default terminal format) is presentation-only.** It exists for
  human readability and may change rendering between releases without notice.
  The `toon-go` dependency is pinned in `go.mod` and a golden-output test
  (`cli/custom/toon_golden_test.go`) makes any rendering change a deliberate,
  reviewed event rather than a silent one, but it is still not a contract.
- **Exit codes:** `0` success, `1` any failure, `130` interrupted (SIGINT),
  `143` terminated (SIGTERM).
- **Errors go to stderr; results go to stdout.**
- **The command surface is tracked in `surface.json`.** CI fails any change to
  commands or flags that is not consciously committed, so the surface cannot
  drift silently under an OpenAPI regeneration. Removing or renaming a
  command or flag is a breaking change: announce it here at least one release
  before it disappears.

## Versioning

The CLI version tracks the orq API line it was generated against (see
`app_version` in `.bartolo.json`, stamped by the release pipeline). This means
a CLI minor bump can carry surface changes originating in the API. The
`surface.json` gate plus this file are the compensating controls: any surface
change is visible in review and recorded here. Decoupling the CLI onto its own
semver is an open team decision (RES-1133); until then, treat the API line as
the version and this changelog as the source of truth for breaking changes.

## Unreleased

- Added: `surface.json` command-surface manifest and CI gate; changes to the
  command tree now fail CI until the manifest is regenerated and reviewed
  (`go run ./cmd/surface-dump -write`).
- Added: CI workflow running build, vet, gofmt, tests, and the surface gate on
  every PR for both modules.
- Added: this changelog and the stability contract above.

## Earlier

Releases before this file existed are documented by their GitHub Releases and
tags only.
