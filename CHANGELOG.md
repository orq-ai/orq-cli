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

- **Changed (security):** the key `orq setup` and `orq connect` mint is now
  restricted to gateway permissions instead of every permission in the
  workspace. Keys minted by v4.13.10 carry `permission_mode: "all"`, which
  resolves to the whole capability catalog — including `member`, `billing`,
  `sso`, `group` and `workspace` — and is written in cleartext into coding-agent
  config files. The new key holds the server's router preset — the gateway
  domains plus read-only access to the model list. The exact set is chosen
  server-side by `source: "router"`, not by the CLI, so the dashboard's
  permission list is the source of truth. A call to any platform endpoint is
  refused with `insufficient_scope` (verified against a live key). **Existing keys are not
  changed, and re-running `orq setup` will not replace one** — it reuses whatever
  is saved. To move to a gateway-scoped key today: `orq auth logout`, log in
  again, then `orq setup`. Revoke the old key in the dashboard. A guided path for
  this is not built yet.
- **Changed:** minted keys are now named `orq-cli gateway <hostname>` rather than
  `orq-cli <hostname>`. The dashboard lists every restricted key as `Restricted`
  without saying restricted to what, so the name is the only place the purpose
  shows — and it leaves room for the MCP key that will sit beside it. Existing
  keys keep their old name until they are renewed.
- **Changed:** the minted key is stored as `gateway_key` in `credentials.json`,
  not `api_key`. A stored `api_key` takes precedence over your login for every
  generated command, so a gateway-scoped key there would have started refusing
  `orq prompts list` and friends. Commands now use your login session, and only
  agent configs get the gateway key. Keys you bring yourself via
  `orq login`/`--api-key` still go to `api_key` and still take precedence.
- **Changed:** minted keys now expire after 90 days instead of never. `orq setup`
  replaces one with under 30 days left; run `orq connect` afterwards to rewrite
  the agent configs. The superseded key is **not** revoked
  — it stays valid until its own expiry, so the cutover overlaps and an agent
  config that has not been rewritten yet keeps working. Keys minted before this
  release record no expiry and are never renewed automatically, and no command
  replaces them in place — see the note above.
- **Fixed:** `orq launch` no longer warns about a workspace mismatch on every
  run. Two causes, both introduced by the `gateway_key` split: the check read
  `api_key`, which is now empty, and the session token the CLI injects into
  `ORQ_API_KEY` was being compared against the saved key as though the user had
  exported it. The credential lookup now lives in one place (`auth.SavedAgentKey`)
  that `launch`, `setup` and `doctor` share.
- **Fixed:** `orq setup --api-key <key>` is no longer silently overridden. A
  previously minted `gateway_key` outranks `api_key`, so the supplied key was
  used for that run and then ignored by the next `orq connect`. Saving an
  explicit key now clears the minted one.
- **Fixed:** `orq disconnect tracing` reported "nothing wired" on a wired
  machine; it now says tracing is not available yet, like `orq connect` does.
- **Fixed:** `orq connect --status <agent>` no longer reports agents you did not
  ask about, and `orq connect --help` no longer claims it never creates keys —
  an interactive run with no credential offers to mint one.
- **Fixed:** `orq setup --json` names its list `coding_agents`, matching
  `connect` and `disconnect`.
- **Fixed:** `orq auth logout` now clears the exported key from `~/.orq/env`
  (and `env.fish`). That file is written by `orq setup`, and a shell profile
  that sources it kept exporting a live key into every new shell after logout —
  so you were signed out while still authenticated everywhere. The file is
  emptied rather than deleted, because a profile carrying `. ~/.orq/env` would
  then error on every new shell. `--json` gains `env_files_cleared`.
- **Changed:** `orq connect --json` and `orq disconnect --json` name their list
  `coding_agents`, not `agents`. `agents` is orq's own entity — the Agents you
  build and invoke — so the old key made the output read like a listing of
  those. `disconnect`'s `removed` is now a list of capability names rather than
  a `+`-joined string, and the payload carries an `api_key` object naming the
  key that survived. Neither command has shipped, so nothing depends on the old
  shape.
- Added: `orq disconnect` now says that the API key survives — it removes the
  wiring, not the credential — and prints the `orq api-keys delete <id>` command
  that would revoke it. Removing the config used to read as "orq is off this
  machine" while the key stayed valid, saved, and exported.
- **Removed:** the gateway credit check, in `orq doctor` and in `orq setup`. It
  reported a zero balance as a problem, but a zero balance blocks nothing on its
  own: the gateway still serves calls when a provider key (BYOK) is connected,
  when the model is private, when the workspace carries the recently-created
  flag, or when the subscription has not disabled shared-key use — **which is
  the default**. None of those four are readable from the CLI, so the warning
  fired more often on working workspaces than broken ones. A workspace that
  genuinely cannot serve a call says so at the point of use, with a request id
  and a documentation link. **`gateway_funded` is gone from `orq setup --json`**,
  and the `gateway_funding` check is gone from `orq doctor --json`.
- Added: `orq doctor` counts down to the gateway key's expiry, warns under 30
  days, and fails once it has lapsed. Also warns when `ORQ_API_KEY` in your shell
  is the gateway-scoped key, which would make platform commands fail in a shell
  where your login would have worked.
- Added: after minting, `orq setup` prints a ready-to-run `orq budgets create`
  command scoped to the new key. It is a suggestion, never applied: a silent
  spend ceiling would stop an agent mid-task, and budgets are workspace-scoped,
  so non-admin members cannot create one anyway.
- Added: `orq connect` now says when an agent's config holds the key itself
  rather than a reference to `ORQ_API_KEY` — today only Kimi Code, whose TOML
  has no env indirection.
- **Removed (breaking):** MCP and skills support in `orq setup`, `orq connect`
  and `orq disconnect`. The `mcp` capability is no longer accepted, the skills
  option is gone from the capability prompt, and `disconnect` no longer removes
  MCP entries. An entry written by v4.13.10 stays until you delete it by hand —
  remove the `orq` key under `mcpServers` (or `mcp`) in `~/.claude.json`,
  `./.mcp.json`, `~/.kimi-code/mcp.json`, `~/.config/opencode/opencode.json`,
  `~/.config/kilo/kilo.json`, and the `[mcp_servers.orq]` table in codex's
  `config.toml`. `orq launch --mcp` is unaffected: it wires MCP for a single
  session using your login, and writes nothing to disk.
- **Removed (breaking):** `--global` and `--local` on `orq connect` and
  `orq disconnect`. No provider config is project-scoped — every agent reads its
  gateway configuration from one absolute path — so both flags had no effect
  once MCP was removed.
- **Changed:** `orq connect` no longer offers Claude Code, and `--status` no
  longer reports it as unwired. Claude reads its endpoint from the environment
  and has no gateway provider config, so with MCP gone there is nothing to
  write for it — `orq launch claude` is still the way to route it through orq.
  Naming it explicitly still works and explains itself.
- Added: `orq connect --status` — the read-only answer to "what is wired?":
  one line per wired capability with its file, one naming detected-but-unwired
  agents. No prompt, no auth, no writes, exit 0.
- Changed: `orq doctor` no longer warns about agents you simply have not
  connected. Healthy per-agent rows collapse into one `coding_agents` summary
  (`2 of 5 wired: kimi, opencode`); the state that breaks something — wired
  without `ORQ_API_KEY` in the shell — still gets its own warning row.
  `--json` keeps every per-agent row plus the summary.
- Changed: interactive `orq connect` with no saved key offers to log in (or
  mint a key when you are already signed in) and continues, instead of erroring
  after you finished selecting. Declining exits 0 with a hint; non-interactive
  and `--yes` runs keep the error and never create credentials.
- Added: `orq connect [agent...] [capability...]` and `orq disconnect` — wire
  coding agents to orq permanently, and remove exactly what was written.
  Capabilities are positional (`gateway`); no agents named means every
  detected agent, which both verbs ask about before
  acting: naming agents is intent, a bare run is not. Without a TTY they refuse
  rather than guess — name the agents or pass `--yes`. `disconnect` lists the
  files it would touch before asking (default no), takes `--dry-run`, and names
  the pre-orq backup when one exists. `tracing` is reserved vocabulary and says
  so when selected.
- Changed: `orq setup` owns the machine, not the agents. It authenticates,
  mints or reuses the key, and ends by offering to connect detected agents
  (a consent gate, then agent selection). Non-interactive runs
  wire nothing and print `Next: orq connect`. The unreleased
  `setup coding-agents` subcommand and the agent flags (`--coding-agent`,
  `--no-coding-agents`, `--no-mcp`, `--no-gateway`) moved to `connect` or were
  dropped; none of them ever shipped.
- Changed: `orq auth logout` warns when wired agents remain, naming
  `orq disconnect`.
- **Removed (breaking):** `orq setup --agent` and `orq setup --no-agent`, the
  v4.13 spellings, are gone. Use `--coding-agent` and `--no-coding-agents`.
  They were briefly kept as hidden aliases; removing them means a script still
  passing the old name fails at parse time rather than having the flag ignored.
- Added: `orq launch <agent>` — starts claude, codex, opencode, kilo, kimi or
  pi preconfigured to route every model call through the orq AI Router, with
  `--dry-run`, `--model`, and `--mcp` to wire the orq MCP server.
  Per-invocation only: nothing is written to your agent's own configuration.
- **Changed:** `orq launch` runs locally only. `--sandbox`, `--mount-cwd`,
  `--rebuild` and `--local` are gone, along with the Docker image build and the
  "local execution" safety prompt — with no sandbox to switch to, a prompt
  offering one option was a keystroke tax. The names now belong to the agent,
  which fixes a collision: codex's own `--sandbox <mode>` is reachable again.
  Launcher flags are still recognised only before the first agent-owned
  argument, so put them first: `orq launch codex --dry-run --sandbox read-only`.
  Sandboxing returns as its own piece of work.
- **Changed (breaking):** `orq launch --mcp` is opt-in. The orq MCP server used
  to be wired on every launch with `--no-mcp` to decline; it is now off unless
  you pass `--mcp`, and `--no-mcp` is accepted but does nothing. The skills
  plugin follows MCP, so it is off by default too. A wrapper relying on the old
  default gets an agent with no orq tools and no error.
- **Changed:** the API key `orq setup` mints is owned by you, not by a service
  account. Service accounts can only be created by workspace admins, so the old
  behaviour failed for Developer and Researcher members of an Enterprise
  workspace. The consequence worth knowing: a user-owned key is revoked when
  that user leaves the organisation, where a service-account key outlived them.
  Runs with no session, or a session carrying no user record, still mint against
  a service account.
- Added: `orq setup` — signs you in, creates (or reuses) a workspace API key,
  and registers the orq MCP server and gateway provider in the config files of
  the coding agents it detects. Unlike `orq launch`, these writes are
  persistent. It does not ask about projects: API keys are workspace-scoped.
  `pi` is wired gateway only — an `orq` provider merged into its agent
  directory's `models.json` (`$PI_CODING_AGENT_DIR`, or `~/.pi/agent`), reached
  with `pi --model orq/<model>` — because pi has no native MCP.
- Fixed: declining "Create a workspace API key now?" no longer wires the
  session's expiring access token into agent configs. kimi embeds the
  credential literally, so the token landed in `~/.kimi-code/config.toml`,
  worked for under an hour, then failed every prompt with a 401. The provider
  write is now skipped with a warning until a durable key exists.
- Changed: when `ORQ_API_KEY` in the environment points at a different
  workspace than your login, setup says which workspace wins; the note no
  longer appears when both point at the same place. After MCP wiring, setup
  suggests the agent-skills install, and the step-3 agent rows drop the
  scope-note suffix.
- Changed: the API key `orq setup` saves is reused only for the workspace it
  was minted in. Running setup against a different workspace (`--workspace`, or
  the interactive picker) mints a fresh key for it instead of silently wiring
  every agent config to the old one; `orq connect` refuses in that
  situation, since it never creates keys. Keys saved by earlier builds, and keys
  supplied with `--api-key`, carry no workspace record — their provenance is
  unknowable, so they are reused as before rather than refused. `orq setup
  coding-agents --api-key` no longer saves the key it was handed: it is used for
  that run only, leaving the credential every other command authenticates with
  untouched.
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
  The warning names what actually breaks: for kimi, whose provider config holds
  the key literally, model calls keep working and only the MCP tools fail.
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
`--model`, `--mount-cwd`, `--rebuild`, `--mcp`, `--no-skills` or
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
