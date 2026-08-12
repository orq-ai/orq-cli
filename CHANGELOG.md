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
  `143` terminated (SIGTERM). One exception: `orq launch` runs another program
  and propagates that program's exit status verbatim, so any value from `2` to
  `255` can come back — `127` when the agent binary does not start, `128+signum`
  when it is killed by a signal (`137` for SIGKILL), and otherwise whatever the
  agent itself returned. Scripts wrapping `orq launch` should treat any non-zero
  code as failure rather than matching on the four above.
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

- Added: `orq launch <agent>` — starts claude, codex, opencode, kilo, kimi or
  pi preconfigured to route every model call through the orq AI Router, with
  `--sandbox` (throwaway Docker container, nothing mounted unless
  `--mount-cwd`), `--dry-run`, `--model`, and the orq MCP server wired in
  automatically (`--no-mcp` to opt out). Per-invocation only: nothing is
  written to your agent's own configuration.
- Added: `orq setup` — signs you in, selects or creates a project, mints an API
  key, and registers the orq MCP server and gateway provider in the config
  files of the coding agents it detects. Unlike `orq launch`, these writes are
  persistent.
- Fixed: `orq launch` sends model calls to the gateway the session authenticated
  against, deriving it from the API base. On an on-prem deployment every agent
  except claude wired its MCP server to the customer's host while sending
  prompts and file contents to the public api.orq.ai, using a key that host had
  issued. Explicit `--base-url`, `ORQ_GATEWAY_URL` and the per-agent env vars
  still take precedence.
- Changed: `orq setup` writes every chat model the workspace has enabled into an
  agent's provider config, instead of four hand-picked families, matching what
  `orq launch` already does. It also fills `default_model` when the config has
  none — never replacing one, since agents persist the user's own pick there.
- Changed: `orq setup` now makes **one** billed completion (proving the default
  model answers) instead of five. Model selection and gateway verification used
  to probe separately, and probing existed to compensate for selecting from the
  whole catalogue rather than the workspace's enabled set.
- Added: `orq setup -y/--yes` — answer yes to every confirmation instead of
  being asked. `orq setup` now asks before registering the orq MCP server in an
  agent's config, since that writes into files you own and grants the agent
  read/write access to your workspace; `--yes` is the auto-approve, `--no-mcp`
  the decline, and `--no-input` still never prompts.
- Added: `orq setup --no-mcp` — instrument agents for gateway routing without
  registering the orq MCP server, for anyone who wants their model calls routed
  through orq but does not want the agent reading and writing their workspace.
  `orq setup -i` asks the same question interactively.
- Added: `orq man-pages` output and a `.sha256` published next to every release
  asset, which `install.sh` now verifies.
- Changed: `orq auth logout` also clears the stored API-key profile, not just
  the session. Previously a "logged out" CLI kept authenticating from
  `credentials.json`. **`--json` field change:** the payload gains
  `api_key_profile_cleared`, and in the no-session case `cleared` is now `true`
  when a stored key was removed (it was always `false`).
- Changed: `orq doctor` reports authentication from `ORQ_API_KEY` or
  `credentials.json` instead of reporting "missing" when only those are present.
- Fixed: API-key profiles written by `orq setup` carry an auth type the CLI can
  resolve. Profiles written by earlier builds were unusable — every generated
  command failed with "no authentication handler configured" — and are now
  repaired on read.

### Known gap in the surface gate

`orq launch` sets `DisableFlagParsing` so that everything after the agent name
reaches the agent untouched. Its flags are therefore parsed by hand and do not
appear in `surface.json`, so the CI gate covers the seven `orq launch` command
paths but **not** their flags: renaming or removing `--sandbox`, `--dry-run`,
`--model`, `--mount-cwd`, `--rebuild`, `--no-mcp`, `--no-skills` or
`--no-fetch-models` will not fail CI. Treat them as covered by the stability
contract anyway and announce changes here by hand, exactly as with `--json`
response field shapes.

- Added: `surface.json` command-surface manifest and CI gate; changes to the
  command tree now fail CI until the manifest is regenerated and reviewed
  (`go run ./cmd/surface-dump -write`).
- Added: CI workflow running build, vet, gofmt, tests, and the surface gate on
  every PR for both modules.
- Added: this changelog and the stability contract above.

## Earlier

Releases before this file existed are documented by their GitHub Releases and
tags only.
