# omp Framework Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register omp (oh-my-pi) with `orq launch` and `orq connect`: a throwaway `models.yml` gateway provider plus a session MCP entry for launch, a durable merged provider plus MCP wiring for connect, and skills through the shared `~/.agents/skills` directory.

**Architecture:** Mirror the pi implementation on both sides, with three deliberate departures, each verified against omp's source in Task 0: launch reuses `BuildPiModelsJSON`'s output verbatim as `models.yml` content (JSON is valid YAML; Bun's parser accepts it — verified with a live run) instead of building a second serializer; launch writes a session `mcp.json` entry into the temp agent dir exactly the way kimi writes one into `KIMI_CODE_HOME`; connect edits `models.yml` through `yaml.Node`, replacing only the `providers.orq` subtree, so a hand-edited file keeps its comments and key order.

**Tech Stack:** Go 1.25, `go.yaml.in/yaml/v3` (already a direct dependency), table-driven Go tests.

**Spec:** [Linear RES-1698](https://linear.app/orqai/issue/RES-1698/add-omp-oh-my-pi-as-a-launch-and-connect-framework)

## Decisions taken (from the adversarial review of the first draft)

- **D1 — Launch wires MCP, kimi-style, over an overlay of the user's agent dir.** `orq launch omp` writes a session `mcp.json` (orq-workspace http entry, no headers) and `models.yml` into the temp agent dir, and symlinks every other entry of the user's agent dir into it (`overlayOmpAgentDir`). Those two files are the session's own — written through a link they would rewrite the user's files. Everything else stays the user's: omp keys the MCP OAuth credential by server URL in `agent.db`, so a login made once (`/mcp`, or after `orq connect omp mcp`) carries across launches, and settings, other-provider logins, extensions and sessions are the user's real ones. A pi-owned or missing dir is not overlaid. Cost: one launch step and the overlay; the user's other providers and other MCP servers are not visible in a launched session.
- **D2 — `PI_CODING_AGENT_DIR` ownership.** omp and pi share that variable (omp has none of its own — verified). A dir holding `models.json` but no `models.yml` is pi-owned: `ompDetect` returns false for it, and `writeOmpProviderYAML` and `writeOmpMCP` both refuse it with an error naming the variable. A dir holding `models.yml`, or a missing dir, is omp-writable. The rule is presence-based: a pi dir pi has not yet written `models.json` into is indistinguishable from an empty dir, so `orq connect omp` can claim it — no presence-based rule can tell the two apart, and refusing any pre-existing dir without `models.yml` would also refuse a genuine omp dir that already holds `mcp.json` or settings but no provider yet. Cost: one detection rule and its test.
- **D3 — omp-local `yaml.Node` helpers.** The JSON helpers (`readJSONConfig`, `removeJSONKeys`, …) stay as they are; the YAML writer and remover are new functions in `cli/custom/commands/omp.go`. Parametrising the JSON helpers over a codec would touch ~6 shipped callers for reuse the node-based approach cannot deliver anyway. Cost: two parallel helper families.
- **No second models builder.** `BuildPiModelsJSON`'s output is JSON text, and JSON is valid YAML: launch writes it to `models.yml` as-is, and connect parses it back with `yaml.v3` to splice the `providers.orq` node. Verified: Bun's built-in YAML parser — the one omp loads `models.yml` with (`config/config-file.ts:5`, `import { JSONC, YAML } from "bun"`) — parses the exact JSON shape `BuildPiModelsJSON` emits.

## Global Constraints

- Do not edit `cli/generated/`. Changes under `cli/custom/` must compile for both the root stable module and `packages/orq-rc`.
- No new dependencies: `go.yaml.in/yaml/v3` is already direct (`go.mod:17`).
- No credential ever lands in a config file: the provider block carries `apiKey: "$ORQ_API_KEY"` (omp resolves `$VAR` config values from `process.env` at request time — `src/config/resolve-config-value.ts:105-107`, wired through `authStorage.keys.setResolver`), and the MCP entry carries no headers.
- omp honors `PI_CODING_AGENT_DIR` for the agent dir (verified: `pi-utils/src/dirs.ts` — env override in default mode, default `~/.omp/agent`, named profiles derive their own dir and ignore the override). Every path resolver must honor it in the same order, or detection and writes disagree on a redirected machine. Named profiles are a non-goal: `ompPath`/`ompDetect` resolve the default dir (env override, then `~/.omp/agent`) and know nothing about profiles, so a session running under a named omp profile does not see what `orq connect omp` writes — the README states the same.
- omp reads no model env vars (verified: `src/config/model-resolver.ts` has no `$env`/`process.env` reads; only `PI_SMO_L_MODEL`/`PI_SLOW_MODEL` exist, for the smol/slow roles). `ResolveGatewayConfig` gets `ModelEnvKey: ""` and `ModelsEnvKey: ""` — unlike pi, which passes `PI_MODEL`/`PI_MODELS`.
- omp accepts `--provider <id>` and `provider/modelId` selectors (verified: `src/cli/flag-tables.ts:134`, `src/config/model-resolver.ts:788`), so `PreArgs{"--provider", "orq", "--model", <gateway model>}` works with slashed gateway ids.
- `models.yml` schema (verified: `src/config/models-config-schema-bundle.ts`): `providers.<name>` with `baseUrl?`, `apiKey?`, `api?` (`"openai-completions"` | `"openai-responses"` | …), `models[]` with `id` (required), `name?`, `api?`, `input?`, `contextWindow?`, `maxTokens?`.
- `mcp.json` schema (verified: `src/config/mcp-schema.json:190-225`): top-level `mcpServers` map; an http entry requires exactly `{type: "http", url}`; read from `<agentDir>/mcp.json` at user level (`src/discovery/builtin.ts:219-224`).
- omp reads skills from `.agent/skills` and `.agents/skills`, project walk-up plus user home (verified: `src/discovery/agents.ts:177`) — it is a sharedReader. Unlike pi, omp has no project-trust gating, so the pi-only setup trust note does not apply to it (`connect_test.go` asserts omp is not named).
- YAML edits go through `yaml.Node`: never round-trip a user's `models.yml` through `map[string]any` (that strips comments and reorders keys). The remover deletes a file only if we created it (no prior `.orq-bak`) and it is empty, and refuses a malformed file rather than overwriting it — the same rules `removeJSONKeys` (`cli/custom/commands/agents.go:1155-1196`) enforces for JSON.
- `surface.json` gains `orq launch omp`: a launch agent is a command-tree entry, not help text. Refresh with `go run ./cmd/surface-dump -write` and commit the delta (the PR's `72f2082b` did exactly that).
- Conventional commits; conventional PR title; `CHANGELOG.md` `**Added:**` entry under `## Unreleased`.

---

### Task 0: Verify omp's premises against its source (DONE — controller, 2026-10-02)

Every fact in Global Constraints was pinned against the installed omp source (`~/.bun/install/global/node_modules/@oh-my-pi/pi-coding-agent`, v18.4.12) and, where marked, a live `bun` run:

- [x] `PI_CODING_AGENT_DIR` honored, precedence env-first then `~/.omp/agent` (`pi-utils/src/dirs.ts`).
- [x] `models.yml` schema and `$ORQ_API_KEY` env resolution (`models-config-schema-bundle.ts`, `resolve-config-value.ts:105-107`).
- [x] JSON content in a `.yml` parses (Bun `YAML.parse`, live run over the exact `BuildPiModelsJSON` shape).
- [x] `mcp.json` `mcpServers` + `{type:"http", url}` (`mcp-schema.json:190-225`, `discovery/builtin.ts:219-224`).
- [x] Skills from `.agents/skills` walk-up + home (`discovery/agents.ts:177`).
- [x] `/mcp` interactive command exists (`slash-commands/helpers/mcp.ts`); no `omp mcp` CLI verb.
- [x] No `PI_MODEL`/`PI_MODELS`, no `OMP_AGENT_DIR`; `--provider` and `provider/modelId` selectors accepted.

### Task 1: Launch support (`orq launch omp`)

**Files:**
- Create: `cli/custom/launch/omp.go`, `cli/custom/launch/omp_test.go`
- Modify: `cli/custom/launch/agents.go` (register `ompAgent()` in `Agents()`), `cli/custom/launch/mcp.go` and `cli/custom/launch/pi.go` (update the two comments that say pi has no MCP support), `cli/custom/skills/targets.go` (add `"omp": true` to `sharedReaders`)

**Interfaces:**
- Consumes: `ResolveGatewayConfig`/`ResolveInput`, `ModelInfo`, `ResponsesModelSet`, `BuildPiModelsJSON`, `mcpURL`, `maybeInstallSessionSkills`, `appendModelWarnings`, `appendCapWarning`; `httpMCPConfigJSON` (`launch/mcp.go`) for the session-entry payload and `kimi.go:74-78` as the temp-dir session-entry pattern.
- Produces: `DefaultOmpModel = "openai/gpt-5.6-terra"`, `OmpProvider = "orq"`, `ompAgent() AgentDef`, `resolveOmp(*AgentContext) (*LaunchPlan, error)`, `writeOmpConfigDir(modelsJSON string) (dir string, cleanup func(), err error)`.

- [ ] **Step 1: Write failing tests**

  - `writeOmpConfigDir`: writes `models.yml` (0600) whose content is byte-identical to the input, into a fresh `os.MkdirTemp("", "orq-omp-")`; cleanup removes it.
  - `resolveOmp`: plan `Env` = `ORQ_API_KEY`, `ORQ_SERVER`, `PI_CODING_AGENT_DIR` (the temp dir); `PreArgs` = `--provider orq --model <gateway model>`; `TempDirs` holds the dir. With `--no-mcp`, no `mcp.json` lands in the dir; without it, `mcp.json` holds the orq-workspace http entry and no headers. Session skills never land under the temp dir (omp is a sharedReader; the redirect only moves config) — assert the temp dir contains only `models.yml` and `mcp.json`.
  - `go.yaml.in/yaml/v3` parses the written `models.yml` and finds `providers.orq.models` with the gateway model ids — the Go-side proof that JSON content in a `.yml` is valid YAML for any YAML consumer.

- [ ] **Step 2: Implement `writeOmpConfigDir`**

  `writeOmpConfigDir` mirrors `writePiConfigDir` (`pi.go:166-177`), writing the JSON string to `models.yml`. The session `mcp.json` entry comes from `httpMCPConfigJSON` (`launch/mcp.go`): the full document with `mcpServers.orq-workspace` = `{"type": "http", "url": url}` — the same payload claude's `--mcp-config` and copilot's `--additional-mcp-config` take.

- [ ] **Step 3: Implement `resolveOmp` and the `AgentDef`**

  `AgentDef{Name: "omp", Binary: "omp", Label: "omp", InstallHint: "npm install -g @oh-my-pi/pi-coding-agent (https://omp.sh)", FetchesModels: true, AllowModels: true, Prompt: -p/--prompt mapping identical to pi's, Resolve: resolveOmp}`. `resolveOmp` mirrors `resolvePi` (`pi.go:38-88`) with these differences: `BaseURLEnvKey: "ORQ_OMP_BASE_URL"`, `ModelEnvKey: ""`, `ModelsEnvKey: ""` (Task 0: omp reads no model env); no second builder — `BuildPiModelsJSON(resolved.BaseURL, resolved.GatewayModels, resolved.Infos)` output goes to `writeOmpConfigDir` verbatim; and after the dir is written, `if url := mcpURL(ctx); url != ""`, write `httpMCPConfigJSON(url)` to `<dir>/mcp.json` (0600) — the kimi pattern: the temp dir shadows the user's persisted `mcp.json`, so the session entry must always be written. Then `maybeInstallSessionSkills(ctx, plan, "omp")` plus pi's warnings (`appendModelWarnings(plan, resolved, noopNormalize, "openai/gpt-5-mini")`, `appendCapWarning(plan, resolved)`).

- [ ] **Step 4: Register and run focused tests**

  Add `ompAgent()` to `Agents()`. Update the `persistedMCPConfigured` comment (`mcp.go:139-152`) and the `resolvePi` MCP comment (`pi.go:65-67`): pi remains the one agent with no MCP support; omp, like kimi, always gets the session entry because its agent dir is redirected. Run `go test ./cli/custom/launch/... ./cli/custom/skills/... -count=1`; `TestCanaryKeyNeverLeaks` and `TestEveryAgentInheritsTheResolvedServer` iterate the registry and must pass with the new agent.

### Task 2: Connect support (`orq connect omp` / `orq disconnect omp`)

**Files:**
- Create: `cli/custom/commands/omp.go`, `cli/custom/commands/omp_test.go`
- Modify: `cli/custom/commands/agents.go` (register the `omp` `agentSpec`; add the `mcpLogin` field to `agentSpec`) and `cli/custom/commands/connect.go` (replace the `mcpLoginLine` switch with the spec field), plus the test files in the Step 5 audit list. `setup.go` stays untouched: its trust note is pi-only (omp has no project-trust gate), which the Step 5 audit below asserts.

**Interfaces:**
- Consumes: `detectPath`, `writeConfigFile`, `jsonProviderPresentAt`, `removeJSONKeys`, `writeMCPJSON`, `launch.BuildPiModelsJSON`, `launchCatalog`, `launch.MCPServerName`.
- Produces: `ompPath(rel string) func(bool) (string, error)`, `ompDetect() bool`, `writeOmpProviderYAML(...)` (same signature shape as `writePiProviderJSON`), `ompProviderPresent(path string) bool`, `removeOmpProvider(path string) (bool, error)`, `ompMCPEntry(url string) map[string]any`.

- [ ] **Step 1: Path resolution and ownership-aware detection (D2)**

  `ompPath` mirrors `piPath` (`agents.go:356-367`): `PI_CODING_AGENT_DIR` first, then `~/.omp/agent`. `ompDetect` mirrors `piDetect` plus the ownership rule: the resolved dir must exist and must not be pi-owned (it holds `models.json` but no `models.yml`).

- [ ] **Step 2: YAML provider writer, reader, remover — `yaml.Node` only (R1, R8)**

  `writeOmpProviderYAML`: refuse a pi-owned dir (D2) with an error naming `PI_CODING_AGENT_DIR`; read `models.yml` (a missing file is an empty document); parse into a `yaml.Node`; build the `providers.orq` node by parsing `launch.BuildPiModelsJSON(...)`'s output with `yaml.v3` and navigating to `providers.orq`; splice it in, creating `providers` when absent; encode and write through `writeConfigFile` (backup-once, temp-file rename, 0600). `ompProviderPresent` mirrors `jsonProviderPresentAt` (false when absent or unparseable). `removeOmpProvider`: navigate to `providers.orq`, delete the key, drop `providers` when it empties; when the document is then empty, delete the file only when no `path + ".orq-bak"` exists (we created it); rewrite otherwise. Refuse a malformed file and a non-mapping `providers` value — never overwrite a file we cannot parse.

- [ ] **Step 3: MCP writer pair and the login line (R4)**

  `ompMCPEntry` returns `{"type": "http", "url": url}` (Task 0: `mcp-schema.json:190-225` requires exactly `type` and `url`). Wire `mcpConfig: ompPath("mcp.json")` (global-only, like pi's provider config — the agent dir is the user-level location), `writeMCP: writeMCPJSON("mcpServers", ompMCPEntry)`, `mcpPresent: jsonProviderPresentAt("mcpServers", launch.MCPServerName)`, `removeMCP: removeJSONKeys(p, "mcpServers", launch.MCPServerName)`. Add `mcpLogin string` to `agentSpec`; move the five existing `mcpLoginLine` cases onto their specs; omp's is `"run /mcp in omp"` (Task 0: the `/mcp` slash command is the only login surface; there is no `omp mcp` CLI verb); delete the switch and read the field through `lookupAgent`.

- [ ] **Step 4: Register the `agentSpec`**

  `ID: "omp"`, `Label: "omp"`, `detect: ompDetect`, provider fields from Step 2, MCP fields from Step 3, no otel fields, `providerEmbedsKey: false`.

- [ ] **Step 5: The hard-coded-list audit (R2)**

  Visit every site below; prefer deriving the table from `Agents()`/`agentRegistry()` where the test's purpose is registry completeness, otherwise add the omp row:
  - `cli/custom/commands/setup_test.go:88` — `TestAgentRegistryIsComplete` asserts the exact list `claude, codex, opencode, kimi, kilo, pi`; it fails as soon as the spec registers.
  - `cli/custom/commands/setup.go:1606` — the trust-note condition `slices.Contains(agents, "pi")` stays pi-only: the note documents pi's project-trust gate and omp has none (its `isProjectTrusted` is unconditionally true), so omp must not be named in it — `connect_test.go` asserts exactly that.
  - `cli/custom/commands/agents_test.go:1635` — `provSection` maps agent to config section with no YAML branch; the round-trip test cannot cover omp until it has one.
  - `cli/custom/commands/connect_test.go:2315` — pi capability cases; add omp rows where the table is per-agent (MCP login line, scope awareness).
  - `cli/custom/launch/run_test.go:18,71`, `cli/custom/launch/gateway_test.go:274`, `cli/custom/launch/defaults_test.go:29-30` (`DefaultOmpModel`), `cli/custom/launch/session_skills_test.go:50-51` (sharedReader → real-home path), `cli/custom/skills/skills_test.go:287,295` (shared readers).
  - No change: `setup_test.go:1833` `TestInstrumentAgentsWiresPi` (pi-specific), `launch/invariants_test.go` (iterates the registry), doctor rows that already derive from the registry.

- [ ] **Step 6: Tests**

  Extend `TestConnectDisconnectRoundTripsEveryWriter`, `TestWritersPairWithDetectorsAndRemovers`, and the doctor summary tests with omp rows, plus omp-specific cases: comments and key order survive a merge and a removal; a missing file is created; an existing `providers.orq` is replaced; a pi-owned dir is refused by the writer and invisible to the detector; a malformed `models.yml` is refused by the remover and reads as not-present; an empty-after-removal file with no `.orq-bak` is deleted, and one with a `.orq-bak` is kept. Run `go test ./cli/custom/commands/... -count=1`.

### Task 3: Documentation and changelog

**Files:** Modify: `README.md`, `cli/custom/launch/gateway.go` (package doc), `CHANGELOG.md`

- [ ] **Step 1: README** — add omp to the eight places pi is enumerated (lines 94, 104, 112, 361, 372, 390, 428, 566: supported connect agents, provider registration list, connect-writes table, `orq launch` list, MCP-coverage exception — pi stays the exception, omp wires MCP like kimi/opencode —, `--models` support, the env-override table gains `ORQ_OMP_BASE_URL` and no model-env rows, and the `/v3/router` client list). Read each sentence first; the row must match its table's shape.
- [ ] **Step 2: Launch package doc** — `gateway.go:2` package doc agent list.
- [ ] **Step 3: Changelog** — `**Added:**` entry under `## Unreleased` naming `orq launch omp` and `orq connect omp` with gateway, MCP, and skills support.

### Task 4: Verification and gates

- [ ] **Step 1: End-to-end** (controller, with the real gateway key and the installed `omp` binary): `orq launch omp --dry-run` reports the temp `models.yml` and `mcp.json` with no key material; `orq connect omp` writes `providers.orq` into `~/.omp/agent/models.yml` and the `orq-workspace` entry into `~/.omp/agent/mcp.json`, preserving existing comments; `omp -p "say hi"` reaches the gateway; `orq connect omp mcp` prints the `/mcp` login step; `orq disconnect omp` removes both halves while the user's own providers and servers survive verbatim; `orq launch omp` starts with session skills symlinked from `~/.agents/skills`.
- [ ] **Step 2: Gates** — `go test ./... && go vet ./...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go run ./cmd/surface-dump -check` (expect no delta), `cd packages/orq-rc && go build ./... && go vet ./...`, `go run ./cmd/orq -o json version | jq .`.
