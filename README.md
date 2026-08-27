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
```

`ORQ_CLI_VERSION` and `ORQ_CLI_INSTALL_DIR` still work as equivalents of `--version` and `--install-dir`. A checksum *mismatch* aborts the install, as does any failure to fetch the checksum other than a 404; releases published before the checksum assets existed simply skip verification with a notice.

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
orq setup                # sign in, wire up your coding agents
```

That is the whole first run. It signs you in, creates a workspace API key (reused on later runs), and wires your coding agents to orq. Projects are never asked about: keys are workspace-scoped, and project scope belongs where resources are created (agents, deployments). After that:

```sh
orq whoami               # verify identity
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
orq connect claude mcp --local     # this project only (mcp is the only scoped capability)
orq connect --dry-run              # show the files that would change
orq disconnect codex               # remove exactly what connect wrote
```

An interactive `orq setup` ends by offering to connect the agents it detects, so one command still takes a new machine to working. Non-interactive runs stop after the key and print `Next: orq connect` — in CI, compose the two: `orq setup --no-input --api-key "$KEY" && orq connect codex`.

Supported coding agents: `codex`, `opencode`, `kimi`, `kilo`, `pi`. `claude` is not offered the gateway: it has no provider config and routes purely through environment variables, so `orq launch claude` is the way to route its model calls. It does receive `skills` and `mcp`.

Connect handles four capabilities: `gateway`, `tracing`, `skills` and `mcp`. Name none and it writes the ones the agent can take. `orq connect claude mcp` writes the orq MCP server's URL into the agent's own config and **nothing else** — no key, no header, no bearer variable — and the agent logs in to that server itself; the command prints its login step. `pi` has no MCP support at all and says so rather than reporting a wire. `--global` (the default) writes machine-wide, `--local` writes to this project — only `mcp` has a project scope, and `--local` is refused from your home directory, where the `~/.mcp.json` it would produce would follow you into every session started from home.

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

Your API key is **not written into an agent config**, with one exception — those configs reference the `ORQ_API_KEY` environment variable, and the real value goes to `~/.orq/credentials.json` (mode 0600) and `~/.orq/env`, which setup offers to source from your shell profile (`~/.orq/env.fish` for fish).

The exception is kimi: version 0.34 reads a provider credential only as a literal in `config.toml`, ignoring both `${ORQ_API_KEY}` interpolation and an `env_key` indirection, so `~/.kimi-code/config.toml` holds the key itself. Setup writes that file mode 0600.

Setup flags: `--workspace <key>` (activate a workspace), `--api-key <key>` (use this key instead of logging in and creating one), `-i` (ask about every choice), `--no-input` (never prompt; missing values become errors).

Connect and disconnect take agents and capabilities as arguments, plus:

| Flag | Effect |
|---|---|
| `--api-key <key>` | Use this key for the run; it is not saved |
| `--dry-run` | Show the files that would change, write nothing (connect only) |
| `--yes` | Act on every detected agent without asking |
| `--status` | Print what is wired on this machine and exit (connect only) |

Re-running the wiring after installing a new agent is just `orq connect <agent>`; it reuses the key setup saved rather than creating another. Not to be confused with `orq agents`, which manages Orq Agents in your workspace; connect wires the coding-agent CLIs on this machine.

Every provider config resolves to one absolute path, so there is no project-versus-home scope to choose: `--global` and `--local` were removed once MCP left, because no config was project-scoped any more.

---

## Authentication

The CLI supports two auth methods. Both respect `--profile <name>` so you can keep multiple identities (personal account, CI, self-hosted customer) side by side.

### OAuth device login (interactive)

```sh
orq auth login
```

This walks you through a browser-based device-authorization flow, writes credentials to `~/.orq/sessions/default.json`, and picks an active workspace. Re-running `orq auth login` refreshes the session. Sign out with `orq auth logout`.

### API key (headless / CI)

```sh
export ORQ_API_KEY=sk_live_...
orq agents list
```

For multiple keys, save each one to a profile:

```sh
orq auth add-profile apikey ci <api-key>
orq --profile ci agents list
```

---

## Profiles

Every command accepts `--profile <name>` (or the `ORQ_PROFILE` env var). On `orq launch` it goes before the agent name — `orq launch --profile acme claude` — because everything after the agent name is handed to the agent. Each profile has its own session file at `~/.orq/sessions/<name>.json` and its own API key credentials in `~/.orq/credentials.json`. The default profile is `default`.

```sh
# personal account against SaaS
orq auth login

# work account against SaaS
orq --profile work auth login
orq --profile work workspace use marketing
orq --profile work prompts list

# self-hosted customer
orq --profile acme auth login --server https://orq.acme.internal
orq --profile acme prompts list
```

After login, every command on that profile automatically routes to the host you authenticated against — you do not need to pass `--server` on subsequent calls. Override once with `--server <url>` or `ORQ_SERVER=<url>` when you need to talk to a different host.

---

## Workspaces

```sh
orq workspace list         # list workspaces available to the active identity
orq workspace use <key>    # switch active workspace (persists in the session)
orq whoami                 # current user + active workspace + URL config
```

---

## Diagnostics

```sh
orq doctor
orq doctor --json          # machine-readable
```

`doctor` reports:

- CLI binary + runtime (version, platform/arch)
- Active profile + session file path
- Resolved `api_base_url`, `v1_base_url`, `auth_base_url`, `profile_base_url` with their *source* (flag, session, env, default, derived)
- Auth status (authenticated / missing / invalid / unreadable), user email, active workspace
- Reachability probes against each endpoint
- Bootstrap token freshness

---

## Output formats

```sh
orq agents list                             # TOON (default, human-readable)
orq agents list --output-format json        # JSON
orq agents list --output-format yaml        # YAML
orq agents list --json                      # shortcut for JSON
orq agents list -q 'data[].display_name'    # JMESPath query
```

Persist a new default:

```sh
orq default-format json
```

### Stability: what scripts may depend on

`--json` on stdout is the only stability-guaranteed machine contract; its
shape follows the orq API response behind the command. TOON, the default
terminal format, is presentation-only and may change rendering between
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
| `orq auth add-profile apikey <name> <key>` | Save an API-key profile |
| `orq auth list-profiles` | List configured credential profiles |
| `orq workspace list` | List workspaces |
| `orq workspace use <key>` | Switch active workspace |
| `orq doctor` | Diagnose config, auth, reachability |
| `orq update` | Update this binary to the latest release (`--check` reports only) |
| `orq request <method> <path>` | Raw API escape hatch (uses configured auth) |
| `orq server list` | List OpenAPI-registered servers |
| `orq completion bash\|zsh\|fish\|powershell` | Generate shell completions |
| `orq default-format <json\|yaml\|toon>` | Persist a default output format |
| `orq launch <agent>` | Launch a coding agent routed through the AI Router (see [Launch](#launch)) |

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

The [orq MCP server](https://my.orq.ai/v2/mcp) is wired by default, per session, using the agent's native mechanism; `--no-mcp` declines. No credential is written — the agent authenticates to that server itself — and the wire is skipped when `orq connect` has already written a persistent entry for that agent, so a session entry cannot shadow it. Point elsewhere with `ORQ_MCP_URL`. Exception: pi has no built-in MCP support (extensions only), so nothing is wired there. MCP tool calls share the free plan's daily request quota with model calls; `--no-mcp` is how you keep the quota for model calls.

orq's skills are linked into the agent's own skills directory **for the session only**; nothing is installed into your `~/.claude` config. Opt out with `--no-skills`. `ORQ_SKILLS_URL` pins your own plugin zip instead, which claude then fetches with `--plugin-url`.

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

## Environment variables

| Variable | Purpose |
|---|---|
| `ORQ_API_KEY` | API key for headless/CI auth. An explicitly typed `--profile` outranks it: the flag names credentials, and the CLI warns on stderr when it overrides an exported key |
| `ORQ_PROFILE` | Default profile (same as `--profile`, except that it does not outrank an exported `ORQ_API_KEY` — env against env has no tie-breaker) |
| `ORQ_SERVER` | The orq host (same as `--server`). Drives every command — auth (`auth login`, `whoami`, `workspace`), the generated API commands, and the URLs `orq setup` writes and `orq launch` injects: router, anthropic and MCP |
| `ORQ_API_BASE_URL` | Deprecated spelling of `ORQ_SERVER`, honored for one release. The matching `--api-base-url` flag on `auth`, `workspace` and `doctor` is deprecated the same way |
| `ORQ_V1_BASE_URL` | Override v1 API base URL (advanced/local dev) |
| `ORQ_PROFILE_BASE_URL` | Override profile endpoint (advanced/local dev) |
| `ORQ_CLI_VERSION` | Version to install via `install.sh` |
| `ORQ_CLI_INSTALL_DIR` | Install directory for `install.sh` |
| `ORQ_WEB_BASE_URL` | Web app base URL used for the links `orq setup` prints |
| `ORQ_NO_SPLASH` | Suppress the `orq setup` banner |
| `ORQ_NO_UPDATE_CHECK` | Suppress the update notice and the version check behind it |

`.env` and `.env.local` files in the current directory are loaded automatically.

### Updating

`orq update` replaces this binary with the latest published release through the
channel it was installed with: an npm install runs `npm install -g
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
orq --profile acme auth login --server https://orq.acme.internal
```

The host is stored in the session and reused for every subsequent command on that profile — there is one name for it, the global `--server` (env: `ORQ_SERVER`), and you only pass it when you want to divert a call. No per-command flag. `orq auth login --server <url>` binds the host to the profile it authenticates, so later calls on that profile need no flag at all. The full order, highest first: `--server`, `ORQ_SERVER`, the host bound to the active profile, a host persisted globally with `orq server set`, the session's own host, then `https://my.orq.ai`. `orq doctor` reports which of those the current run used. Switch back and forth between profiles without logging out of either:

```sh
orq --profile acme prompts list            # talks to acme's backend
orq --profile default prompts list         # talks to my.orq.ai
```

That one host also drives everything `orq setup` writes and `orq launch` injects, so a coding agent on a self-hosted deployment never talks to the public gateway. On `orq launch`, global flags go before the agent name — `orq launch --server <url> claude`, or `orq --server <url> launch claude` — because everything after the agent name is handed to the agent verbatim, so its own flags (`--resume`, codex's `-p`) reach it untouched.

| Derived from `--server` | Used by |
|---|---|
| `<host>/v3/router` | model calls for codex, opencode, kilo, kimi, pi |
| `<host>/v3/anthropic` | model calls for claude (Anthropic-native API) |
| `<host>/v2/mcp` | the orq MCP server, wired per session by `orq launch` and persistently by `orq connect mcp` |

```sh
orq --profile acme auth login --server https://orq.acme.internal
orq --profile acme setup                   # writes acme's URLs into the agent's config
orq launch --profile acme kimi             # model calls stay on acme's network
```

Nothing is compiled in: the same released binary serves SaaS, staging and every self-hosted deployment. `https://my.orq.ai` — the API spec's own `servers[0]` — is only the fallback when there is no session and no override.

Without a session — CI, or an API key alone — set `ORQ_SERVER` (or pass `--server`) instead, which resolves the same three URLs.

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

### Regenerating from OpenAPI

```sh
bartolo sync
```

This wipes `cli/generated/` and rebuilds it from `openapi.json`. **`cli/custom/` is never touched** — bartolo detects the existing directory and skips the stub.

### Cutting a release

Releases are fully automated by `.github/workflows/release.yml`:

1. Bump and push a tag: `git tag v0.1.1 && git push --tags`
2. Create a [GitHub Release](https://github.com/orq-ai/orq-cli/releases/new) from that tag and publish it
3. The workflow fires on `release: [published]` and:
   - Cross-compiles 5 platform binaries (`darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`, `win32-x64`)
   - Ad-hoc signs the macOS binaries with `codesign -s -`
   - Stamps the version into all 6 `package.json` files
   - Publishes the 5 platform packages to npm (in that order)
   - Publishes `@orq-ai/cli` wrapper to npm
   - Uploads raw binaries to the GitHub Release as assets for `install.sh` to fetch

Required repository secret:

- `NPM_TOKEN` — an npm [automation token](https://docs.npmjs.com/creating-and-viewing-access-tokens) with publish access to the `@orq-ai` organization.

To reproduce the release build locally (without publishing):

```sh
./scripts/release-build.sh 0.1.0
ls npm/cli-*/bin/
```

---

## License

MIT — see [LICENSE](./LICENSE).
