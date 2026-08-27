# Implementation plan: MCP wiring for coding agents

Spec: `docs/superpowers/specs/2026-08-25-orq-cli-mcp-wiring-design.md` (RES-1435). The spec is
the binding authority; this plan is its argument. Read the spec section named by each task.

Repo: Go CLI under `cli/`. Module `orq`. Tests run with `go test ./cli/...`.

## Global constraints

1. **No credential ever reaches an MCP entry.** `writeMCP(path, url string) error` takes no
   credential, by design — that signature is the guarantee. No config this branch writes may
   contain `Authorization`, `headers`, `bearer_token_env_var`, `bearerTokenEnvVar`, or any
   substring of a stored API key. This holds for `connect` **and** `launch`.
2. **Server name is `orq-workspace`** — `launch.MCPServerName`. Never a second name.
3. **URL resolution is not duplicated.** `connect` calls the exported resolver in `launch`;
   it does not re-derive the URL. Order stays `ORQ_MCP_URL` → `deriveFromAPIBase(apiBase,
   "/v2/mcp")` → `launch.DefaultMCPURL`.
4. **Reads probe both scopes; writes take the chosen scope.** `wiredPath` (agents.go) and
   `bothScopePaths` (connect.go) already implement the read half — extend, do not replace.
5. **Never `panic`, never `os.Exit`** in these paths; errors are returned and reported through
   the existing `reporter`.
6. **Match the surrounding code.** Same comment density (these files explain *why*, not
   *what*), same naming, same error-wrapping style. New exported/unexported helpers mirror
   the existing provider-side names.
7. Every task ends with `go build ./...` and `go test ./cli/...` green, and one commit.

## Task 1 — MCP entry writers in the agent registry

Files: `cli/custom/commands/agents.go`, new `cli/custom/commands/agents_mcp_test.go`.
Spec section 2, plus the "Verified" section for the exact paths and shapes.

Add four fields to `agentSpec`, mirroring the provider quartet directly above them:

```go
// mcpConfig returns the file holding the agent's MCP servers; nil when the agent has no MCP support.
mcpConfig func(global bool) (string, error)
// writeMCP writes the orq-workspace entry. No credential argument, by design: every
// MCP-capable agent authenticates by OAuth, and a signature that cannot carry a secret is
// what keeps e44c747's regression from returning.
writeMCP func(path, url string) error
// mcpPresent is writeMCP's read-side pair; required whenever writeMCP is set.
mcpPresent func(path string) bool
// removeMCP is writeMCP's inverse; required whenever writeMCP is set.
removeMCP func(path string) (bool, error)
```

Add a two-scope path resolver next to `alwaysGlobalPath`:

```go
// projectOrGlobalPath resolves the project copy for global=false and the home copy for
// global=true. Only claude and kimi have two scopes; everything else keeps alwaysGlobalPath.
func projectOrGlobalPath(projectRel, globalRel string) func(bool) (string, error)
```

For `global=false` it returns `filepath.Join(cwd, projectRel)`. For `global=true`,
`filepath.Join(home, globalRel)`.

Registry wiring, exactly these paths and shapes (all verified against the shipped products):

| Agent | mcpConfig | Format |
| --- | --- | --- |
| claude | `projectOrGlobalPath(".mcp.json", ".claude.json")` | JSON, key `mcpServers`, entry `{"type": "http", "url": "…"}` |
| codex | `codexPath("config.toml")` | TOML table `[mcp_servers.orq-workspace]` with `url` only |
| opencode | `alwaysGlobalPath(".config/opencode/opencode.json")` | JSON, key `mcp`, entry `{"type": "remote", "url": "…", "enabled": true}` |
| kilo | `alwaysGlobalPath(".config/kilo/kilo.json")` | same as opencode |
| kimi | `projectOrGlobalPath(".kimi-code/mcp.json", ".kimi-code/mcp.json")` | JSON, key `mcpServers`, entry `{"url": "…"}` |
| pi | none — leave all four fields nil | — |

Note codex writes into the **base** `config.toml`, not the `orq.config.toml` profile: MCP
servers are not profile-scoped in codex, and `writeCodexProviderTOML` owns the profile file
outright and rewrites it wholesale, so an entry placed there would be destroyed on the next
`orq connect gateway`. Merge into the base file, preserving everything else in it.

Implementation notes:

- JSON writers: one parametrised helper (`writeMCPJSON(key string, entry func(url string)
  map[string]any)`) rather than three near-copies, matching how `jsonProviderPresentAt` is
  parametrised over its key. Assign the whole `orq-workspace` value rather than merging into
  it, so a v4.13.10 leftover carrying `"Authorization": "Bearer {env:ORQ_API_KEY}"` is
  upgraded to the headerless shape in place.
- Readers: reuse `jsonProviderPresentAt("mcpServers"/"mcp", launch.MCPServerName)` and
  `tomlTablePresent("mcp_servers." + launch.MCPServerName)`.
- Removers: reuse `removeJSONKeys(path, key, launch.MCPServerName)`; write a TOML remover for
  codex that drops only the `[mcp_servers.orq-workspace]` table and leaves the rest byte-identical.
- Kilo's reader must accept `kilo.jsonc` as well as `kilo.json` — the shipped binary
  references both. Writes go to `.json`.
- File mode: `writeJSONConfig` chmods `0600`. A project `./.mcp.json` is meant to be committed,
  and Claude itself creates it `0644`. Give the JSON writer a mode parameter (or preserve an
  existing file's mode) so the project write does not narrow a shared file. `~/.claude.json`
  is already `0600`, so the global path is unchanged.

Tests (`agents_mcp_test.go`), all against `t.TempDir()` with `HOME` overridden:

- Round-trip write → present → remove, per format (JSON entry, codex TOML).
- Idempotence: writing twice yields one entry, byte-identical.
- Foreign content preserved: unrelated JSON keys and unrelated TOML tables survive write and remove.
- Upgrade in place: a pre-existing `orq-workspace` entry carrying an `Authorization` header is
  replaced by the headerless shape.
- **Credential canary:** for every agent with `writeMCP`, write an entry and assert the file
  contains none of `Authorization`, `headers`, `bearer_token_env_var`, `bearerTokenEnvVar`.
- codex: the entry lands in `config.toml`, not `orq.config.toml`, and survives a subsequent
  `writeCodexProviderTOML` call.

## Task 2 — MCP as a connect capability, and the scope flags

Files: `cli/custom/commands/connect.go`, `cli/custom/commands/setup.go` (the `setupOptions`
struct only), `cli/custom/commands/connect_test.go`. Spec sections 1, 3 and 6.

Add `capMCP = "mcp"` to the const block and to `connectCapabilities`, and make
`availableCapabilities()` return `{capGateway, capSkills, capMCP}`.

Every capability-dispatching function needs an MCP clause. Work through this list — it is
exhaustive as of this commit:

| Function | Change |
| --- | --- |
| `agentsToConnect` (91) | select an agent when `spec.writeMCP != nil` and mcp is among caps. Without this a bare `orq connect mcp` selects zero agents on a claude-only machine, since claude has no `writeProvider`. |
| `agentsToInspect` (115) | unchanged apart from following `agentsToConnect` |
| `reportUnwirableAgents` (239) | today returns early unless gateway is in caps; must also report pi as unable to receive mcp |
| `credentialFreeCaps` / `capsNeedCredential` (175/185) | mcp is credential-free, like skills |
| `runConnectStatus` (307) | report the MCP entry per agent, naming the scope it was found in |
| `connectSelected` (433) | write the entry, then print section 6's login line per agent |
| `dryRunConnect` (562) | print the path it would write. Paths only — that is the function's stated contract |
| `wiredTargets` (775) | list MCP paths for disconnect |
| `removeWiring` (890) | remove the entry from **both** scopes |
| `disconnectOnLogout` (959) | `caps` is hardcoded to `{capGateway}`; make it `availableCapabilities()` |

Scope flags on `orq connect` and `orq disconnect`:

```go
f.BoolVar(&opts.scopeGlobal, "global", false, "Write to the machine-wide config (the default)")
f.BoolVar(&opts.scopeLocal, "local", false, "Write to this project only")
```

Add both fields to `setupOptions`. Both set is an error. Neither set means global. The pair is
mutually exclusive with a clear message rather than a silent precedence rule. `--local` against
a global-only agent (codex, opencode, kilo) warns through the reporter and writes the global
path — it does not fail the run. `--local` when the working directory is `$HOME` is **refused**
with the reason: a `~/.mcp.json` is not project config and Claude will not read it.

`--local` on a gateway-only run warns that gateway is machine-wide, and proceeds.

RES-1437 (skills `--local`/`--global`) will consume these same flags. Declare them once here;
do not add a second pair.

Also update both commands' `Long` help to describe the `mcp` capability, in the voice of the
existing `gateway` and `skills` entries.

Tests: bare `orq connect mcp` on a claude-only machine selects claude; `orq connect pi mcp`
reports unsupported and writes nothing; `--local` writes `./.mcp.json` and leaves the home
config untouched; `--global` the inverse; `--local` + `--global` together errors; disconnect
with no scope flag removes from both; `--status` reports an entry found in either scope.

## Task 3 — The setup wizard

Files: `cli/custom/commands/setup.go`, `cli/custom/commands/setup_test.go`. Spec section 3.

`promptForCapabilities` builds its options from `availableCapabilities()`, so `mcp` appears
once Task 2 lands. It needs a label in the same `%-9s` padded style:

```go
capMCP: fmt.Sprintf("%-9s give the agent orq's MCP tools (the agent logs in itself)", capMCP),
```

Add the scope question as a **second** prompt, immediately after the capability multi-select,
in the same style and routed through `promptStdio()` — which `promptForCapabilities` currently
omits and should gain at the same time:

```
Where should MCP and skills go?
  > global    every project on this machine
    local     this project only
```

One question for both scope-capable capabilities, not one each. Asked only when `mcp` or
`skills` is among the chosen capabilities, the run is interactive, and a scope-capable agent is
detected. `--global`, `--local`, `--yes` and `--no-input` all pre-answer it. **Default global** —
every other artifact `orq setup` writes is machine-global, and `install.sh` runs setup
non-interactively from wherever the user happens to be.

There is no scope inference. The answer comes from the prompt or the flag.

Tests: the multi-select offers mcp; a non-interactive run defaults to global and never prompts;
`--local` pre-answers and suppresses the prompt; the prompt is skipped when neither mcp nor
skills is chosen.

## Task 4 — `orq launch` stops embedding a credential, and no-ops when already wired

Files: `cli/custom/launch/mcp.go` and its siblings, `cli/custom/launch/*_test.go`. Spec section 4.

1. **Drop the credential from every launch MCP writer.** Remove the `ORQ_API_KEY` reference
   from `claudeMCPConfig` (`mcp.go:124`), `codexMCPArgs` (`:149`), `openCodeMCPBlock` (`:161`)
   and `kimiMCPConfig` (`:236`). A bare URL is the correct entry in all four formats. This is
   not a regression: it replaces a credential the MCP server is expected to reject —
   `launch/auth.go:51` records that the key `orq setup` mints does not carry `mcp_gateway` — with
   one the agent can actually obtain by OAuth.
2. **No-op when the agent is already wired.** Before injecting a session entry, check the
   persisted config through the Task 1 reader (both scopes); if the entry is there, inject
   nothing and let the persisted entry serve. Correct for **claude** (`--mcp-config` temp file,
   `claude.go:82`), **codex** (`-c` overrides, `mcp.go:148`), and the **opencode family**
   (`OPENCODE_CONFIG_CONTENT` / `KILO_CONFIG_CONTENT` merge over the persisted file — verified:
   `opencode mcp list` reported 5 servers with 4 persisted and 1 injected).
   **Not** correct for **kimi**: launch points `KIMI_CODE_HOME` at a fresh temp dir
   (`kimi.go:75`), so the persisted file is not on kimi's search path and launch must keep
   writing there — now headerless.
3. Export the URL resolver so `connect` can call it (global constraint 3).

Tests: the launch credential canary, per agent; with a persisted entry present,
`orq launch claude --mcp` and `orq launch codex --mcp` inject nothing; kimi still injects;
URL derivation for a non-default `ORQ_API_BASE_URL`.

## Task 5 — Doctor

Files: `cli/custom/commands/doctor.go`, `cli/custom/commands/doctor_test.go`. Spec section 5.

Add an `mcpCheck()` group modelled on the existing `skillsCheck()` — same shape, `pass`/`warn`,
naming the command that fixes it. Reads **both** scopes.

The wording constraint is load-bearing: `pass` says **"entry present"**, never "MCP works". The
CLI writes no credential and holds no token, so it cannot know whether the user ever completed
the OAuth flow in that agent. The `pass` message names the login command, so the check is also
the instruction:

| Agent | Login command |
| --- | --- |
| claude | `run /mcp in Claude Code, or 'claude mcp login orq-workspace'` |
| codex | `run 'codex mcp login orq-workspace'` |
| opencode | `run 'opencode mcp auth orq-workspace'` |
| kilo | `run 'kilo mcp auth orq-workspace'` |
| kimi | `run 'kimi mcp auth orq-workspace'` |

`warn` when an MCP-capable agent is detected with no entry, naming `orq connect <agent> mcp`.
Never a non-zero exit — an unwired agent is an offer, not a breakage (RES-1270). Pi is not
reported at all. No expiry, renewal or credential-drift checks: there is no credential.

Test: `pass` asserts on the literal "entry present" wording, so a later edit cannot quietly
promote it to a health claim.
