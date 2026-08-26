# orq CLI: MCP wiring for coding agents

Date: 2026-08-25
Status: verified design, ready for an implementation plan
Ticket: RES-1435
Branch base: `Baukebrenninkmeijer/cli-mcp-support-pr` (on `origin/main`). Section 1 assumes
`arianpasquali/skills-safety-fixes` (`69e05ba`) has landed, since that is what makes
`skills` a live capability.

## Problem

`orq connect` and `orq setup` wire coding agents to the orq gateway, but not to the
orq MCP server. A user who wants orq's tools in Claude Code or Codex has to write the
MCP entry by hand into a per-agent config file whose format differs for every agent.

MCP wiring used to exist. It was removed in `e44c747` ("gateway-only wiring with
least-privilege keys"), because the key it wrote into agent configs was minted with
`permission_mode: "all"` — a leaked agent config was a workspace-admin credential. The
commit removed MCP wholesale rather than narrow it, and left entries written by
v4.13.10 in place rather than deleting them.

This design re-adds MCP wiring without writing a credential at all.

## Why that is now possible

`https://api.orq.ai/v2/mcp` answers an unauthenticated request with a complete
RFC 9728 discovery chain:

```
401
www-authenticate: Bearer error="invalid_token",
  resource_metadata="https://api.orq.ai/.well-known/oauth-protected-resource/v2/mcp"
```

The protected-resource metadata names `https://my.orq.ai/v2/auth/mcp` as its
authorization server with scopes `mcp:tools` and `mcp:resources`. That server's
metadata advertises `authorization_endpoint`, `token_endpoint`, a
`registration_endpoint` (dynamic client registration), `code_challenge_methods_supported: ["S256"]`,
and both `authorization_code` and `refresh_token` grants.

**Every agent in the registry that speaks MCP at all can use it.** Surveyed
2026-08-25, live where the agent is installed on this machine:

| Agent | MCP OAuth | Evidence |
| --- | --- | --- |
| Claude Code 2.1.245 | yes | `claude mcp login <name>`, `--client-id` / `--callback-port` flags |
| Codex 0.144.6 | yes | `codex mcp login orq-workspace` produced a live PKCE authorize URL against `my.orq.ai`; `codex mcp list` reports a per-server `Auth` column |
| opencode 1.17.20 | yes | installed SDK types: `McpRemoteConfig.oauth?: McpOAuthConfig \| false`, DCR when `clientId` is absent; `needs_auth` / `needs_client_registration` statuses |
| Kilo Code | yes | same `mcp.<name>` schema as opencode; docs state OAuth starts automatically. Not installed here — see gate G1 |
| Kimi Code | yes | `kimi mcp add --transport http --auth oauth`, then `kimi mcp auth <name>`. Not installed here — see gate G1 |
| Pi | **no MCP at all** | grep of the installed `@earendil-works/pi-coding-agent` 0.84.2 bundle finds no MCP code; extensibility is proprietary "extensions" |

So there is exactly **one** mode: write the URL, write no credential. The
header/bearer fallback an earlier draft of this design carried — a second API key
scoped to `mcp_gateway`, stored as `mcp_key`, revoked on disconnect — is deleted. It
had no users, and it could not have worked anyway: `createAPIKeyBody`
(`cli/custom/auth/projects.go:162`) always sends `"source": "router"`, and its own
comment records that identity-api *"never reads permission_mode or access — it
discards both and applies the catalog's router preset"*. A key minted with
`{"mcp_gateway": "write"}` would have received the router preset, which
`launch/auth.go:51` says does not carry `mcp_gateway`.

Also relevant to Codex: `experimental_use_rmcp_client` is **obsolete**. The flag was
removed in openai/codex PR #8087; the OAuth-capable client is now the default stack. A
remote HTTP entry with `url` and nothing else is the OAuth shape.

## Design

### 1. MCP is a connect capability

`cli/custom/commands/connect.go` models capabilities as `capGateway`, `capTracing`,
`capSkills` in `connectCapabilities`, with `availableCapabilities()` gating which are
actually built. Add `capMCP = "mcp"` to both. As of `69e05ba` on this branch's base,
`availableCapabilities()` returns `{capGateway, capSkills}`, so after this change a
bare `orq connect` writes **gateway + skills + mcp**.

| Command | Behaviour |
| --- | --- |
| `orq connect` | writes gateway + skills + mcp for detected agents |
| `orq connect codex mcp` | writes only the MCP entry, only for codex |
| `orq disconnect claude mcp` | removes only Claude's MCP entry |
| `orq connect --status` | reports MCP wiring alongside gateway |
| `orq connect --dry-run` | reports the file path it would write (paths only, per `dryRunConnect`'s existing contract) |

Adding the constant is not the work. Every capability-dispatching function in
`connect.go` is a chain of `if hasCap(caps, capGateway)` / `hasCap(caps, capSkills)`
branches and needs a `capMCP` clause: `agentsToConnect` (line 92), `agentsToInspect`
(line 116), `dryRunConnect` (563), `wiredTargets` (776), `removeWiring` (891),
`reportUnwirableAgents` (240), plus `disconnectOnLogout` (958), whose capability list
is hardcoded to `{capGateway}`. `agentsToConnect` matters most: it selects agents on
`spec.writeProvider != nil`, and claude has no provider config, so without an MCP
clause a bare `orq connect mcp` would select zero agents on the most common machine
there is.

`orq setup` needs no new *capability* question — `promptForCapabilities`
(`setup.go:1340`) builds its multi-select from `availableCapabilities()`, so `mcp`
appears there by default like the others. It does gain one new question, in section 3.

### 2. The agent registry gains four MCP fields

Restore the fields deleted in `e44c747`, mirroring the existing provider naming:

| Field | Mirrors |
| --- | --- |
| `mcpConfig func(global bool) (string, error)` | `providerConfig` |
| `writeMCP func(path, url string) error` | `writeProvider` |
| `mcpPresent func(path string) bool` | `providerPresent` |
| `removeMCP func(path string) (bool, error)` | `removeProvider` |

There is no `mcpAuth` field and no credential analogue of `providerEmbedsKey` /
`providerKey`, because there is no second mode and no credential. `writeMCP` takes a
URL and nothing else — a signature that cannot embed a secret is the structural
guarantee that replaces `e44c747`'s hand-checking.

`manualSnippet` is **not** restored. It was only ever called from `setup.go`, and
`dryRunConnect`'s comment is explicit — *"Paths only, not content: the writers resolve
content against the live catalogue"* — so rendering MCP content in dry-run alone would
make one command behave two ways.

Per-agent entry shapes, all verified against the shipped product (see "Verified"):

- **claude** — `./.mcp.json` (project) or `~/.claude.json` (global), key `mcpServers`:
  `{"type": "http", "url": "…"}`
- **codex** — `~/.codex/config.toml`: `[mcp_servers.orq-workspace]` with `url` only
- **opencode** — `~/.config/opencode/opencode.json`, key `mcp`:
  `{"type": "remote", "url": "…", "enabled": true}`
- **kilo** — `~/.config/kilo/kilo.json`, same shape as opencode
- **kimi** — `./.kimi-code/mcp.json` (project) or `~/.kimi-code/mcp.json` (global),
  key `mcpServers`: `{"url": "…"}`
- **pi** — no MCP entry. `detect()` still finds pi for the gateway; MCP reports
  "not supported by this agent", the same way claude reports no gateway config today

**Two agents have two scopes, not one:** claude and kimi. Everything in section 3 that
says "today: claude" covers kimi as well.

No manifest. Unlike skills — where a symlink on disk cannot say who created it, which
is why `skills.LoadManifest()` exists — an MCP entry is self-describing: it is the
value at `orq-workspace` in a known file. `mcpPresent` reading that key is the whole
state model.

### 3. Scope: a wizard step and two flags

`--global` and `--local` return to `orq connect` and `orq disconnect`. RES-1437 adds
the same two flags for the skills capability on the same two commands, so whichever
lands first owns the flag definitions and the other consumes them; they must not be
declared twice. Scope is meaningful for `mcp` and `skills`; naming one on a
gateway-only run warns rather than silently doing nothing.

Only Claude Code has two scopes: `./.mcp.json` (project) and `~/.claude.json` (global).
Codex, opencode, kilo and kimi read MCP config from a fixed location, so `--local`
against them warns and writes the global path.

**Gateway is global-only, verified.** Every provider resolver in the registry is
home- or env-rooted with no project variant: `alwaysGlobalPath` (opencode, kilo, kimi),
`codexPath` (`$CODEX_HOME`, else `~/.codex`), `piPath` (`$PI_CODING_AGENT_DIR`, else
`~/.pi/agent`), and claude has no provider config at all. The `alwaysGlobalPath` call
sites carry the reason: opencode and kilo *"reject `{env:...}` references in a project
config"*, and the rest read only from their own home directory. So `--local` with
gateway warns; it is not a gap to close later.

**Every path that must understand scope**, not just the two that take the flag:

| Path | Requirement |
| --- | --- |
| `orq setup` | wizard step + the flags, defaulting global |
| `orq connect` | the flags; writes the chosen scope only |
| `orq disconnect` | with no flag, removes from **both** scopes — otherwise a project-scoped wire becomes unremovable without the user knowing which scope it landed in |
| `orq connect --status` | reports both scopes, and says which |
| `orq doctor` | reads both scopes. `edf338b` already fixed exactly this class of bug once ("doctor was blind to project-scoped wiring") |
| `orq launch` | the section-4 no-op check reads both scopes; a project `./.mcp.json` counts as wired |
| `disconnectOnLogout` (`connect.go:958`) | its capability list is hardcoded to `{capGateway}` and must grow `capMCP` (and `capSkills`), each removed from both scopes |
| `install.sh` → `orq setup` | non-interactive, so it takes the global default and never prompts |

The read/write asymmetry is the rule: **writes take the chosen scope, reads probe
both.** `wiredPath` (`agents.go:652`) and `bothScopePaths` (`connect.go:1006`) already
implement the read half.

`--local` from a directory that is not a project — most obviously `$HOME`, where it
would produce a `~/.mcp.json` that Claude does not read as project config — is refused
with the reason, not silently written.

The two-scope resolver is **new code**. `alwaysGlobalPath` exists (`agents.go:144`) but
`pathFor(project, global)` does not — it went out with `e44c747` and an earlier draft
of this spec wrongly claimed it was still there. Restoring it also means deciding how
it interacts with `wiredPath` (`agents.go:652`) and `bothScopePaths`
(`connect.go:1006`), which today probe *both* scopes unconditionally: for reads
(`--status`, doctor, disconnect) probing both is correct and should stay, since the
user may have wired either; only the *write* path takes the chosen scope.

**New wizard step.** This is a second question in `orq setup`, asked immediately after
the capability multi-select, in the same style (`survey.Select`, same label padding,
routed through `promptStdio()` — which `promptForCapabilities` currently omits and may
as well gain):

```
Where should MCP and skills go?
  > global    every project on this machine
    local     this project only
```

One question for both scope-capable capabilities, not one each — the answer is the
same kind of answer, and asking twice in a four-question wizard buys nothing. Asked
only when `mcp` or `skills` is among the chosen capabilities, the run is interactive,
and a scope-capable agent is detected. `--global` / `--local` / `--yes` / `--no-input`
all pre-answer it. Gateway is unaffected either way.

**Default is global**, changed from the pre-`e44c747` behaviour, which defaulted to the
cwd. Every other artifact `orq setup` writes is machine-global — `~/.orq/env`, the
shell profile line, every provider config — and `orq setup` is documented as getting "a
new machine from zero to working". A `curl | sh` install run from `$HOME` that silently
produces `~/.mcp.json`, or a run inside one repo that gives that repo MCP tools and no
other, is a footgun the old default carried because nothing then had two scopes.

There is no scope *inference*. The answer comes from the prompt or the flag.

### 4. One entry writer, and `orq launch` stops embedding a credential

`cli/custom/launch/mcp.go` already writes MCP entries for claude, codex, opencode,
kilo and kimi, under the same `MCPServerName = "orq-workspace"` this design uses, and
every one of them embeds a bearer credential: `Authorization: Bearer ${ORQ_API_KEY}`
(`mcp.go:124`), `bearer_token_env_var = "ORQ_API_KEY"` (`:149`),
`Bearer {env:ORQ_API_KEY}` (`:161`), `"bearerTokenEnvVar": "ORQ_API_KEY"` (`:236`).

Left alone, that defeats this design entirely. The `MCPServerName` comment says a
session entry deliberately **shadows** the persisted one — so the first
`orq launch claude --mcp` after `orq connect claude mcp` replaces the headerless OAuth
entry with a bearer entry carrying the gateway key, which is exactly the
credential-in-agent-config pattern `e44c747` deleted. Worse, that key almost certainly
does not work: `launch/auth.go:51` records that the key `orq setup` mints does not
carry `mcp_gateway`, so `SupportsMCP()` is "optimistic" — launch is wiring a credential
that the MCP server is expected to reject.

Two changes, both in `launch`:

1. **Drop the credential from every launch MCP writer.** All five agents do OAuth; a
   bare URL is the correct entry in every one of their formats. This deletes the
   `ORQ_API_KEY` reference from `claudeMCPConfig`, `codexMCPArgs`, `openCodeMCPBlock`
   and `kimiMCPConfig`. It is not a regression: it replaces a credential the server is
   expected to reject with one the agent can actually obtain.
2. **No-op when the agent is already wired.** Before injecting a session entry, check
   `mcpPresent` on that agent's persisted config; if the entry is there, write nothing
   and let the persisted one serve. This is only correct where launch's injection sits
   *alongside* the agent's own config — claude (`--mcp-config` temp file,
   `claude.go:82`) and codex (`-c` overrides, `mcp.go:148`). For kimi, launch points
   `KIMI_CODE_HOME` at a temp dir (`kimi.go:75`), and the opencode family gets a whole
   inline document via `OPENCODE_CONFIG_CONTENT` / `KILO_CONFIG_CONTENT`
   (`opencode.go:34`); there the persisted entry may not be visible at all, so launch
   keeps writing — now headerless. Gate G3 settles which of those two the opencode
   family is.

URL resolution stays where it is: `ORQ_MCP_URL` → `deriveFromAPIBase(apiBase, "/v2/mcp")`
→ `DefaultMCPURL`, exported from `launch` and called by `connect` rather than copied,
so self-hosted and regional deployments carry their MCP endpoint for free.

### 5. Doctor

`arianpasquali/skills-safety-fixes` added `skillsCheck()` to `doctor.go` — a check
group returning `pass`/`warn` and naming the command that fixes it. The MCP check is a
sibling of that function, same shape, with one wording constraint.

`pass` must say **"entry present"**, never "MCP works". The CLI writes no credential
and holds no token, so it cannot see whether the user ever completed the OAuth flow in
that agent — a green check that implied working MCP would be a false positive for
every user who has not yet run `/mcp` or `codex mcp login`. The `pass` message names
the login command so the check is also the instruction.

`warn` when an MCP-capable agent is detected with no entry, naming
`orq connect <agent> mcp`. Never a non-zero exit: an unwired agent is an offer, not a
breakage (per RES-1270). Pi is not reported at all — it cannot receive MCP.

No expiry, renewal, or credential-drift checks. There is no credential.

### 6. After the write

Per agent, one line naming the login command — no shelling out, no blocking:

| Agent | Line |
| --- | --- |
| claude | `run /mcp in Claude Code, or 'claude mcp login orq-workspace'` |
| codex | `run 'codex mcp login orq-workspace'` |
| opencode | opencode prompts on first use (status `needs_auth`) |
| kilo | `run 'kilo mcp auth orq-workspace'` |
| kimi | `run 'kimi mcp auth orq-workspace'` |

### 7. Testing

- Round-trip write/remove per config format, restored from `e44c747^`'s
  `agents_test.go`, which had them for all five agents, updated for the corrected paths
  and the headerless shapes.
- Idempotence: writing twice produces one entry, byte-identical.
- Foreign-content preservation: unrelated keys and unrelated TOML survive a write and a
  remove.
- **Credential canary, now covering every agent and both packages:** no config written
  by `connect` *or* `launch` contains `Authorization`, `headers`, `bearer_token_env_var`,
  `bearerTokenEnvVar`, or any substring of a stored key. This is the test that keeps
  `e44c747`'s regression from returning, and it is the reason `writeMCP` takes no
  credential argument.
- Launch/connect interaction: with a persisted entry present, `orq launch claude --mcp`
  and `orq launch codex --mcp` inject nothing.
- Scope: `--local` writes `./.mcp.json` and leaves `~/.claude.json` untouched;
  `--global` the inverse; `--local` against a global-only agent warns and writes global;
  reads still find an entry in either scope.
- Doctor: `pass` asserts on the "entry present" wording, so a future edit cannot quietly
  promote it to a health claim.
- Pi: `orq connect pi mcp` reports unsupported and writes nothing.
- URL derivation for a non-default `ORQ_API_BASE_URL`.

## Verified

All gates are resolved. Evidence, 2026-08-26:

**Kilo and Kimi config paths.** Neither is installed here, so both were checked by
downloading the published package and reading the shipped binary rather than trusting
docs.

*Kimi Code* is `@moonshot-ai/kimi-code` 0.38.0 — the product this registry already
targets, whose installer defaults `KIMI_INSTALL_DIR=$HOME/.kimi-code`. Its bundle has
108 references to `KIMI_CODE_HOME` and 109 to `.kimi-code` against 3 to `.kimi/`, and
`mcpServers` is the key (143 references). So the pre-`e44c747` writer's
`.kimi-code/mcp.json` was right and an earlier draft of this spec was wrong to
"correct" it to `~/.kimi/mcp.json`: that path belongs to **Kimi CLI**
(`moonshotai.github.io/kimi-cli`), a different Moonshot product this registry does not
target. The bundle also documents a project scope — *"Project-local MCP:
`<cwd>/.kimi-code/mcp.json`"* — which is why kimi joins claude as a two-scope agent
above.

*Kilo* ships both: the `@kilocode/cli-darwin-arm64` 7.4.23 binary references
`kilo.json` 72 times and `kilo.jsonc` 39, alongside `.config/kilo` and
`KILO_CONFIG_CONTENT`. `.json` is the more common form and what the old writer used;
keep it, and have the reader accept either.

**Local and global file writes.** `claude mcp add --transport http … -s project` in a
scratch repo creates `./.mcp.json` containing exactly
`{"mcpServers": {"<name>": {"type": "http", "url": "…"}}}` — the shape section 2
specifies, confirmed rather than assumed. `~/.claude.json` on this machine is already
`0600`, so `writeJSONConfig`'s blanket chmod matches what Claude itself does and there
is nothing to reconcile. One wrinkle: Claude creates the *project* `.mcp.json` as
`0644`, and `.mcp.json` is meant to be committed and shared with a team, so narrowing
it to `0600` is unnecessary. Harmless — git tracks only the exec bit — but the project
writer should leave `0644` rather than inherit the global path's permissions.

**The opencode family merges its inline config.** With four MCP servers in
`~/.config/opencode/opencode.json`, running `opencode mcp list` under
`OPENCODE_CONFIG_CONTENT='{"mcp":{"g3-probe":…}}'` reported **five** servers — the four
persisted plus the injected one. So `OPENCODE_CONFIG_CONTENT` merges, and the
section-4 launch no-op applies to opencode and kilo. Kimi does not merge: launch points
`KIMI_CODE_HOME` at a fresh temp dir (`kimi.go:75`), so the persisted file is not on
kimi's search path at all and launch must keep writing there.

Two things fell out of that check worth recording. `opencode mcp auth <name>` is the
OAuth login verb, upgrading section 6's opencode row from "prompts on first use" to a
named command. And the machine already carried a persisted `orq-workspace` entry with
`"Authorization": "Bearer {env:ORQ_API_KEY}"` — a real v4.13.10 leftover. The writers
assign the whole `orq-workspace` value rather than merging into it, so they upgrade
such an entry to the headerless shape on the next `orq connect`, which is the desired
behaviour and should have a test.

## Out of scope

- Triggering the agent's OAuth flow from the CLI. Connect writes the config and prints
  one line per agent (section 6). No shelling out, no blocking on a browser.
- Cleaning up MCP entries written by v4.13.10. `e44c747` pinned a test that connect and
  disconnect leave them byte-identical, and this design does not change that.
- `orq setup mcp` as a subcommand. RES-1270 decided against it: redundant with the
  capability argument.
- Any second API key. See "Why that is now possible".
