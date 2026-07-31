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
curl -fsSL https://raw.githubusercontent.com/orq-ai/orq-cli/main/install.sh | sh
```

Installs a raw binary to `~/.orq/bin/orq`. Pin a specific version or pick a different install directory:

```sh
# pin a version
curl -fsSL https://raw.githubusercontent.com/orq-ai/orq-cli/main/install.sh | ORQ_CLI_VERSION=v0.1.0 sh

# custom install dir (must be writable by the current user)
curl -fsSL https://raw.githubusercontent.com/orq-ai/orq-cli/main/install.sh | ORQ_CLI_INSTALL_DIR=/usr/local/bin sh
```

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
orq auth login           # OAuth device login; picks an active workspace
orq whoami               # verify identity
orq workspace list       # see available workspaces
orq prompts list         # run any generated command
orq doctor               # diagnose auth, config, and endpoint reachability
```

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
```

The agent CLI itself must be installed (each subcommand prints an install hint when it is missing). All requests appear in your orq.ai traces and logs like any other gateway traffic.

### Shared flags

| Flag | Description |
|---|---|
| `--model <id>` | Gateway model id, e.g. `anthropic/claude-sonnet-4-6` |
| `--models <list>` | Extra model ids: comma-separated or JSON array (opencode, kilo, kimi) |
| `--base-url <url>` | Override the gateway base URL |
| `--no-fetch-models` | Skip fetching the enabled-model catalog |
| `-p, --prompt <text>` | One-shot prompt, mapped to the agent's own syntax |
| `--sandbox` | Run inside a throwaway Docker container |
| `--mount-cwd` | Sandbox only: mount the current directory read-write at `/workspace` |
| `--rebuild` | Sandbox only: rebuild the Docker image (`--no-cache --pull`) |
| `--dry-run` | Print the resolved command and env (key redacted) without launching |

Everything after `--` is passed to the agent untouched:

```sh
orq launch claude -- --resume
orq launch codex -- exec --full-auto "fix the build"
```

### Local vs sandbox

Local mode runs the agent directly on your machine — it has full access to your filesystem, shell, and network, so an interactive warning is shown on TTYs (skip it with `ORQ_LAUNCH_NON_INTERACTIVE=1`).

`--sandbox` runs the agent inside a throwaway Docker container instead: the image is built locally on first use, **nothing is mounted by default** (opt in with `--mount-cwd`), and the container is removed when the session ends. Works with any Docker-compatible engine — Docker Desktop, [OrbStack](https://orbstack.dev), or Colima. Note that the routing env (including the API key) is visible via `docker inspect` to anyone with access to your Docker socket. Leftover containers can be removed manually with:

```sh
docker ps -a --filter label=orq.launch=1 -q | xargs docker rm -f
```

### Per-agent environment overrides

| Variable | Purpose |
|---|---|
| `ORQ_GATEWAY_URL` | Gateway base URL for all agents |
| `ANTHROPIC_MODEL` / `ANTHROPIC_SMALL_FAST_MODEL` | claude model selection |
| `ORQ_CODEX_BASE_URL` / `CODEX_MODEL` | codex overrides |
| `ORQ_OPENCODE_BASE_URL` / `OPENCODE_MODEL` / `OPENCODE_MODELS` | opencode + kilo overrides |
| `ORQ_KIMI_BASE_URL` / `KIMI_MODEL` / `KIMI_MODELS` | kimi overrides |

---

## Environment variables

| Variable | Purpose |
|---|---|
| `ORQ_API_KEY` | API key for headless/CI auth |
| `ORQ_PROFILE` | Default profile (same effect as `--profile`) |
| `ORQ_SERVER` | Override generated-command base URL (same as `--server`) |
| `ORQ_API_BASE_URL` | Override auth endpoint base URL (used by `auth login`, `whoami`, `workspace`) |
| `ORQ_V1_BASE_URL` | Override v1 API base URL (advanced/local dev) |
| `ORQ_PROFILE_BASE_URL` | Override profile endpoint (advanced/local dev) |
| `ORQ_CLI_VERSION` | Version to install via `install.sh` |
| `ORQ_CLI_INSTALL_DIR` | Install directory for `install.sh` |

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

---

## Development

### Project layout

```
cmd/orq/main.go              entrypoint
cli/generated/               bartolo-generated OpenAPI commands (DO NOT edit)
cli/custom/
├── register.go              custom entrypoint: middleware + commands
├── auth/                    OAuth device-login client, session store, URL resolution
└── commands/                cobra commands: auth, workspace, doctor, identity
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
