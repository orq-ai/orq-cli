# orq CLI: `--local` and `--global` scopes for skills

Date: 2026-08-25, revised 2026-09-02 after a design review, per-agent probes and a
five-critic hate review
Status: approved design, not yet implemented
Ticket: RES-1437
Base: `main`. The scope flags, the three-state `configScope`, the both-scopes removal
rule, the `--status` scope column and the `orq setup` scope question all exist already
(landed with RES-1435, PR #35). They serve the `mcp` capability only. This design turns
them on for skills, and fixes three things the probes showed our agent registry has wrong.

## Problem

Skills install to the agent's home directory and nowhere else. `orq connect skills` and
`orq launch` both resolve targets through `skills.Targets()` in
`cli/custom/skills/targets.go`, which returns `~/.claude/skills`, `~/.codex/skills`,
`~/.kimi-code/skills` and the shared `~/.agents/skills`. A user cannot scope a skill set
to one repo, and every repo on the machine sees the same set.

**How to see it:** `orq connect skills` from inside any repo, then `ls ~/.claude/skills` —
the links are in `$HOME`, not the repo. `orq connect skills --local` warns "`--local` has
nothing to scope here: only the mcp capability has a project scope" and writes `$HOME`
anyway.

## What the agents actually read

Probed on 2026-09-02. codex and opencode were proven by running them offline
(`codex debug prompt-input`, `codex mcp list --json`, `opencode debug skill`,
`opencode debug config`) from scratch git repos under `/tmp`; kimi and pi from their
shipped, unminified source; kilo from the `Kilo-Org/kilocode` source on GitHub; claude
from its documentation only.

### Skills directories

| Agent | Home | Project | Project anchor | Dedupes by name |
| --- | --- | --- | --- | --- |
| claude | `~/.claude/skills` | `.claude/skills` | cwd | — |
| codex | `~/.codex/skills` **and** `~/.agents/skills` | `.codex/skills`, `.agents/skills` | every dir from cwd up to the git root | **no** |
| opencode | `~/.agents/skills`, `~/.claude/skills`, `~/.config/opencode/skills` | `.opencode/skill(s)`, `.agents/skills`, `.claude/skills` | every dir from cwd up to the git root | yes |
| kimi | `~/.kimi-code/skills` **and** `~/.agents/skills` | `.kimi-code/skills`, `.agents/skills` | **git root only** (nearest `.git` ancestor; cwd when no repo) | yes, first wins |
| pi | `~/.pi/agent/skills` **and** `~/.agents/skills` | `.pi/skills` (cwd only), `.agents/skills` | every dir from cwd up to the git root; **only if the project is trusted** | yes |
| kilo | `~/.agents/skills`, `~/.claude/skills`, `~/.kilo(code)/skills`, `~/.config/kilo/skills` | `.agents/skills`, `.claude/skills`, `.kilo(code)/skills` | every dir from cwd up to the git worktree root | yes, later wins |

Kimi's skills anchor (git root) differs from its MCP anchor (cwd, table below). Both come
from the same source read (`packages/agent-core/src/skill/scanner.ts` walks for `.git`;
`.kimi-code/mcp.json` is resolved against cwd). Re-check the skills anchor against a live
kimi before shipping the warning in §3, which depends on it.

Three consequences for the code we ship today:

1. **`.agents/skills` is read by every agent except claude**, at both levels. The
   `sharedReaders` map (`opencode`, `kilo`, `pi`) is missing codex and kimi.
2. **Codex is double-indexed today.** `ownDir` gives codex `~/.codex/skills`, and
   `Targets` also writes `~/.agents/skills` whenever any shared reader is selected. Codex
   reads both and does not dedupe: every orq skill appears twice in its catalog on any
   machine where codex is installed next to opencode, pi or kilo. Kimi has the same two
   directories written but dedupes, so it is only wasted links.
3. **The Linux `$XDG_CONFIG_HOME/agents/skills` target is read by nothing.** Kilo's only
   XDG path is `$XDG_CONFIG_HOME/kilo`; `~/.config/agents/skills` exists as a GitHub
   discussion proposal (Kilo-Org/kilocode#5783), not as code. `Targets` writes it on
   Linux whenever kilo is selected.

### MCP config files

| Agent | Global | Project (write path in bold) | Project anchor | Gate |
| --- | --- | --- | --- | --- |
| claude | `~/.claude.json` | **`.mcp.json`** | cwd | — |
| codex | `~/.codex/config.toml` (or `$CODEX_HOME`) | **`.codex/config.toml`** | git root and every dir down to cwd; cwd when no repo | **project layers load only when `~/.codex/config.toml` marks the repo root `trust_level = "trusted"`**; otherwise silently ignored |
| opencode | `~/.config/opencode/opencode.json` | **`opencode.json`**, `opencode.jsonc`, `.opencode/opencode.json` | git root and every dir down to cwd; cwd when no repo | none |
| kimi | `~/.kimi-code/mcp.json` | **`.kimi-code/mcp.json`** | cwd | — |
| kilo | `~/.config/kilo/kilo.json` | **`kilo.json`**, `kilo.jsonc`, `.kilo/kilo.json` | git root and every dir down to cwd | none |

The registry in `cli/custom/commands/agents.go` models codex (`codexPath`, ignores the
scope argument), opencode and kilo (`alwaysGlobalPath`) as global-only. All three have a
project scope. `orq connect opencode mcp --local` today warns and writes `$HOME`, and
`mcpScopeAware` reports false for them, so `orq setup` never asks the scope question on a
machine that has only those agents.

This contradicts the sibling spec, `2026-08-25-orq-cli-mcp-wiring-design.md` §3
("Codex, opencode and kilo read MCP config from a fixed location") and the plan
`2026-08-26-orq-cli-mcp-wiring.md`. That claim was made from `--help` output and config
docs; the 2026-09-02 probes ran the agents. The sibling spec carries a dated correction
pointing here; the plan is historical and is left as written.

## Design

### 1. Skills consume the existing flags

`--local` and `--global` stay exactly where they are, declared once by `addScopeFlags`
for both `orq connect` and `orq disconnect`. `capScoped()` accepts `capSkills` alongside
`capMCP`; that is what makes `checkScopeFlags`, `scopedPaths` and `resolveScope` treat a
skills run as scopeable. It is not the whole change — §9 lists every touch point — but it
is the one that decides the rest.

`orq launch` gains no scope flag. Its old `--local` (RES-1349, skip the sandbox prompt)
was deleted with the sandbox in `472eef1` and is now forwarded to the agent untouched
(`launch/args_test.go`, `TestSandboxFlagsBelongToTheAgent`), so the name is free; launch
does not take it because it does not need a flag at all — see §5.

Defaults are unchanged: a bare `orq connect skills` is global, because `install.sh` runs
setup non-interactively from wherever the user happens to be and a local default would
hand one directory the skills and no other. A bare `orq disconnect skills` removes both
scopes, the rule `configScope`'s doc comment already gives for MCP. No prompt in either
direction: the non-interactive answer has to be "both" regardless.

### 2. Two local directories, and the same two globally

Local targets, anchored at **cwd**:

- `./.claude/skills` — claude
- `./.agents/skills` — everyone else (codex, opencode, kimi, pi, kilo)

This is what `npx skills` does, and it is what the table above says the agents read.
Writing each agent's own project directory as well (`.codex/skills`, `.kimi-code/skills`,
`.opencode/skill`, `.pi/skills`) would double-index codex and buy nothing for the rest.

The consequence is stated plainly: **a local install is scoped by repository, not by
agent.** `orq connect codex skills --local` writes `./.agents/skills`, which opencode, pi,
kimi and kilo read too. The agent argument still decides which directories are written
(claude alone means no `.agents/skills`); it does not fence other agents out of a shared
directory, and `--status` describes the directory, not the readers.

Global targets become the same two: `sharedReaders` gains codex and kimi, `ownDir` keeps
only claude, and the Linux XDG branch is deleted. That is the fix for consequences 1–3.

There is no per-agent skip under `--local`. Every agent maps to one of the two
directories, and the directories are created if absent — the same `os.MkdirAll` the
global install does. `Targets` still does no detection (its doc comment is explicit);
the caller passes only detected agents, as today.

**Reconciling an existing install.** A manifest written by the current binary records
links under `~/.codex/skills`, `~/.kimi-code/skills` and, on Linux, the XDG path. Nothing
in `install()` or `refresh()` removes a link because its directory dropped out of
`Targets` — `refresh` prunes by *skill name* only (`project.go`, the `!inSet[l.Skill]`
branch). So install gains one step, under the manifest lock, before it projects: every
recorded non-session link whose directory is not in the current global target set, and
that `isOurs` still proves is ours, is removed and dropped from the manifest. A path
something else now occupies is reported as skipped, the same as everywhere else. This is
the only place the old layout is ever consulted.

### 3. Anchor: cwd, with one warning

The local anchor is the cwd, the same as `projectOrGlobalPath` uses for MCP. Not the git
root: anchoring there takes the choice away from the user, and codex, opencode, pi and
kilo all walk from cwd up to the root anyway, so an install at cwd is found from cwd and
below.

Kimi does not walk. It reads the nearest ancestor holding `.git`, so a cwd-anchored
install in a subdirectory is invisible to it. Rather than special-case kimi to the root —
which reintroduces the loss of control — connect warns once when cwd is inside a repo and
is not its root. One run-level `rep.warn` line, the form `checkScopeFlags` already uses:

```
--local writes into /repo/sub, but the repository root is /repo; kimi reads project skills from the root only
```

The warning fires only inside a repo. Outside one there is no root to disagree with. It
is never a refusal; the one refusal — cwd is `$HOME` — is already in `checkScopeFlags`
and applies to skills unchanged.

**Root detection is one helper** in the skills package (see §4 for why it lives there):
resolve cwd with `filepath.EvalSymlinks`, then walk parents for a `.git` entry — file or
directory, so a linked worktree counts — stopping at the first hit. A nested repo's root
is the nearest `.git`, which is what codex, opencode and kilo do too. An unreadable parent
ends the walk as "no repo". There is no such helper in `cli/custom` today.

### 4. Scope is a type in the skills package; the manifest does not change

Three consumers need to answer "which directories does this scope mean at this cwd":
connect and disconnect (from `opts.scope`), launch (from cwd alone, and `launch` cannot
import `commands`), and status/doctor (to classify a recorded path). Deriving that in each
place is three copies of the `$HOME` rule and the root helper that will drift.

So the skills package owns it:

```go
type Scope int
const (ScopeGlobal Scope = iota; ScopeLocal; ScopeBoth)

// ScopeFor is the launch default: local unless cwd is $HOME.
func ScopeFor(cwd string) Scope

// Targets is every directory the scope means for these agents, at this cwd.
func Targets(agents []string, scope Scope) ([]Target, error)
```

`commands` converts `configScope` at the boundary (`scopeUnset` → `ScopeBoth` for a
removal, `ScopeGlobal` for a write, exactly the split `mcpWriteScope`/`scopedPaths`
already make). `Target` carries its scope, so `--status`, the `--json` payload and doctor
classify a recorded path by asking whether it sits under a `ScopeLocal` or `ScopeGlobal`
target for the current cwd — no `Scope` field on `Link`, no manifest version bump, no
migration. The one thing a stored field would buy — knowing a link was local after the
user moved the repo — describes a link whose directory no longer exists.

`Remove(agents, scope)` selects a link by **agent membership and path**: the existing
agent rule (`wanted[l.Agent]`, shared links when any named agent is a shared reader)
*and* `l.Path` inside one of `Targets(agents, scope)`. Today `remove()` filters on agent
alone; the path predicate is new, and `isOurs` still gates every deletion. Local links
that live outside the current cwd's directories are not selected by any scope; a bare
disconnect counts them and says where they are ("left 4 links in 2 other directories; run
`orq disconnect skills` from those"). The manifest stays the single
`~/.orq/materialized-skills.json`: it is the deletion allow-list and the lock PR #31 made
correct, and a per-repo copy forks both. That list only shrinks through a disconnect run
from the right directory; there is no garbage collection for a repo the user deleted,
because "the directory is gone" is indistinguishable from "the drive is unmounted".

### 5. `orq launch` defaults to local

Session skills link into the two local directories at cwd for the life of the session,
and into the global directories when cwd is `$HOME` — `ScopeFor(cwd)`, the same rule
`checkScopeFlags` *refuses* on for a typed `--local`. Launch does not refuse; it picks
the other scope silently, because a launcher run from `$HOME` has to start the agent
either way. No flag: a launcher run inside a repo putting its skills in that repo is the
behaviour the user expects, and an opt-out is a one-flag addition if anyone asks for it.

`InstallSession(agent)` calls `Targets([]string{agent}, ScopeFor(cwd))`. The session
claim, release, heartbeat and sweep in `session.go` are unchanged. For the duration of
the session the links are untracked files in the repo. Launch says nothing about that:
the gitignore line connect prints (§7) has covered the directory once, and repeating it
on every launch is noise.

Two launch paths do not go through `InstallSession` and are **unchanged** by this
design:

- **kimi.** Launch hands kimi a launcher-owned `KIMI_CODE_HOME` and writes the session
  skills into it through `maybeWriteSessionSkills` (`launch/mcp.go`, `launch/kimi.go`).
  That directory is not a `Targets` directory, so the scope does not apply and the §3
  warning cannot fire from launch. A permanent local install is still read by kimi at
  the git root through its own discovery.
- **Windows.** `maybeInstallSessionSkills` returns before `InstallSession` and tells the
  user to run a permanent connect. That stays.

### 6. `--status`, `--json` and `doctor` classify by the current cwd

`--status` today builds skills rows from every manifest link with no scope and no cwd
filter (`wiredTargets`, the skills block), and `printWiredTable` shows the scope column
only when some row has one; `ui_test.go` pins a skills row with an empty scope cell. That
changes: each skills row is classified against `Targets(agents, ScopeBoth)` at cwd and
gets `global` or `local`; local links from other directories are not rows but one
trailing count. Shared (`Agent == ""`) links are included only when a selected agent is a
shared reader, the same membership rule `Remove` uses.

`skillsPayload` (the `--json` shape) gains a `scope` per directory, from the same
classification, so a script sees what the table shows — `mcpResult` already carries one.

`doctor`'s `skillsCheck()` (in `agents.go`, not `doctor.go`) reads the whole manifest
today. It gets the same three buckets as disconnect, each with its own remedy: global
links missing → "run `orq connect skills`"; local links at cwd missing → "run
`orq connect skills --local`"; local links elsewhere → counted, no remedy, because the
fix is to run doctor from that directory. Never a non-zero exit for any of them.

None of this reports cross-reads. opencode reads `~/.claude/skills`; codex reads
`~/.agents/skills`; an agent can see our skills without us having written a directory for
it. Status describes what `disconnect` can undo.

### 7. What connect prints on a local install

Two run-level lines, each only when it applies, plus one line per selected agent with a
manual step the install cannot do for it:

- The subdirectory warning from §3, when cwd is inside a repo and not its root.
- `add .agents/skills/ and .claude/skills/ to .gitignore` naming only the directories
  written, printed only inside a repo. Connect does not edit `.gitignore`: writing to a
  tracked file unasked is not something a connect command should do. On Unix the links
  point into `~/.orq/snapshot/`, so a committed one is a dangling symlink for everyone
  else; on Windows copy mode produces real directories, and committing one pins a
  generation the CLI will later expect to own. Copy mode itself is unaffected by the
  scope — it is already the fallback everywhere.
- For skills, one agent has a manual step: **pi** loads project skills only for a trusted
  project. The line is printed once when pi is selected and the scope is local, in the
  shape the MCP writer already uses for OAuth login. The codex trust line belongs to the
  MCP run and is described in §8; a skills run never prints it.

### 8. MCP gains the project scope for codex, opencode and kilo

In the registry:

- codex: a codex-specific resolver — project `.codex/config.toml` at cwd, global
  `$CODEX_HOME/config.toml` falling back to `~/.codex/config.toml`. `projectOrGlobalPath`
  cannot express the env fallback; `codexPath` cannot express the project side.
- opencode: `projectOrGlobalPath("opencode.json", ".config/opencode/opencode.json")`.
- kilo: `projectOrGlobalPath("kilo.json", ".config/kilo/kilo.json")`.

The write path is one file per scope (the bold entries in the table). The agents also
read every layer between the git root and cwd; whether `mcpPresent`, `--status` and
disconnect should read that whole chain is a separate decision, tracked outside this
ticket, because a false "not wired" from a subdirectory is an MCP-only problem.

`mcpScopeAware` then reports all five agents correctly, `orq setup` asks the scope
question on machines that only have those agents, and `orq connect codex mcp --local`
writes the file codex reads instead of warning that it cannot. The entry formats do not
change. On a local codex write, connect prints the manual step: the project file loads
only when `~/.codex/config.toml` marks the repo root `trust_level = "trusted"`. The CLI
does not write that entry — elevating a repository's trust is the user's decision.
`--status` reports the entry as present regardless; the gate is codex's, and a
"present but untrusted" state would need the trust file parsed, which is out of scope.

### 9. Touch points

Everything this design changes in code, so the diff can be checked against it:

| Where | Change |
| --- | --- |
| `skills/targets.go` | `Scope`, `ScopeFor`, root helper; `Targets(agents, scope)`; `sharedReaders` += codex, kimi; `ownDir` = claude only; XDG branch deleted |
| `skills/project.go` | `Install(agents, scope)` with the reconciliation step from §2; `Remove(agents, scope)` with the path predicate from §4 |
| `skills/session.go` | `InstallSession` passes `ScopeFor(cwd)` |
| `launch/mcp.go` | dry-run branch passes the same scope |
| `commands/connect.go` | `capScoped` += skills; `checkScopeFlags` warning text stops naming mcp ("only the mcp capability has a project scope", "--local scopes mcp only"); skills rows in `wiredTargets` classified and cwd-filtered; `skillsPayload.scope`; §7 lines |
| `commands/setup.go` | `scopeMatters` counts a detected agent that receives skills; `promptForScope` wording covers both; `skillTargetsFor` takes the scope; the skills install call passes it |
| `commands/agents.go` | codex resolver; opencode and kilo `projectOrGlobalPath`; `skillsCheck` buckets |
| sibling MCP spec | dated correction on §3 |

Existing tests that assert the old contract and change with it: `connect_test.go`
(`--local` on skills warns "only mcp"), `setup_test.go` (`scopeMatters(gateway, skills)`
is false), `skills_test.go` (XDG target on Linux; `~/.codex/skills` and
`~/.kimi-code/skills` as codex/kimi targets), `ui_test.go` (skills row with an empty
scope cell).

## Out of scope

- Editing `.gitignore`, or writing codex's `[projects."…"] trust_level` entry.
- Per-repo generation snapshots or manifests; garbage-collecting local links whose
  repo is gone.
- A scope flag or opt-out on `orq launch`.
- Reading the full ancestor chain of project MCP files for presence checks (§8).
- Tying the local scope to the active orq project (RES-1497). That scopes credentials
  and workspace data; this scopes where files land. Two axes that share a word.
- Migrating an existing global install into a repo. `orq connect skills --local` in a repo
  that already has the global set simply adds the local one; both are recorded, both are
  removable.

## Tests

- `Targets`: `ScopeLocal` returns exactly `<cwd>/.claude/skills` and
  `<cwd>/.agents/skills` for the selected agents; `ScopeGlobal` returns
  `~/.claude/skills` and `~/.agents/skills` and nothing else — no `~/.codex/skills`, no
  `~/.kimi-code/skills`, no XDG path on Linux. `~/.agents/skills` appears once however
  many shared readers are selected. `ScopeFor($HOME)` is global; any other cwd is local.
- Root helper: plain repo, linked worktree (`.git` file), nested repo (nearest wins),
  no repo, symlinked cwd resolving to the same root.
- `--local` from `$HOME` is refused for skills exactly as for mcp. `--local` with
  `gateway skills` warns that gateway is machine-wide without naming mcp. A plain
  non-repo cwd installs locally with no warning.
- The subdirectory warning fires inside a repo below its root, names kimi, and does not
  fire at the root or outside a repo.
- Reconciliation: a manifest recording `~/.codex/skills`, `~/.kimi-code/skills` and the
  XDG path is cleaned up by the next `orq connect skills`; a path since replaced by
  something foreign is skipped and reported; `$CODEX_HOME` and `$KIMI_CODE_HOME` set to
  non-default values resolve the old paths the manifest actually holds.
- `Remove`: bare removes global and local-at-cwd and reports the count of local links
  elsewhere; `--local` and `--global` each leave the other alone; a link whose agent
  matches but whose path is in the other scope is untouched.
- Launch links at cwd, joins an existing permanent local install at the same cwd rather
  than reprojecting it, and releases on exit; at `$HOME` it links globally; kimi's
  `KIMI_CODE_HOME` path and the Windows early return are unchanged.
- `--status` shows `local`/`global` on skills rows, local rows only for the current cwd,
  shared rows only when a selected agent is a shared reader; `--json` carries the same
  scope. `doctor` produces the three buckets with their remedies. `ui_test.go`'s empty
  scope cell is replaced, not kept.
- `scopeMatters` is true for skills with a detected receiving agent and no mcp; the
  prompt text names neither capability alone.
- MCP round-trip for codex (with and without `$CODEX_HOME`), opencode and kilo at the
  project path, leaving the global file byte-identical, plus the existing credential
  canary on the project file. A local codex write prints the trust line.
- One golden-output test for the `connect skills --local` summary, so the warning, the
  gitignore line and the pi line are reviewed in a diff rather than by hand.
