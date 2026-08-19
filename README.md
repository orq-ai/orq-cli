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

Installs a raw binary to `~/.orq/bin/orq`, verifies it against the release's published per-asset `.sha256`, adds the install directory to your shell profile, and then runs `orq setup` to get you authenticated. Pass flags after `-s --`:

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

Three modes, same command:

```sh
orq setup                # short path — asks only what it cannot infer
orq setup -i             # asks about every choice
orq setup --no-input \
  --api-key "$ORQ_API_KEY" \
  --coding-agent codex   # fully parameterized, for CI
```

Supported coding agents: `claude`, `codex`, `opencode`, `kimi`, `kilo`, `pi` (repeat `--coding-agent` for several). Each gets the `orq-workspace` MCP server registered in its own config format, except `pi`: it has no native MCP (extensions only), so setup wires its gateway provider alone.

Setup does not install skills, it suggests them: a run that wired MCP prints `npx skills add orq-ai/assistant-plugins`, which detects your agent and writes into its own config. `orq launch` separately loads them session-only for claude via `--plugin-url`. The plugin is an [Agent Plugins 1.0.0](https://agent-plugins.org) package; shipping the MCP server inside it is deferred, since the spec forbids credentials in headers.

**Setup also registers orq as a model provider** for kimi, codex, opencode, kilo and pi, so their own LLM calls can route through the orq AI Gateway and show up in your traces. The provider is registered as an **available option, never the agent's default** — setup cannot guarantee `ORQ_API_KEY` is exported in every future shell, and an agent whose default points at a provider with no credential fails on every run. The exception is kimi, which fills its `default_model` only when the config has none. `orq launch <agent>` remains the way to get orq as the default for a session.

| Agent | Setup writes | Route through orq by |
|---|---|---|
| `kimi` | `[providers.orq]` + the model list into `~/.kimi-code/config.toml` | just running `kimi` (default filled when absent) |
| `codex` | a self-contained profile at `$CODEX_HOME/orq.config.toml` (default `~/.codex/`) | `codex --profile orq` |
| `opencode` | `provider` blocks merged into `~/.config/opencode/opencode.json` | picking an **Orq AI Gateway** model in the picker |
| `kilo` | `provider` blocks merged into `~/.config/kilo/kilo.json` | picking an **Orq AI Gateway** model in the picker |
| `pi` | an `orq` provider merged into `~/.pi/agent/models.json` | `pi --model orq/<model>`, or the `/model` picker |
| `claude` | nothing — claude has no provider concept, only all-or-nothing env routing | `orq launch claude` |

Models come from the live gateway catalogue (enabled chat models with tool calling), keyed by their canonical ref. The default the agent opens with is chosen from that ranking. `orq setup` sends no model call of its own: a probe would bill your credits and open a trace in your workspace to prove something you did not ask to have proven. Your first agent request is the test, and `orq doctor` reports gateway funding for free before you get there.

Your API key is **not written into an agent config**, with one exception — those configs reference the `ORQ_API_KEY` environment variable, and the real value goes to `~/.orq/credentials.json` (mode 0600) and `~/.orq/env`, which setup offers to source from your shell profile (`~/.orq/env.fish` for fish).

The exception is kimi: version 0.34 reads a provider credential only as a literal in `config.toml`, ignoring both `${ORQ_API_KEY}` interpolation and an `env_key` indirection, so `~/.kimi-code/config.toml` holds the key itself. Setup writes that file mode 0600.

| Flag | Effect |
|---|---|
| `--workspace <key>` | Activate a workspace |
| `--api-key <key>` | Use this key instead of logging in and creating one |
| `--coding-agent <name>` | Wire a coding agent (repeatable) |
| `--global` | Write agent config under `$HOME` instead of the current project. Only claude and kimi's MCP config are scope-aware; codex, opencode and kilo read exclusively from their home-directory configs (opencode and kilo reject `{env:…}` references in a project file), so theirs are always global |
| `--no-coding-agents` | Skip coding-agent wiring |
| `--no-mcp` / `--no-gateway` | Wire only the gateway / only MCP (both = skip, same as `--no-coding-agents`) |
| `--no-input` | Never prompt; missing values become errors |

Re-running just the wiring — after installing a new agent, say — is `orq setup coding-agents`, which reuses the key an earlier `orq setup` saved rather than creating another. `--gateway` and `--mcp` narrow it to one half. Not to be confused with `orq agents`, which manages Orq Agents in your workspace; these wire the coding-agent CLIs on this machine.

Scope is chosen automatically: setup writes into the current directory when it looks like a project (`.git`, `package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`) and falls back to `$HOME` otherwise, so installing from your home directory does not scatter config files there.

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

Every command accepts `--profile <name>` (or the `ORQ_PROFILE` env var). Each profile has its own session file at `~/.orq/sessions/<name>.json` and its own API key credentials in `~/.orq/credentials.json`. The default profile is `default`.

```sh
# personal account against SaaS
orq auth login

# work account against SaaS
orq --profile work auth login
orq --profile work workspace use marketing
orq --profile work prompts list

# self-hosted customer
orq --profile acme auth login --api-base-url https://orq.acme.internal
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

For local mode the agent CLI itself must be installed (each subcommand prints an install hint when it is missing); `--sandbox` installs it into the container image for you. All requests appear in your orq.ai traces and logs like any other gateway traffic.

The [orq MCP server](https://api.orq.ai/v2/mcp) is wired into each launched agent automatically using its native mechanism — the API key is passed by env-var reference, never written into config files. Opt out with `--no-mcp`, point elsewhere with `ORQ_MCP_URL`. Exception: pi has no built-in MCP support (extensions only), so nothing is wired there.

For claude, the [orq skills plugin](https://github.com/orq-ai/assistant-plugins) is loaded **session-only** via `--plugin-url` — nothing is installed into your `~/.claude` config. Opt out with `--no-skills`, override the zip with `ORQ_SKILLS_URL`.

### Shared flags

| Flag | Description |
|---|---|
| `--model <id>` | Gateway model id, e.g. `anthropic/claude-sonnet-4-6` |
| `--models <list>` | Extra model ids: comma-separated or JSON array (opencode, kilo, kimi, pi) |
| `--base-url <url>` | Override the gateway base URL |
| `--no-fetch-models` | Skip fetching the enabled-model catalog |
| `--no-mcp` | Do not wire the orq MCP server into the agent |
| `--no-skills` | Do not load the orq skills plugin (claude only) |
| `-p, --prompt <text>` | One-shot prompt, mapped to the agent's own syntax |
| `--local` | Run directly on this computer |
| `--sandbox` | Run inside a throwaway Docker container |
| `--mount-cwd` | Sandbox only: mount the current directory read-write at `/workspace` |
| `--rebuild` | Sandbox only: rebuild the Docker image (`--no-cache --pull`) |
| `--dry-run` | Print the resolved command and env (key redacted) without launching |

Launcher flags are recognized only **before** the first agent-owned argument — everything from the first arg the launcher doesn't recognize onwards goes to the agent verbatim (so agent flags that collide with ours, like codex's `--sandbox <mode>`, stay reachable). Everything after `--` is passed to the agent untouched:

```sh
orq launch claude -- --resume
orq launch codex -- exec --full-auto "fix the build"
```

### Local vs sandbox

Local mode runs the agent directly on your machine — it has full access to your filesystem, shell, and network, so an interactive warning is shown on TTYs. `--local` states that intent up front and skips the warning; `ORQ_LAUNCH_NON_INTERACTIVE=1` suppresses every prompt. Passing `--local` with `--sandbox` is an error.

`--sandbox` runs the agent inside a throwaway Docker container instead: the image is built locally on first use, **nothing is mounted by default** (opt in with `--mount-cwd`), and the container is removed when the session ends. Works with Docker Desktop (the `docker` CLI is the only requirement). The routing env (including the API key) is passed at `docker exec` time via name-only `-e` flags, so it never appears in the container's `docker inspect` config or in host `ps`. Leftover containers can be removed manually with:

```sh
docker ps -a --filter label=orq.launch=1 -q | xargs docker rm -f
```

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
| `ORQ_API_KEY` | API key for headless/CI auth |
| `ORQ_PROFILE` | Default profile (same effect as `--profile`) |
| `ORQ_SERVER` | Override generated-command base URL (same as `--server`) |
| `ORQ_API_BASE_URL` | Override the orq host. Drives auth (`auth login`, `whoami`, `workspace`) **and** the URLs `orq setup` writes and `orq launch` injects — router, anthropic and MCP |
| `ORQ_V1_BASE_URL` | Override v1 API base URL (advanced/local dev) |
| `ORQ_PROFILE_BASE_URL` | Override profile endpoint (advanced/local dev) |
| `ORQ_CLI_VERSION` | Version to install via `install.sh` |
| `ORQ_CLI_INSTALL_DIR` | Install directory for `install.sh` |
| `ORQ_WEB_BASE_URL` | Web app base URL used for the links `orq setup` prints |
| `ORQ_NO_SPLASH` | Suppress the `orq setup` banner |

`.env` and `.env.local` files in the current directory are loaded automatically.

---

## Self-hosted orq.ai

```sh
orq --profile acme auth login --api-base-url https://orq.acme.internal
```

The host is stored in the session and reused for every subsequent command on that profile. No configuration files, no per-command flag, no env vars. Switch back and forth between profiles without logging out of either:

```sh
orq --profile acme prompts list            # talks to acme's backend
orq --profile default prompts list         # talks to api.orq.ai
```

That one host also drives everything `orq setup` writes and `orq launch` injects, so a coding agent on a self-hosted deployment never talks to the public gateway:

| Derived from `--api-base-url` | Used by |
|---|---|
| `<host>/v3/router` | model calls for codex, opencode, kilo, kimi, pi |
| `<host>/v3/anthropic` | model calls for claude (Anthropic-native API) |
| `<host>/v2/mcp` | the orq MCP server registered in each agent |

```sh
orq --profile acme auth login --api-base-url https://orq.acme.internal
orq --profile acme setup                   # writes acme's URLs into the agent's config
orq --profile acme launch kimi             # model calls stay on acme's network
```

Nothing is compiled in: the same released binary serves SaaS, staging and every self-hosted deployment. `https://api.orq.ai` is only the fallback when there is no session and no override.

Without a session — CI, or an API key alone — set `ORQ_API_BASE_URL` instead, which resolves the same three URLs.

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
