# orq.ai API

Generated CLI for the orq.ai API API.

The examples below assume the compiled binary is named `orq`. If you install it under a different name, replace that in the commands below.

## First Run

Build the CLI:

```sh
make build
```

Install it locally under `~/.local/bin`:

```sh
make install-local
```

If you prefer shell scripts over `make`, the scaffold also includes:

```sh
./scripts/build.sh
./scripts/install-local.sh
```

Refresh Bartolo-owned scaffold files after upgrading Bartolo or the schema:

```sh
bartolo sync
```

The scaffold also includes:

- `.gitignore` for local binaries, env files, and editor noise
- `.editorconfig` and `.gitattributes` for predictable formatting and line endings
- `.env.example` with the default auth variable placeholder

Check the generated setup before making requests:

```sh
orq --json doctor
```

## Authentication

The generated CLI supports bearer token authentication from environment variables or a stored profile.

Environment variables:
- `ORQ_TOKEN`
- `ORQ_API_KEY`
- `ORQ_AUTHORIZATION`

Project-local `.env` and `.env.local` files are loaded automatically if present.

Profile setup:

```sh
orq auth setup
```

## Command Surface

This CLI groups commands by product/resource noun inferred from the OpenAPI tags.
- `orq agents --help` Agents
- `orq agents-responses --help` Agents Responses
- `orq api-keys --help` API keys authenticate programmatic access to the workspace. The unified key model exposes opaque tokens, per-domain access grants, and budget / rate-limit constraints (see ADR 0001 and ADR 0002).
- `orq budgets --help` Budgets govern spend, token usage, and request rate across six scopes: workspace, project, identity, api-key, provider, and model. A budget is hierarchical and defense-in-depth — every applicable budget is a hard gate, and the most restrictive one wins per dimension (see ADR 0007).
- `orq chunking --help` Chunking
- `orq datasets --help` Datasets
- `orq deployments --help` Deployments
- `orq documentation --help` Documentation
- `orq evals --help` Evals
- `orq feature-previews --help` Feature Previews
- `orq feedback --help` Feedback
- `orq files --help` File upload and retrieval operations.
- `orq guardrail-rules --help` Guardrail Rules
- `orq human-review-sets --help` Human Review Sets
- `orq identities --help` Identities
- `orq knowledge-bases --help` Knowledge Bases
- `orq management-keys --help` Management keys are workspace-scoped credentials that authenticate programmatic access to workspace administration surfaces (API keys, budgets). Unlike project-scoped API keys, a management key always operates at the workspace level.
- `orq memory-stores --help` Memory Stores
- `orq models --help` Models
- `orq pii --help` Pii
- `orq policies --help` Policies
- `orq projects --help` Projects organize resources within a workspace
- `orq prompts --help` Prompts
- `orq reporting --help` GenAI reporting API over canonical analytics rollups. Accepts a metric name, time range, grain, group-by, and filters; returns a typed time series and optional totals.
- `orq speech --help` Speech
- `orq transcriptions --help` Transcriptions
- `orq translations --help` Translations
- `orq chat --help` Chat
- `orq completions --help` Completions
- `orq embeddings --help` Embeddings
- `orq images --help` Images
- `orq moderations --help` Moderations
- `orq ocr --help` Ocr
- `orq rerank --help` Rerank
- `orq routing-rules --help` Routing Rules
- `orq skills --help` Skills are modular instructions you can use to codify processes and conventions
- `orq tools --help` Tools
- `orq traces --help` Traces
- `orq schedules --help` Schedules
- `orq responses --help` Responses

## Examples

### Check setup

Verify config, auth source, and selected server before making API calls.

```sh
orq --json doctor
```

### Inspect server defaults

See the generated server targets and persist a default when the spec provides multiple environments.

```sh
orq server list
```

### Persist the default output format

Write the preferred output format into the CLI config so future commands use it automatically.

```sh
orq default-format json
```

### Explore a command group

Inspect the grouped product commands synthesized from the OpenAPI tags.

```sh
orq agents --help
```

### Run a grouped command

Replace any positional placeholders with real values from your environment.

```sh
orq agents create
```

### Use the raw escape hatch

Call the API directly with configured auth when a high-level command is missing.

```sh
orq request get /v2/agents
```

The same generated examples are also written to `examples/README.md` for quick copy/paste references.

## Raw Request Escape Hatch

Use the built-in raw request command when a high-level command is missing:

```sh
orq request get /path
orq request post /path < body.json
```

## Custom Commands

Bartolo keeps generated and user-owned code separate:

- CLI entrypoint: `cmd/orq/main.go`
- Generated API code: `cli/generated/`
- User-owned extensions: `cli/custom/`
- Add your own commands or hook registrations inside `cli/custom/Register(...)` so regeneration does not overwrite your work.

## Output Conventions

- Prefer `--json` when you want machine-readable output.
- Use `--help` on any command group or command to inspect flags and required args.
- Use `help-input` when a command accepts a request body from stdin or CLI shorthand.
- Use `server list`, `server use`, and `server set` to manage generated server defaults.
- `make build` writes the binary to `./bin/orq`.
- `make install-local` installs the binary to `~/.local/bin/orq` by default.
- `make completions` writes shell completion files into `./completions/`.
