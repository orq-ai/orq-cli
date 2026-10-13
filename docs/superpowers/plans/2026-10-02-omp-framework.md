# omp Framework Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register omp (oh-my-pi) with `orq launch` and `orq connect`: a throwaway `models.yml` gateway provider plus a session MCP entry for launch, a durable merged provider plus MCP wiring for connect, and skills through the shared `~/.agents/skills` directory.

**Architecture:** Launch reuses `BuildPiModelsJSON` through `OmpMergeProvider` to add orq to a session copy of the user's `models.yml` (or legacy `models.json` if YAML is absent), with a bare `ORQ_API_KEY` reference. It symlinks the user's other agent-directory entries into the temporary directory and adds the orq MCP entry through a session `.mcp.json`; the user's `mcp.json` remains linked. Connect uses the same `yaml.Node` merge to preserve other providers, comments and key order.

**Tech Stack:** Go 1.25, `go.yaml.in/yaml/v3` (already a direct dependency), table-driven Go tests.

**Spec:** [Linear RES-1698](https://linear.app/orqai/issue/RES-1698/add-omp-oh-my-pi-as-a-launch-and-connect-framework)

## Decisions taken (from the adversarial review of the first draft)

- **D1 — Launch adds orq to the user's own agent dir.** The session owns `models.yml` and, when MCP is enabled, `.mcp.json`; other entries are symlinked from the user's dir. Before linking, launch creates persistent `sessions/` and an empty `agent.db` if missing, without truncating an existing database. First logins and conversations survive cleanup, including on fresh installations. A redirected pi-owned dir contributes nothing. If a symlink fails, discard every partial link and user-derived session file, rebuild an isolated orq-only config, and warn that user config, logins and history are unavailable for that launch.
- **D2 — `PI_CODING_AGENT_DIR` ownership.** The default `~/.omp/agent` belongs to omp, including before JSON-to-YAML migration; launch and connect preserve its legacy providers when adding orq. Outside the default location, a dir holding `models.json` but no `models.yml` is treated as pi-owned: detection skips it and both persistent writers refuse it. A missing redirected dir remains omp-writable. A fresh pi dir without `models.json` remains indistinguishable from an empty dir.
- **D3 — omp-local `yaml.Node` helpers.** The JSON helpers (`readJSONConfig`, `removeJSONKeys`, …) stay as they are; the YAML helpers live in `cli/custom/launch/omp_yaml.go`, shared by launch's session merge and connect's writer and remover in `cli/custom/commands/omp.go`. Parametrising the JSON helpers over a codec would touch ~6 shipped callers for reuse the node-based approach cannot deliver anyway. Cost: two parallel helper families.
- **No second models builder.** `BuildPiModelsJSON(baseURL, apiKeyRef, models, infos)` is shared: pi supplies `"$ORQ_API_KEY"` and omp supplies bare `ORQ_API_KEY`. Both launch and connect parse its JSON output with `yaml.v3`, extract `providers.orq`, normalize block styling, and splice it into the user's YAML with `OmpMergeProvider`. There is no post-generation key patch. Bun's YAML parser accepts the result.
- **Profile warning (subsequent review D1).** If `OMP_PROFILE` or `PI_PROFILE` is nonempty, launch warns, clears both for the child, and hints at `orq connect omp` for more flexible persistent wiring. Named-profile directory resolution is not implemented; the hint explicitly notes that their own directory needs wiring.

## Global Constraints

- Do not edit `cli/generated/`. Changes under `cli/custom/` must compile for both the root stable module and `packages/orq-rc`.
- No new dependencies: `go.yaml.in/yaml/v3` is already direct (`go.mod:17`).
- No credential ever lands in a config file: the provider block carries `apiKey: ORQ_API_KEY`, and the MCP entry carries no headers. omp resolves an `apiKey` as an `!command` or a **bare** environment-variable name and sends anything else verbatim (`src/config/resolve-config-value.ts:104-107`, `$envExact(value) || value`), so pi's `"$ORQ_API_KEY"` reached the router as the literal token and every request 401'd — the first draft of this plan misread that function.
- omp honors `PI_CODING_AGENT_DIR` in default-profile mode; named profiles ignore it. Connect resolves the env override, then `~/.omp/agent`, without automatic named-profile resolution. Launch detects profile variables, warns, clears them for the child, and hints at persistent connect wiring.
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
- [x] `models.yml` schema and env resolution of a bare `ORQ_API_KEY` (`models-config-schema-bundle.ts`, `resolve-config-value.ts:104-107`; `"$ORQ_API_KEY"` is not resolved — checked with a live `bun -e` call).
- [x] JSON content in a `.yml` parses (Bun `YAML.parse`, live run over the exact `BuildPiModelsJSON` shape).
- [x] `mcp.json` `mcpServers` + `{type:"http", url}` (`mcp-schema.json:190-225`, `discovery/builtin.ts:219-224`).
- [x] Skills from `.agents/skills` walk-up + home (`discovery/agents.ts:177`).
- [x] `/mcp` interactive command exists (`slash-commands/helpers/mcp.ts`); no `omp mcp` CLI verb.
- [x] No `PI_MODEL`/`PI_MODELS`, no `OMP_AGENT_DIR`; `--provider` and `provider/modelId` selectors accepted.

### Task 1: Launch support (`orq launch omp`)

**Files:**
- Create: `cli/custom/launch/omp.go`, `cli/custom/launch/omp_yaml.go`, `cli/custom/launch/omp_test.go`
- Modify: `cli/custom/launch/agents.go` (register `ompAgent()` in `Agents()`), `cli/custom/launch/mcp.go` and `cli/custom/launch/pi.go` (update the two comments that say pi has no MCP support), `cli/custom/skills/targets.go` (add `"omp": true` to `sharedReaders`)

**Interfaces:**
- Consumes: `ResolveGatewayConfig`/`ResolveInput`, `ModelInfo`, `ResponsesModelSet`, `BuildPiModelsJSON`, `OmpMergeProvider`, `mcpURL`, `maybeInstallSessionSkills`, `appendModelWarnings`, `appendCapWarning`.
- Produces: `DefaultOmpModel`, `OmpProvider`, `ompAgent() AgentDef`, `resolveOmp(*AgentContext) (*LaunchPlan, error)`, `writeOmpConfigDir`, `ompSessionMCP`, `linkOmpAgentDir`, `prepareOmpState`, shared `HTTPMCPEntry`, `OmpAgentDir(getenv, home)` and `OmpPiOwned(dir, home)`.

- [ ] **Step 1: Write failing tests**

  - `writeOmpConfigDir`: writes `models.yml` (0600) whose content is byte-identical to the input, into a fresh `os.MkdirTemp("", "orq-omp-")`; cleanup removes it.
  - `resolveOmp`: exercise the session merge and symlink overlay, keeping user providers and MCP servers while adding orq. Verify the provider names bare `ORQ_API_KEY` without embedding a credential. With `--no-mcp`, no session `.mcp.json` is added; otherwise it holds the orq entry while the user's existing entry wins. Verify newly created login and conversation state survives cleanup and a second launch. A legacy default omp directory remains detected and its providers retained; a redirected pi-owned directory is not overlaid.
  - With a profile variable set, exercise the warning, connect hint and default-profile child environment. Shared-reader skills remain outside the temporary config directory.
  - Simulate a symlink failure after a successful link and verify complete isolation, warning and cleanup without changing user files. Empty, malformed, null and non-object `.mcp.json` inputs must not block launch or modify their source. Inject the home directory so tests cannot overlay real user state.

- [ ] **Step 2: Implement `writeOmpConfigDir`**

  `writeOmpConfigDir` writes the merged YAML string to session `models.yml` at 0600. Cleanup removes only the temporary directory. `ompSessionMCP` uses `HTTPMCPEntry` to add the headerless HTTP entry to a copy of the user's `.mcp.json`, preserving a same-named entry; malformed/non-object documents are treated as absent, while filesystem read errors are returned.

- [ ] **Step 3: Implement `resolveOmp` and the `AgentDef`**

  Register `AgentDef{Name: "omp", Binary: "omp", Label: "oh-my-pi (omp)", FetchesModels: true, AllowModels: true, Resolve: resolveOmp}` with pi's prompt mapping. Resolve gateway models using `ORQ_OMP_BASE_URL`, no model environment overrides. Pass `OmpAPIKeyRef` into `BuildPiModelsJSON`, then merge through `OmpMergeProvider`. Use shared `OmpAgentDir` with an injected/default home; create missing persistent state and link entries except session-owned config. On symlink failure, rebuild only orq's config and warn; directory-read failures remain errors. Set gateway credentials and temporary `PI_CODING_AGENT_DIR` in the plan. If either profile variable is nonempty, warn, clear both in the child, and hint at persistent `orq connect omp` wiring. Append session skills and model/cap warnings.

- [ ] **Step 4: Register and run focused tests**

  Add `ompAgent()` to `Agents()`. pi remains the one agent with no built-in MCP support. omp adds its session entry in `.mcp.json`, behind the user's linked `mcp.json`; first-name-wins preserves a persisted entry. Run `go test ./cli/custom/launch/... ./cli/custom/skills/... -count=1`; the registry-wide credential and server invariants must pass with the new agent.

### Task 2: Connect support (`orq connect omp` / `orq disconnect omp`)

**Files:**
- Create: `cli/custom/commands/omp.go`, `cli/custom/commands/omp_test.go`
- Modify: `cli/custom/commands/agents.go` (register the `omp` `agentSpec`; add the `mcpLogin` field to `agentSpec`) and `cli/custom/commands/connect.go` (replace the `mcpLoginLine` switch with the spec field), plus the test files in the Step 5 audit list. `setup.go` stays untouched: its trust note is pi-only (omp has no project-trust gate), which the Step 5 audit below asserts.

**Interfaces:**
- Consumes: `writeConfigFile`, `jsonProviderPresentAt`, `removeJSONKeys`, `writeMCPJSON`, `launch.BuildPiModelsJSON`, `launchCatalog`, `launch.MCPServerName`, `launch.HTTPMCPEntry`, `launch.OmpAgentDir` and `launch.OmpPiOwned`.
- Produces: `ompPath`, `ompDetect`, `refusePiOwnedDir`, `writeOmpProviderYAML`, `ompProviderPresent`, `removeOmpProvider` and `writeOmpMCP`.

- [ ] **Step 1: Path resolution and ownership-aware detection (D2)**

  Call shared `OmpAgentDir(os.Getenv, home)` for directory resolution; detection and both writers use the same pi-owned refusal helper. The default directory is always omp's, including before JSON-to-YAML migration. Outside it, retain the `models.json` without `models.yml` guard. The YAML writer and remover both use 0600 directly, not the MCP file-mode policy.

- [ ] **Step 2: YAML provider writer, reader, remover — `yaml.Node` only (R1, R8)**

  `writeOmpProviderYAML` refuses redirected pi-owned dirs, then uses `OmpMergeProvider`: read `models.yml`, or legacy `models.json` when YAML is absent, and splice the generated `providers.orq` with its bare `ORQ_API_KEY` reference. Write through `writeConfigFile` (backup-once, temp-file rename, 0600). `ompProviderPresent` returns false when absent or unparseable. `removeOmpProvider` deletes only orq, preserving other providers; remove an empty file only when there is no `.orq-bak`, rewrite otherwise. Refuse malformed files and non-mapping providers rather than overwriting them.

- [ ] **Step 3: MCP writer pair and the login line (R4)**

  Use `launch.HTTPMCPEntry(url)` for the headerless `{type: "http", url}` shape shared by launch and connect. Wire `mcpConfig: ompPath("mcp.json")` (global-only), `writeMCP: writeOmpMCP` (shared ownership guard, then `writeMCPJSON`), presence/removal at `mcpServers.orq-workspace`. The login instruction comes from `agentSpec.mcpLogin`: `"run /mcp in omp"`.

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

- [ ] **Step 1: End-to-end**: launch real omp with a fresh redirected agent directory and profile variables set. Observe the warning and connect hint, a successful model response, persistent database and session files after cleanup, and retained state on a second launch. Verify connect/disconnect in a legacy default omp directory keeps other providers and the source JSON unchanged; redirected pi-owned directories remain refused.
- [ ] **Step 2: Gates** — `go test ./... && go vet ./...`, `test -z "$(gofmt -l $(git ls-files '*.go'))"`, `go run ./cmd/surface-dump -check` (expect no delta), `cd packages/orq-rc && go build ./... && go vet ./...`, `go run ./cmd/orq -o json version | jq .`.
