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
- Added: `orq setup` — signs you in, creates (or reuses) a workspace API key,
  and registers the orq MCP server and gateway provider in the config files of
  the coding agents it detects. Unlike `orq launch`, these writes are
  persistent. It does not ask about projects: API keys are workspace-scoped.
  `pi` is wired gateway only — an `orq` provider merged into
  `~/.pi/agent/models.json`, reached with `pi --model orq/<model>` — because pi
  has no native MCP.
- Changed: the API key `orq setup` saves is reused only for the workspace it
  was minted in. Running setup against a different workspace (`--workspace`, or
  the interactive picker) mints a fresh key for it instead of silently wiring
  every agent config to the old one; `orq setup coding-agents` refuses in that
  situation, since it never creates keys. Keys saved by earlier builds carry no
  workspace record and are reused as before.
- Removed: `orq setup` no longer writes `ORQ_API_KEY` into `./.env`, and the
  `--no-env` flag is gone with it. The key lives in `~/.orq/credentials.json`
  for the CLI and `~/.orq/env` for your agents: configs reference the
  `ORQ_API_KEY` environment variable, which that file exports (kimi is the
  exception — its config carries the key literally). An existing `./.env` keeps working — and keeps taking
  precedence over your login, including after `orq auth logout`, which is the
  behaviour that made writing one a poor default.
  **--json field change:** `orq setup` no longer emits `api_key.env_file`.
- Changed: `orq setup` no longer sends a model call. It used to spend one billed
  completion to prove the gateway answers, which also opened a trace in your
  workspace, for a request you did not make, often as the first thing a new
  account ever saw in Traces. The default model is chosen from the catalogue
  ranking instead, and the first real agent request is the test. `orq doctor`
  reports gateway funding for free.
  **Removed with it:** the `--no-verify` flag, which existed only to decline
  that call.
- Changed: `orq setup` asks about credits only when it is wiring the gateway,
  and only on a deployment with a known dashboard. Choosing "MCP tools only",
  passing `--no-coding-agents`, or running against a self-hosted install now
  says nothing about credits. The balance is irrelevant to all three, and the
  gateway does not meter on-premise deployments at all. The wording states the
  rule rather than asserting a state the CLI cannot read: a zero balance still
  serves calls through a BYOK provider key, a private model, or a subscription
  that has not disabled shared-key use.
- Added: `orq doctor` reports coding-agent wiring per detected agent, and warns
  when an agent is wired but `ORQ_API_KEY` is absent from the current shell —
  the state every agent launched from a terminal older than your last `orq
  setup` is in, and one that looks like a broken install from inside the agent.
  It also reports gateway funding when a login session is present. Detected but
  unwired is a warning, never a failure, so the exit code stays 0.
  **--json field change:** `checks[]` gains `coding_agent_<id>` entries and,
  with a session, `gateway_funding`.
- **--json field change:** `orq setup` gains `gateway_funded`, a string of
  `funded`, `unfunded` or `unknown`. Not a boolean: "nobody asked" is a normal
  outcome now that the balance is only read when the gateway is wired, and a
  script testing truthiness would read it as "cannot pay". `models_enabled` is
  omitted entirely on runs that never listed the catalogue, rather than
  reported as zero.
- Fixed: `orq setup` no longer ends in an unexplained gateway error on a
  workspace that cannot serve model calls. It says so once, with the links that
  fix it, and still wires everything that works without funding, exiting 0
  because nothing is broken. The "check again" retry prompt is gone with it.
- Fixed: the "connect a provider" link pointed at a page that does not exist.
  Dashboard links are now workspace-scoped.
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
- Changed: `orq setup` makes **no** billed completions. It briefly made five,
  then one; it now makes none at all, for the reasons in the entry above. Model
  selection and gateway verification used to probe separately, and probing
  existed to compensate for selecting from the whole catalogue rather than the
  workspace's enabled set, which is no longer how the default is chosen.
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
