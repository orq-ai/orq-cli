# orq CLI: MCP wiring for coding agents

Date: 2026-08-25
Status: approved design, not yet implemented
Ticket: RES-1435
Branch base: `Baukebrenninkmeijer/cli-mcp-support` (on `origin/main` + `arianpasquali/skills-safety-fixes`)

## Problem

`orq connect` and `orq setup` wire coding agents to the orq gateway, but not to the
orq MCP server. A user who wants orq's tools in Claude Code or Codex has to write the
MCP entry by hand into a per-agent config file whose format differs for every agent.

MCP wiring used to exist. It was removed in `e44c747` ("gateway-only wiring with
least-privilege keys"), because the key it wrote into agent configs was minted with
`permission_mode: "all"` — a leaked agent config was a workspace-admin credential. The
commit removed MCP wholesale rather than narrow it, and left entries written by
v4.13.10 in place rather than deleting them.

This design re-adds MCP wiring on a credential model that does not have that failure:
OAuth for the agents that support it, and a key scoped to a single capability domain
for the agents that do not.

## Why OAuth is available now

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

An MCP client that implements the spec's auth flow therefore needs nothing from us but
the URL: it discovers the authorization server, registers itself dynamically, runs
PKCE in the user's browser, and holds its own refresh token. No credential is written
to disk by the CLI, and none can leak from an agent config.

## Design

### 1. MCP is a connect capability

`cli/custom/commands/connect.go` already models capabilities: `capGateway`,
`capTracing`, `capSkills` in `connectCapabilities`, with `availableCapabilities()`
gating which are actually built. Add `capMCP = "mcp"` to both.

Everything else follows from the existing dispatcher with no new command:

| Command | Behaviour |
| --- | --- |
| `orq connect` | writes gateway + skills + mcp for detected agents |
| `orq connect codex mcp` | writes only the MCP entry, only for codex |
| `orq disconnect claude mcp` | removes only Claude's MCP entry |
| `orq connect --status` | reports MCP wiring alongside gateway and skills |
| `orq connect --dry-run` | shows the file and the entry it would write |

`orq setup` gains **no new consent question**. Its `promptForCapabilities` multi-select
("What should orq connect?") already lists every available capability; `mcp` appears
there automatically once it is in `availableCapabilities()`, selected by default like
the others.

### 2. Two credential modes on the agent registry

Restore the four `agentSpec` fields deleted in `e44c747` — `mcpConfig`, `writeMCP`,
`removeMCP`, `manualSnippet` — plus `mcpPresent` (the read side, for `--status` and
doctor) and one new field, `mcpAuth`, with two values.

**`mcpAuthOAuth` — claude, codex.** The writer emits the server entry and nothing
else:

```json
{ "mcpServers": { "orq-workspace": { "type": "http", "url": "https://api.orq.ai/v2/mcp" } } }
```

No `headers` key, no `Authorization`, no env reference. The agent runs the OAuth flow
itself on first use.

**`mcpAuthHeader` — opencode, kilo, kimi.** These clients authenticate remote MCP
servers with a bearer header only. They get a **second, separate API key**:

- New `auth.MCPAccess()` returning `{"mcp_gateway": "write"}` — the single domain
  `gatewayAccessMap` deliberately excludes, with the comment "MCP data-plane execution
  rather than gateway wiring".
- Minted through the existing `auth.NewAPIKeyRequest` path with
  `permission_mode: "restricted"`, exactly as the gateway key is.
- Stored in `credentials.json` as `mcp_key` and `mcp_key_id`, alongside `gateway_key`
  / `gateway_key_id`. It must not reuse either `api_key` (which `installSessionPreRun`
  keys off) or `gateway_key` (which cannot authenticate to MCP by construction).
- Written into the agent's config in that agent's own format, with the config file at
  `0600`.
- `orq disconnect ... mcp` revokes the key once no header-mode agent is still wired,
  and clears the `mcp_key` fields.

The tradeoff is explicit: header mode puts a credential on disk. It is bounded to one
capability domain and revocable, which is what `e44c747` asked for and did not have.
Verification item V3 below decides whether that bound holds.

### 3. Scope: a wizard step and two flags

`--global` and `--local` return to `orq connect` and `orq disconnect`, meaningful for
`mcp` only. Naming a scope alongside a gateway-only run warns rather than silently
doing nothing.

Only Claude Code has two scopes: `./.mcp.json` (project) and `~/.claude.json` (global).
Codex, opencode, kilo and kimi read MCP config from a fixed directory; `--local`
against them warns and writes the global path. The old `pathFor(project, global)` and
`alwaysGlobalPath` helpers in `agents.go` already express exactly this.

**New wizard step.** `orq setup` asks a second question immediately after the
capability multi-select, in the same style (`survey`, same label padding, routed
through `promptStdio()`):

```
Where should the MCP entry go?
  > local     this project only (./.mcp.json)
    global    every project on this machine (~/.claude.json)
```

Asked only when `mcp` is among the chosen capabilities, the run is interactive, and at
least one scope-capable agent (today: claude) is detected. Skipped — defaulting to
local — otherwise. `--global` / `--local` / `--yes` / `--no-input` all pre-answer it.

There is no scope *inference*. The pre-`e44c747` code had none either: the path was
cwd-relative unless `--global`. The answer comes from the user or the flag, so there is
nothing for a heuristic to disagree with.

Note while adding this: the existing `promptForCapabilities` calls `survey.AskOne`
without `promptStdio()`, unlike every other prompt in the CLI. Not a bug — a prompt
only appears when stdout is a terminal, so it cannot land in a redirected payload — but
the new prompt should use `promptStdio()`, and the existing one may as well match.

### 4. One URL resolver, shared with launch

`cli/custom/launch/mcp.go` already resolves the endpoint as
`ORQ_MCP_URL` → `deriveFromAPIBase(apiBase, "/v2/mcp")` → `DefaultMCPURL`, and
registers it under `MCPServerName = "orq-workspace"`. The comment on that constant is
load-bearing: launch deliberately uses the same name so a session entry *shadows* a
persisted one instead of the agent loading both and paying for every tool definition
twice.

Connect must therefore use the same name and the same resolution, from one shared
helper rather than a second copy. Self-hosted and regional deployments carry their MCP
endpoint from `ORQ_API_BASE_URL` for free.

### 5. Doctor

`arianpasquali/skills-safety-fixes` added `skillsCheck()` to `doctor.go` — a check
group returning `pass`/`warn` and naming the command that fixes it. The MCP check is a
sibling of that function, same shape:

- `pass` — entry present in the agent's config.
- `warn` — agent detected, no entry: "run `orq connect <agent> mcp`". Never a non-zero
  exit; an unwired agent is an offer, not a breakage (per RES-1270).
- `warn` — header-mode agent wired but `mcp_key` missing from credentials.

### 6. Testing

- Round-trip write/remove per config format, restored from `e44c747^`'s
  `agents_test.go`, which had them for all five agents.
- Idempotence: writing twice produces one entry, byte-identical.
- Foreign-content preservation: unrelated keys and unrelated TOML survive a write and
  a remove.
- **Credential canary:** an `mcpAuthOAuth` agent's config, after a write, contains no
  `Authorization`, no `headers`, and no substring of any key. This is the test that
  keeps `e44c747`'s regression from returning.
- Scope: `--local` writes `./.mcp.json` and leaves `~/.claude.json` untouched;
  `--global` the inverse; `--local` against a global-only agent warns and writes global.
- URL derivation for a non-default `ORQ_API_BASE_URL`.

## Verify before implementing

These four are gates, not notes. V3 in particular can invalidate the section-2 design.

**V1 — Codex remote MCP.** Confirm against a real Codex install that it supports a
remote streamable-HTTP MCP server with OAuth, and what the current `config.toml` shape
is (`[mcp_servers.orq-workspace] url = ...`, versus an `experimental_use_rmcp_client`
gate). If Codex cannot do OAuth in the shipped version, it moves to header mode.

**V2 — opencode / kilo / kimi.** Confirm they lack OAuth support. Any that has gained
it moves to OAuth mode and needs no key at all, which is strictly better.

**V3 — Is an `mcp_gateway`-only key actually enough?** Mint one against a live
workspace and call `/v2/mcp`. Two failure modes: it may not authenticate at all, or it
may authenticate and then expose a tool list that fails on every call because the
individual tools need `agents`, `deployments`, `traces` and friends. If the key needs a
materially wider access map, "narrowly scoped" is a fiction and the header-mode
tradeoff must go back to the user before implementation.

**V4 — Local and global file writes.** Validate both scopes for real, per agent:
`./.mcp.json` created and merged in a project; `~/.claude.json` merged in place without
disturbing Claude's own contents (it owns that file aggressively and keeps far more
than MCP servers in it); permissions `0600` on header-mode files; and that a
`disconnect` returns each file to its pre-write bytes.

## Out of scope

- Triggering the agent's OAuth flow from the CLI. Connect writes the config and prints
  one line per agent saying how to authenticate there (`/mcp` in Claude Code,
  `codex mcp login orq-workspace`). No shelling out, no blocking on a browser.
- Cleaning up MCP entries written by v4.13.10. `e44c747` pinned a test that connect and
  disconnect leave them byte-identical, and this design does not change that.
- `orq setup mcp` as a subcommand. RES-1270 already decided against it: redundant with
  the capability argument.
