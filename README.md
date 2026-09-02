# orq.ai CLI

Official command-line interface for the [orq.ai](https://orq.ai) API.

Manage prompts, agents, deployments, knowledge bases, evaluators, and the rest of the orq.ai platform from your terminal, CI, or scripts. Works against orq.ai SaaS out of the box and against self-hosted deployments with a single flag.

---

## Installation

### npm (recommended)

```sh
npm install -g @orq-ai/cli
```

Requires Node.js 14 or newer. The matching native binary is downloaded automatically for your platform; no postinstall scripts or network downloads at runtime.

### curl | sh

```sh
curl -fsSL https://cli.orq.ai/install.sh | sh
```

Installs a raw binary to `~/.orq/bin/orq`, verifies it against the release's published per-asset `.sha256`, asks before adding the install directory to your shell profile (declining is fine — PATH just stays unset), and then runs `orq setup` to get you authenticated. Pass flags after `-s --`:

```sh
# pin a version
curl -fsSL .../install.sh | sh -s -- --version v0.1.0

# custom install dir (must be writable by the current user)
curl -fsSL .../install.sh | sh -s -- --install-dir /usr/local/bin

# just install: no profile edit, no setup
curl -fsSL .../install.sh | sh -s -- --no-modify-path --no-setup

# the pre-release line instead of stable
curl -fsSL .../install.sh | sh -s -- --channel rc
```

`ORQ_CLI_VERSION`, `ORQ_CLI_CHANNEL` and `ORQ_CLI_INSTALL_DIR` still work as equivalents of `--version`, `--channel` and `--install-dir`. `--channel` and `--version` together are an error, but an exported `ORQ_CLI_CHANNEL` is ambient config and is ignored rather than rejected when `--version` pins a release — that is the combination `orq update` passes. A checksum *mismatch* aborts the install, as does any failure to fetch the checksum other than a 404; releases published before the checksum assets existed simply skip verification with a notice.

### Pre-built release binaries

Grab a binary for your platform from the [Releases page](https://github.com/orq-ai/orq-cli/releases). Assets are named `orq-<os>-<arch>[.exe]`.

### Build from source

Requires Go 1.23 or newer:

```sh
git clone https://github.com/orq-ai/orq-cli.git
cd orq-cli
make build
./bin/orq --version
```

---

## Quick start

```sh
orq setup                # sign in, pick a project, wire up your coding agents
```

That is the whole first run. It signs you in, asks which project to work in when the workspace has more than one, creates an API key scoped to that project (reused on later runs), and wires your coding agents to orq. After that:

```sh
orq status               # verify identity, workspace and project
orq workspace list       # see available workspaces
orq prompts list         # run any generated command
orq doctor               # diagnose auth, config, and endpoint reachability
```

### `orq setup`

Setup authenticates the machine; wiring agents is `orq connect`:

```sh
orq setup                          # sign in, create the key, then offers to connect
orq connect                        # wire every detected agent through the gateway
orq connect codex kimi             # specific agents
orq connect codex gateway          # one capability: gateway, tracing, skills or mcp
orq connect claude mcp skills --local   # this project only (mcp and skills have a project scope)
orq connect --dry-run              # show the files that would change
orq disconnect codex               # remove exactly what connect wrote
```

An interactive `orq setup` ends by offering to connect the agents it detects, so one command still takes a new machine to working. Non-interactive runs stop after the key and print `Next: orq connect` — in CI, compose the two: `orq setup --no-input --api-key "$KEY" && orq connect codex`.

Between signing in and creating the key, setup asks which project to work in. It skips itself automatically at zero or one project — there is nothing to choose — and never offers to create one. `--project <id|key|name>` pre-answers it for a non-interactive run; `--no-project` skips it and leaves the session unscoped. An `--api-key` run skips it entirely, since there is no session for a project choice to narrow.

Supported coding agents: `codex`, `opencode`, `kimi`, `kilo`, `pi`. `claude` is not offered the gateway: it has no provider config and routes purely through environment variables, so `orq launch claude` is the way to route its model calls. It does receive `skills` and `mcp`.

Connect handles four capabilities: `gateway`, `tracing`, `skills` and `mcp`. Name none and it writes the ones the agent can take. `orq connect claude mcp` writes the orq MCP server's URL into the agent's own config and **nothing else** — no key, no header, no bearer variable — and the agent logs in to that server itself; the command prints its login step. `pi` has no MCP support at all and says so rather than reporting a wire. `--global` (the default) writes machine-wide, `--local` writes to this project: `mcp` goes into the agent's project config file (`.mcp.json`, `.codex/config.toml`, `opencode.json`, `kilo.json`, `.kimi-code/mcp.json`), and `skills` into `./.claude/skills` for Claude Code and `./.agents/skills` for every other agent — the two directories `npx skills` uses, anchored at the directory you run from. Add the directories it writes to `.gitignore`; connect names them, and the links point into `~/.orq`. `gateway` and `tracing` are machine-wide whatever the flag says. `--local` is refused from your home directory, where the config it would produce would follow you into every session started from home. A bare `orq disconnect` removes both scopes; a local install made from another directory is counted and left for a `--local` run from there. Codex loads its project config only for a repository marked trusted in `~/.codex/config.toml`; connect prints the line to add.

**Connect also registers orq as a model provider** for kimi, codex, opencode, kilo and pi, so their own LLM calls can route through the orq AI Gateway and show up in your traces. The provider is registered as an **available option, never the agent's default** — setup cannot guarantee `ORQ_API_KEY` is exported in every future shell, and an agent whose default points at a provider with no credential fails on every run. The exception is kimi, which fills its `default_model` only when the config has none. `orq launch <agent>` remains the way to get orq as the default for a session.

| Agent | Connect writes | Route through orq by |
|---|---|---|
| `kimi` | `[providers.orq]` + the model list into `~/.kimi-code/config.toml` | just running `kimi` (default filled when absent) |
| `codex` | a self-contained profile at `$CODEX_HOME/orq.config.toml` (default `~/.codex/`) | `codex --profile orq` |
| `opencode` | `provider` blocks merged into `~/.config/opencode/opencode.json` | picking an **Orq AI Gateway** model in the picker |
| `kilo` | `provider` blocks merged into `~/.config/kilo/kilo.json` | picking an **Orq AI Gateway** model in the picker |
| `pi` | an `orq` provider merged into `$PI_CODING_AGENT_DIR/models.json` (default `~/.pi/agent/`) | `pi --model orq/<model>`, or the `/model` picker |
| `claude` | nothing — claude has no provider concept, only all-or-nothing env routing | `orq launch claude` |

Models come from the live gateway catalogue (enabled chat models with tool calling), keyed by their canonical ref. The default the agent opens with is chosen from that ranking. `orq setup` sends no model call of its own: a probe would bill your credits and open a trace in your workspace to prove something you did not ask to have proven. Your first agent request is the test.

Your API key is **not written into an agent config**, with one exception — those configs reference the `ORQ_API_KEY` environment variable. A gateway key minted from a browser login lives in the server-keyed `~/.orq/sessions/<host>.json`; a saved API-key profile lives in `~/.orq/credentials.json`. Setup also writes the effective key to `~/.orq/env`, which it offers to source from your shell profile (`~/.orq/env.fish` for fish). These credential files are mode 0600.

The exception is kimi: version 0.34 reads a provider credential only as a literal in `config.toml`, ignoring both `${ORQ_API_KEY}` interpolation and an `env_key` indirection, so `~/.kimi-code/config.toml` holds the key itself. Setup writes that file mode 0600.

The key setup mints is scoped to the project you chose in the step above. That is what the coding agents use for model calls, so a config file another program reads — `~/.kimi-code/config.toml` included — can no longer reach the rest of the workspace. It does not scope the agents' MCP tools: those authenticate to the orq MCP server over OAuth, independent of the API key.

Setup flags: `--workspace <key>` (activate a workspace), `--project <id|key|name>` (pre-answer the project step), `--no-project` (skip the project step, leave the session unscoped), `--api-key <key>` (use this key instead of logging in and creating one), `-i` (revisit inferred workspace and API-key choices), `--no-input` (never prompt; missing values become errors).

Connect and disconnect take agents and capabilities as arguments, plus:

| Flag | Effect |
|---|---|
| `--api-key <key>` | Use this key for the run; it is not saved |
| `--dry-run` | Show the files that would change, write nothing (connect only) |
| `--yes` | Act on every detected agent without asking |
| `--status` | Print what is wired on this machine and exit (connect only) |

Re-running the wiring after installing a new agent is just `orq connect <agent>`; it reuses the key setup saved rather than creating another. Not to be confused with `orq agents`, which manages Orq Agents in your workspace; connect wires the coding-agent CLIs on this machine.

Every provider config resolves to one absolute path, so the gateway has no project-versus-home scope to choose; `--global` and `--local` act on `mcp` and `skills` only.

---

## Authentication

Two ways in. A browser login is an account on a server and sees every workspace
that account can; a profile is one saved API key. `--profile` selects a profile
and nothing else.

### OAuth device login (interactive)

```sh
orq auth login
```

Walks you through a browser device-authorization flow, stores the login in
`~/.orq/sessions/<host>.json` (`my.orq.ai.json` for the hosted service), and
picks an active workspace. Re-running `orq auth login` refreshes it. Sign out
with `orq auth logout`.

A second server is a second login, selected the way every other command selects
a server:

```sh
orq auth login --server https://orq.acme.internal
orq --server https://orq.acme.internal prompts list
orq server set https://orq.acme.internal     # make it the default
```

### API key (headless / CI)

```sh
export ORQ_API_KEY=sk_live_...
orq agents list
```

For several keys, save each as a profile and pick one per call, or persist the
pick:

```sh
orq auth profile add apikey ci <api-key>
orq --profile ci agents list
orq auth profile use ci
orq auth profile current
orq auth profile clear
```

While a profile is in force the login session is not consulted: `--workspace`
has no effect and `orq whoami` reports the profile. `--profile ""` turns a
persisted pick off for one call.

---

## Workspaces

```sh
orq workspace list         # list workspaces available to the active identity
orq workspace use <key>    # switch active workspace (persists in the session)
orq status                 # current user + active workspace + active project + server + credential in use (alias: orq whoami)
```

---

## Projects

Projects organize resources within a workspace. Selecting one narrows the session, not just the display: reads and creates are both scoped, with no per-command flag needed.

```sh
orq projects list                # list projects in the active workspace
orq projects use <project>       # select the active project, by id, key or name (persists in the session)
orq projects use                 # no argument at a terminal opens a picker; otherwise prints the active project
orq projects use --clear         # unset the active project
```

Selecting a project exchanges the session's access token for one scoped to it — `POST /v2/auth/access-token` takes the `project_id` and returns a token whose `projects` claim carries only that project. The server enforces the scope on both reads and creates: `orq agents list` goes from 112 agents workspace-wide down to the 9 that belong to the selected project, and `orq agents create` with no `--project-id` lands there.

For a single invocation without switching the session, pass `--project <id|key|name>` (env `ORQ_PROJECT`) on any command. Under an explicit API key there is no session token to narrow, so `--project` fills the command's own `--project-id` instead — an explicit `--project-id` always wins over it. Resolution order, highest first: the command's own `--project-id`, `--project`/`ORQ_PROJECT`, the session's active project, the workspace's default project.

`orq switch [workspace] [project]` walks both selections in one command. Switching workspaces clears the active project, since a project belongs to the workspace it was chosen in — `orq workspace use <other>` does the same, and `orq workspace use` on the workspace already active leaves the chosen project alone, because it only ever writes the workspace. `orq switch` has no such exemption: naming a workspace tells it to rewrite both halves, so `orq switch <already-active>` re-runs the project selection too.

Without a terminal, `orq switch` needs both halves named — `orq switch <workspace> <project>`. It rewrites your whole identity, so guessing at either half would replace a deliberately chosen project with the workspace default and report success. To move workspace alone, use `orq workspace use <workspace>`, which leaves the project untouched and re-asserts rather than failing when given no argument. Neither restriction applies at a terminal, where both commands open a picker.

---

## Diagnostics

```sh
orq doctor
orq doctor --json          # machine-readable
orq doctor --fix           # chmod the credential paths the permissions check flags (Unix; exits 1 if a repair fails)
```

`doctor` reports:

- CLI binary + runtime (version, orq API version, platform/arch)
- Active profile or session file path
- Resolved `api_base_url`, `v1_base_url`, `auth_base_url`, `profile_base_url` with their *source* (flag, session, env, default, derived)
- Auth status (authenticated / missing / invalid / unreadable), user email, active workspace
- Reachability probes against each endpoint
- Bootstrap token freshness
- Credential file permissions under `~/.orq` (Unix only) — group- or other-accessible paths only; a clean tree is not reported.
  Symlinked paths are judged on their target, and the finding names that target as well as the path under `~/.orq`. `--fix` chmods them (0600 files, 0700 directories); without it doctor only reports. A `--fix` run that could not repair something exits `1`; a run that only reports findings exits `0`. On Windows `--fix` is rejected — the bits it repairs do not exist there.

---

## Output formats

```sh
orq agents list                             # table at a terminal, TOON when piped
orq agents list --output-format toon        # TOON
orq agents list --output-format json        # JSON
orq agents list --output-format yaml        # YAML
orq agents list --json                      # shortcut for JSON
orq agents list --columns id,display_name   # pick table columns
orq agents list -j 'data[].display_name'    # JMESPath query
```

Persist a new default:

```sh
orq default-format json
```

### Stability: what scripts may depend on

`--json` on stdout is the only stability-guaranteed machine contract; its
shape follows the orq API response behind the command. The table a terminal
gets, and TOON, are presentation-only and may change rendering between
releases. Exit codes are `0` success, `1` failure, `130`/`143` on
SIGINT/SIGTERM, and errors go to stderr. The full contract, including how
command/flag removals are announced, lives in
[CHANGELOG.md](./CHANGELOG.md). The command surface itself is tracked in
[`surface.json`](./surface.json), which CI keeps in lockstep with the code so
surface changes are always a reviewed diff.

---

## Command reference

### Built-in commands

| Command | Purpose |
|---|---|
| `orq setup` | First-run onboarding: auth, API key, coding agents |
| `orq auth login` | OAuth device login |
| `orq auth logout` | Revoke refresh token, clear local session |
| `orq auth whoami` | Show current identity (alias: `orq whoami`) |
| `orq auth profile add\|list\|current\|use\|clear` | Save and select API-key profiles |
| `orq workspace list` | List workspaces |
| `orq workspace use <key>` | Switch active workspace |
| `orq projects list` | List projects in the active workspace |
| `orq projects use [project]` | Switch active project, by id, key or name (`--clear` unsets it) |
| `orq status` | Show active user, workspace, project, server and the credential in use (alias: `orq whoami`) |
| `orq switch [workspace] [project]` | Switch active workspace and/or project in one command |
| `orq doctor` | Diagnose config, auth, reachability (`--fix` repairs loose credential permissions) |
| `orq update` | Update this binary to the latest release (`--check` reports only) |
| `orq version` | Print the CLI version, the orq API version it was built against, and the install method |
| `orq request <method> <path>` | Raw API escape hatch (uses configured auth) |
| `orq server list` | List OpenAPI-registered servers |
| `orq completion bash\|zsh\|fish\|powershell` | Generate shell completions |
| `orq default-format <json\|yaml\|toon\|table>` | Persist a default output format |
| `orq launch <agent>` | Launch a coding agent routed through the AI Router (see [Launch](#launch)) |
| `orq orqi` | Run orqi, the orq.ai assistant, installing it on first use (see [orqi](#orqi)) |

### Resource commands

Use `--help` on any group for the full surface (inputs, body fields, examples):

```text
orq agents                   orq identities             orq rerank
orq annotations              orq images                 orq responses
orq audio                    orq knowledge-bases        orq router
orq chat                     orq memory-stores          orq router-guardrail-rules
orq chunking                 orq models                 orq router-policies
orq completions              orq moderations            orq router-routing-rules
orq contacts                 orq prompts                orq tools
orq datasets                 orq remote-configs
orq deployments              orq embeddings
orq evaluators               orq feedback
orq files                    orq human-evals
orq human-review-sets
```

**Deleting requires confirmation.** Generated `delete` commands prompt before
acting, and refuse to run when there is no terminal. Pass `--force` to skip the
prompt — scripts and CI need it:

```sh
orq datasets delete <id>           # asks first
orq datasets delete <id> --force   # required in CI
```

---

## Launch

`orq launch <agent>` starts a coding-agent CLI preconfigured to route every model call through the orq.ai AI Router — one command, no manual env or config wiring. Authenticate first with `orq auth login` (or export `ORQ_API_KEY`).

```sh
orq launch claude                 # Claude Code
orq launch codex                  # OpenAI Codex CLI
orq launch opencode               # OpenCode
orq launch kilo                   # Kilo CLI (OpenCode fork)
orq launch kimi                   # Kimi Code
orq launch pi                     # Pi Coding Agent
```

The agent CLI itself must be installed — each subcommand prints an install hint when it is missing. All requests appear in your orq.ai traces and logs like any other gateway traffic.

**Launch follows the workspace you are in now.** If `ORQ_API_KEY` holds the key `orq setup` minted and you have since run `orq workspace use <other>`, launch uses the workspace you switched to for that run and says so, rather than silently running against the one the key was minted for. Nothing on disk changes: the agent's own config, `~/.orq/env` and `credentials.json` are untouched, and `orq connect` stays the only thing that repoints an agent. A key you exported yourself still wins outright — its workspace is unknowable, so CI and bring-your-own-key setups are unaffected.

Agents stay pinned to whatever `orq connect` wired them against. `orq connect --status` names that workspace per agent, and `orq doctor` says so too when it differs from your active one, with the commands to move it.

The [orq MCP server](https://my.orq.ai/v2/mcp) is wired by default, per session, using the agent's native mechanism; `--no-mcp` declines. No credential is written — the agent authenticates to that server itself — and the wire is skipped when `orq connect` has already written a persistent entry for that agent, so a session entry cannot shadow it. Point elsewhere with `ORQ_MCP_URL`. Exception: pi has no built-in MCP support (extensions only), so nothing is wired there. MCP tool calls share the free plan's daily request quota with model calls; `--no-mcp` is how you keep the quota for model calls.

orq's skills are linked into the agent's skills directory under the directory you launch from (`./.claude/skills`, `./.agents/skills`) **for the session only**, and under your home directory when launched from there; nothing is installed permanently. Opt out with `--no-skills`. `ORQ_SKILLS_URL` pins your own plugin zip instead, which claude then fetches with `--plugin-url`.

### Shared flags

| Flag | Description |
|---|---|
| `--model <id>` | Gateway model id, e.g. `anthropic/claude-sonnet-5` |
| `--models <list>` | Extra model ids: comma-separated or JSON array (opencode, kilo, kimi, pi) |
| `--base-url <url>` | Override the gateway base URL |
| `--no-fetch-models` | Skip fetching the enabled-model catalog |
| `--mcp` | Wire the orq MCP server (workspace tools) into the agent — the default |
| `--no-mcp` | Do not wire the orq MCP server for this session |
| `--no-skills` | Do not link orq's skills into the agent for this session |
| `-p, --prompt <text>` | One-shot prompt, mapped to the agent's own syntax |
| `--dry-run` | Print the resolved command and env (key redacted) without launching |

There are two layers of flags. Global `orq` flags such as `--profile` and `--server` must appear before the agent name — either side of the `launch` word works, so `orq launch --profile acme --server https://orq.acme.internal kimi` and `orq --profile acme launch kimi` both select the profile and host. Once the agent name appears, those global flags belong to the agent and are forwarded verbatim. Launcher-specific flags in the table above (`--model`, `--mcp`, `--dry-run`, etc.) are recognized at the start of the agent's arguments; after the first agent-owned argument, everything goes to the agent untouched. A leading `--` ends launcher-specific parsing explicitly:

```sh
orq launch claude -- --resume
orq launch codex -- exec --full-auto "fix the build"
orq launch codex --dry-run --sandbox read-only   # --sandbox is codex's; put launcher flags first
```

### Running locally

The agent runs directly on your machine, with full access to your filesystem, shell, and network — the same access it has when you start it yourself. `ORQ_LAUNCH_NON_INTERACTIVE=1` suppresses every prompt, including the login prompt.

Sandboxed execution is not available in this version.

### Per-agent environment overrides

| Variable | Purpose |
|---|---|
| `ORQ_GATEWAY_URL` | Gateway base URL for all agents except claude (OpenAI-shaped router) |
| `ORQ_ANTHROPIC_BASE_URL` | claude gateway base URL (Anthropic-native endpoint) |
| `ANTHROPIC_MODEL` / `ANTHROPIC_SMALL_FAST_MODEL` | claude model selection |
| `ORQ_CODEX_BASE_URL` / `CODEX_MODEL` | codex overrides |
| `ORQ_OPENCODE_BASE_URL` / `OPENCODE_MODEL` / `OPENCODE_MODELS` | opencode + kilo overrides |
| `ORQ_KIMI_BASE_URL` / `KIMI_MODEL` / `KIMI_MODELS` | kimi overrides |
| `ORQ_PI_BASE_URL` / `PI_MODEL` / `PI_MODELS` | pi overrides |

---

## orqi

[orqi](https://github.com/orq-ai/orqi) is the orq.ai assistant in your terminal: ask it to
investigate a failing agent, check workspace health or explain the platform, and it answers
using your workspace's own tools, models and skills.

```sh
orq orqi                                   # interactive session
orq orqi "why did my agent fail today?"    # one-shot
orq orqi --install                         # install or reinstall, then exit
```

The first run installs it, after asking, by running the orqi repo's own installer. It lands in
`~/.local/bin` (or `$ORQI_INSTALL_DIR`) and the session starts straight away, whether or not
that directory is on your PATH yet. Under `--no-input` nothing is installed and the command
prints the one-liner instead. When orqi is already installed, `--install` reinstalls into that
binary's own directory rather than the default, so a copy from Homebrew or a source build is
updated in place instead of being shadowed by a second one.

orqi reads the login session this CLI maintains, so `orq auth login` is all the setup it needs.
orq's own global flags go in front of the command word, and orqi's go behind it:

```sh
orq --profile staging orqi "why did it fail?"   # --profile is orq's
orq orqi --version                              # --version is orqi's
```

That split is the whole rule. Nothing typed after `orqi` is read as one of orq's flags, so a
prompt opening with `--workspace` or `--verbose` reaches orqi as words rather than being eaten,
and any flag orqi grows later works without orq having to hear about it. The two exceptions are
`--help` and `--install`, which orq answers itself; a leading `--` ends orq's parsing explicitly,
so `orq orqi -- --install` sends `--install` to orqi.

`--no-input` refuses rather than installing, so a script that needs both does it in two calls:

```sh
orq orqi --install                          # unconditional, prompts nobody
orq --no-input orqi "summarise today"       # fails loudly if the first call did not run
```

orqi publishes macOS (arm64, x86_64) and Linux x86_64 builds; on anything else the command says
so and stops.

---

## Environment variables

| Variable | Purpose |
|---|---|
| `ORQ_API_KEY` | API key for headless/CI auth. Any active profile outranks it because the profile is the selected credential |
| `ORQ_PROFILE` | Default profile (same as `--profile`). A selected profile's API key and server both outrank ambient `ORQ_API_KEY` and `ORQ_SERVER` values |
| `ORQ_SERVER` | The orq host (same as `--server`). Used when no active profile supplies a server, and drives every command — auth (`auth login`, `whoami`, `workspace`), the generated API commands, and the URLs `orq setup` writes and `orq launch` injects: router, anthropic and MCP |
| `ORQ_API_BASE_URL` | Deprecated spelling of `ORQ_SERVER`, honored for one release. The matching `--api-base-url` flag on `auth`, `workspace` and `doctor` is deprecated the same way |
| `ORQ_V1_BASE_URL` | Override v1 API base URL (advanced/local dev) |
| `ORQ_PROFILE_BASE_URL` | Override profile endpoint (advanced/local dev) |
| `ORQ_CLI_VERSION` | Version to install via `install.sh` |
| `ORQ_CLI_CHANNEL` | Release line for `install.sh`: `stable` (default) or `rc` |
| `ORQ_CLI_INSTALL_DIR` | Install directory for `install.sh` |
| `ORQ_WEB_BASE_URL` | Web app base URL used for the links `orq setup` prints |
| `ORQ_NO_SPLASH` | Suppress the `orq setup` banner |
| `ORQ_NO_UPDATE_CHECK` | Suppress the update notice and the version check behind it |

`.env` and `.env.local` files in the current directory are loaded automatically.

### Versions

`orq version` prints the CLI version, the orq API version the build was
generated against, and the install method it was installed through; `--json`
gives `cli`, `api_version` and `install_method`. `orq --version` remains the
compact CLI-version line used by installers and scripts.

The CLI's version is its own — it does not track the orq API version, which is
why the API line has to be reported rather than read off the tag. `--channel rc`
installs the pre-release line, built from the staging API schema.

### Updating

`orq update` replaces this binary with the latest published release through the
install method it was installed with: an npm install runs `npm install -g
@orq-ai/cli@<version>`, an `install.sh` install re-runs the installer pinned to
that same version, which verifies the release checksum and swaps the binary in
atomically. An rc build follows the rc line rather than being moved onto the
older stable release. A binary that
arrived some other way is refused rather than overwritten, with both commands
printed so you can pick. `orq update --check` reports the versions and changes
nothing.

At most once a day, after a command succeeds, the CLI also checks the npm
dist-tag for its release line and prints a single stderr line if a newer version
exists, telling you to run `orq update`. The only request is a `GET` of the
public `registry.npmjs.org` dist-tags document: no version, platform or
identifier is sent anywhere. Nothing is printed when `ORQ_NO_UPDATE_CHECK` or
`CI` is set, when output is piped, when `--json`/`-o` requested a machine
format, or when the check fails.

---

## Self-hosted orq.ai

```sh
orq auth login --server https://orq.acme.internal
```

The server selects the login session. There is one name for it, the global
`--server` (env: `ORQ_SERVER`), and you only pass it when you want to divert a
call. No per-command flag. Persist a host with `orq server set` when you do not
want to pass it on subsequent calls. The full order, highest first: `--server`,
the active profile's server, `ORQ_SERVER`, a host persisted globally with
`orq server set`, then `https://my.orq.ai`. `orq doctor` reports which of those
the current run used.
Switch back and forth between servers without logging out of either:

```sh
orq --server https://orq.acme.internal prompts list
orq --server https://my.orq.ai prompts list
```

That one host also drives everything `orq setup` writes and `orq launch` injects, so a coding agent on a self-hosted deployment never talks to the public gateway. On `orq launch`, global flags go before the agent name — `orq launch --server <url> claude`, or `orq --server <url> launch claude` — because everything after the agent name is handed to the agent verbatim, so its own flags (`--resume`, codex's `-p`) reach it untouched.

| Derived from `--server` | Used by |
|---|---|
| `<host>/v3/router` | model calls for codex, opencode, kilo, kimi, pi |
| `<host>/v3/anthropic` | model calls for claude (Anthropic-native API) |
| `<host>/v2/mcp` | the orq MCP server, wired per session by `orq launch` and persistently by `orq connect mcp` |

```sh
orq auth login --server https://orq.acme.internal
orq --server https://orq.acme.internal setup
orq launch --server https://orq.acme.internal kimi
```

Nothing is compiled in: the same released binary serves SaaS, staging and every self-hosted deployment. `https://my.orq.ai` — the API spec's own `servers[0]` — is only the fallback when there is no server override.

For an exported API key in CI, set `ORQ_SERVER` (or pass `--server`) to target a
different host; a saved API-key profile carries its own server. Either source
resolves the same three URLs.

If a deployment serves the AI gateway from a different hostname than the platform API, override just that one with `ORQ_GATEWAY_URL` (all agents) or `--base-url` (one command) — see [per-agent environment overrides](#per-agent-environment-overrides). Both take precedence over the derived value.

---

## Development

### Project layout

```
cmd/orq/main.go              entrypoint
cli/generated/               bartolo-generated OpenAPI commands (DO NOT edit)
cli/custom/
├── register.go              custom entrypoint: middleware + commands
├── auth/                    OAuth device-login client, session store, URL resolution, projects/api-keys
└── commands/                cobra commands: auth, workspace, doctor, identity, setup, agents
npm/
├── cli/                     @orq-ai/cli wrapper (JS shim + optionalDependencies)
└── cli-<os>-<arch>/         per-platform binary containers
scripts/
├── build.sh                 local dev build
├── install-local.sh         install to ~/.local/bin
└── release-build.sh         cross-compile all platforms + stamp version
install.sh                    curl | sh installer
.github/workflows/release.yml CI release workflow
```

### Common commands

```sh
make build              # local dev binary at ./bin/orq
make install-local      # install to ~/.local/bin/orq
make completions        # generate shell completions into ./completions/
make tidy               # go mod tidy
make doctor             # run the doctor command
```

### Contributing

PR titles must be conventional commits (`feat:`, `fix(auth):`, `chore!:`) — CI
fails otherwise, and the type decides which release-notes section the PR lands
in. See [AGENTS.md](AGENTS.md) for the type → label table and the rest of the
repo conventions.

### Regenerating from OpenAPI

```sh
bartolo sync
```

This wipes `cli/generated/` and rebuilds it from `openapi.yaml`. **`cli/custom/` is never touched** — bartolo detects the existing directory and skips the stub.

### Cutting a release

You do not cut one by hand. `.github/workflows/release.yml` fires on every push
to `main`, releases when something that reaches a binary is unreleased —
`cli/custom/`, `cmd/orq/` (or `packages/orq-rc/cmd/` for the rc line), `npm/`,
`scripts/`, `install.sh`, `VERSION`, `go.mod`, `go.sum`, or either module's
`openapi.yaml`/`.bartolo.json`, changed since that line's last tag — and calls
`release-pipeline.yml`, which:

1. Resolves the version, by the rules in [Versioning](CHANGELOG.md#versioning),
   and takes the next free tag from it. Nobody tags by hand.
2. Stamps `CHANGELOG.md`: the hand-written `## Unreleased` section is renamed to
   the version being cut, a fresh empty one takes its place, and the section
   becomes the top of the release notes. An rc release becomes
   `<next-minor>.0-rc.<n>` and leaves the changelog alone.
3. Regenerates `cli/generated/` from the module's schema.
4. Commits the regenerated tree, the stamped `CHANGELOG.md` and the resolved
   `VERSION` back to `main` (signed, through the Git Data API — `main` requires
   verified signatures), then creates and pushes the release tag before
   building. This makes the version record and its tag one release identity; a
   tag may briefly have no assets.
5. Cross-compiles 5 platform binaries (`darwin-arm64`, `darwin-x64`,
   `linux-x64`, `linux-arm64`, `win32-x64`), ad-hoc signs the macOS ones, and
   stamps version and `orqApiVersion` into all 6 `package.json` files.
6. Creates an idempotent draft GitHub release, uploads the raw binaries, their
   `.sha256` files, the man pages and a stamped `install.sh`, then publishes it.
7. Calls `publish-npm.yml`, which publishes the packages under `latest`
   (stable) or `rc` (pre-release), skipping package versions already present so
   a retry can resume. It is called rather than triggered by the release event,
   because GitHub does not start a workflow run from an event raised with
   `GITHUB_TOKEN`; its `workflow_dispatch` is the manual retry. The npm
   dist-tags are what `install.sh --channel rc` resolves a version from.

`.github/workflows/**` is deliberately not a release trigger: a change to the
pipeline itself ships with the next release that has something to ship.

npm publishing authenticates through OIDC (`id-token: write`), not a stored
token. `workflow_dispatch` runs a release on demand — `stable`, `prerelease` or
`both` — and no longer collides with an existing tag, because the patch is
resolved rather than read.

The orq API version comes from `.bartolo.json`'s `app_version`, which
orquesta-web writes when it publishes a schema. It is recorded in the release
and in the binary; it is not the CLI's version.

To reproduce the release build locally (without publishing):

```sh
./scripts/release-build.sh 0.1.0
ls npm/cli-*/bin/
```

---

## License

MIT — see [LICENSE](./LICENSE).
