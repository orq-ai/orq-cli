# orq CLI: `--local` and `--global` scopes for skills

Date: 2026-08-25, revised 2026-09-02 after a design review and per-agent probes
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

| Agent | Global | Project | Project anchor | Gate |
| --- | --- | --- | --- | --- |
| claude | `~/.claude.json` | `.mcp.json` | cwd | — |
| codex | `~/.codex/config.toml` | `.codex/config.toml` | git root and every dir down to cwd; cwd when no repo | **project layers load only when `~/.codex/config.toml` marks the repo root `trust_level = "trusted"`**; otherwise silently ignored |
| opencode | `~/.config/opencode/opencode.json` | `opencode.json(c)`, `.opencode/opencode.json` | git root and every dir down to cwd; cwd when no repo | none |
| kimi | `~/.kimi-code/mcp.json` | `.kimi-code/mcp.json` | cwd | — |
| kilo | `~/.config/kilo/kilo.json` | `kilo.json(c)`, `.kilo/kilo.json` | git root and every dir down to cwd | none |

The registry in `cli/custom/commands/agents.go` models codex (`codexPath`, ignores the
scope argument), opencode and kilo (`alwaysGlobalPath`) as global-only. All three have a
project scope. `orq connect opencode mcp --local` today warns and writes `$HOME`, and
`mcpScopeAware` reports false for them, so `orq setup` never asks the scope question on a
machine that has only those agents.

## Design

### 1. Skills consume the existing flags

`--local` and `--global` stay exactly where they are, declared once by `addScopeFlags`
for both `orq connect` and `orq disconnect`. `capScoped()` accepts `capSkills` alongside
`capMCP`. That one change makes `checkScopeFlags`, `scopedPaths`, the `--status` scope
column and `orq setup`'s `resolveScope` cover skills with no further wiring:
`scopeMatters` counts a detected agent that receives skills, and `promptForScope`'s
question drops "the MCP entry" for wording that covers both.

`orq launch --local` keeps its RES-1349 meaning (skip the sandbox safety prompt). Launch
gains no scope flag; see §5.

Defaults are unchanged: a bare `orq connect skills` is global, because `install.sh` runs
setup non-interactively from wherever the user happens to be and a local default would
hand one directory the skills and no other. A bare `orq disconnect skills` removes both
scopes, the same rule MCP already has, so a project install cannot outlive every
disconnect run by whoever forgot which scope it landed in. No prompt in either direction:
a prompt has to be skipped under `--no-input`, `--yes` and every non-TTY run, so the
non-interactive answer would have to be "both" regardless.

### 2. Two local directories, and the same two globally

`Targets(agents []string, global bool)`. The `bool` mirrors the registry's
`mcpConfig func(global bool)` shape; every call site already holds one.

Local targets, anchored at **cwd**:

- `./.claude/skills` — claude
- `./.agents/skills` — everyone else (codex, opencode, kimi, pi, kilo)

This is what `npx skills` does, and it is what the table above says the agents read.
Writing each agent's own project directory as well (`.codex/skills`, `.kimi-code/skills`,
`.opencode/skill`, `.pi/skills`) would double-index codex and buy nothing for the rest.

Global targets become the same two: `sharedReaders` gains codex and kimi, `ownDir` keeps
only claude, and the Linux XDG branch is deleted. That is the fix for consequences 1–3.
An existing global install that recorded `~/.codex/skills` or `~/.kimi-code/skills` links
is still in the manifest and still removable; `orq connect skills` re-run after the
upgrade removes them through the normal refresh path, because they are recorded links
whose target is no longer in `Targets`.

There is no per-agent skip under `--local`. Every agent maps to one of the two
directories, and the directories are created if absent — the same `os.MkdirAll` the
global install does.

### 3. Anchor: cwd, with one warning

The local anchor is `os.Getwd()`, the same as `projectOrGlobalPath` uses for MCP. Not the
git root: anchoring there takes the choice away from the user, and codex, opencode, pi and
kilo all walk from cwd up to the root anyway, so an install at cwd is found from cwd and
below.

Kimi does not walk. It reads the nearest ancestor holding `.git`, so a cwd-anchored
install in a subdirectory is invisible to it. Rather than special-case kimi to the root —
which reintroduces the loss of control — connect warns once when cwd is inside a repo and
is not its root, naming both paths and kimi:

```
warn  --local writes to /repo/sub/.agents/skills; the repository root is /repo, and kimi
      reads project skills from the root only
```

The warning fires only inside a repo. Outside one there is no root to disagree with. It
is never a refusal; the one refusal — cwd is `$HOME` — is already in `checkScopeFlags`
and applies to skills unchanged.

### 4. Scope is derived from the path; the manifest does not change

A link's `Path` is absolute and already recorded. `disconnect --local` asks
`Targets(agents, false)` for the local directories at the current cwd and removes the
recorded links inside them; `--global` does the same with the global set; unscoped does
both. `--status` labels a link `local` when it sits under a local directory for the
current cwd and `global` otherwise.

No `Scope` field on `Link`, no manifest version bump, no migration. The one thing a field
would buy — knowing a link was local after the user moved the repo — describes a link
whose directory no longer exists.

The manifest stays the single `~/.orq/materialized-skills.json`. It is the deletion
allow-list and the lock PR #31 made correct; a per-repo copy forks both. Local links from
other repos that a bare disconnect cannot reach from here are counted and reported
("left 4 links in 2 other directories; run `orq disconnect skills` from those"), not
silently orphaned.

### 5. `orq launch` is local

Session skills link into the two local directories at cwd for the life of the session,
and into the global directories only when cwd is `$HOME` — the same fallback the
`checkScopeFlags` refusal encodes. No flag: `--local` is taken, and a launcher run inside
a repo putting its skills in that repo is the behaviour the user expects. An opt-out is a
one-flag addition if anyone asks for it.

Session links are still session-scoped in the manifest, still released on exit, still
swept when the process is gone. `InstallSession` calls `Targets(agent, global)` with the
resolved `global`; nothing else in `session.go` changes. For the duration of the session
the links are untracked files in the repo. Launch says nothing about that: the gitignore
line connect prints (§7) has covered the directory once, and repeating it on every launch
is noise.

### 6. `--status` and `doctor` report what we own

`--status` gains nothing new: the scope column already exists, and the skills rows get a
`local`/`global` label from §4. It reports local links for the current cwd only; listing
every repo the CLI has ever installed into is noise from a global command.

It does not report cross-reads. opencode reads `~/.claude/skills`; codex reads
`~/.agents/skills`; an agent can genuinely see our skills without us having written a
directory for it. `--status` describes what `disconnect` can undo, and the manifest is the
source of truth for both.

`doctor`'s `skillsCheck()` applies the same cwd rule: a local install whose links are
missing is a `warn` naming `orq connect skills --local`.

### 7. What connect prints on a local install

At most three lines beyond the normal per-agent rows, each only when it applies:

- The subdirectory warning from §3, when cwd is inside a repo and not its root.
- One line saying which directories to gitignore. Connect does not edit `.gitignore`:
  writing to a tracked file unasked is not something a connect command should do. On
  Unix the links point into `~/.orq/snapshot/`, so a committed one is a dangling symlink
  for everyone else; on Windows copy mode produces real directories, and committing one
  pins a generation the CLI will later expect to own.
- One line per agent with a manual step the install cannot do for it, in the shape the
  MCP writer already uses for OAuth login: pi loads project skills only for a trusted
  project; codex loads a project `.codex/config.toml` only for a repo marked
  `trust_level = "trusted"` in `~/.codex/config.toml`. The CLI does not write the trust
  entry: elevating a repository's trust level is the user's decision, not ours.

### 8. MCP gains the project scope for codex, opencode and kilo

In the registry:

- codex: `mcpConfig` becomes project-or-global over `.codex/config.toml`, with the
  global path still honouring `$CODEX_HOME`.
- opencode: `projectOrGlobalPath("opencode.json", ".config/opencode/opencode.json")`.
- kilo: `projectOrGlobalPath("kilo.json", ".config/kilo/kilo.json")`.

`mcpScopeAware` then reports all five agents correctly, `orq setup` asks the scope
question on machines that only have those agents, and `orq connect codex mcp --local`
writes the file codex reads instead of warning that it cannot. The entry formats do not
change; the writers already emit `[mcp_servers.<name>] url = ...` and
`"mcp": {<name>: {"type": "remote", "url": ...}}`, and those are the project shapes too.
Codex's trust gate gets the manual-step line from §7.

## Out of scope

- Editing `.gitignore`, or writing codex's `[projects."…"] trust_level` entry.
- Per-repo generation snapshots or manifests.
- A scope flag on `orq launch`.
- Tying the local scope to the active orq project (RES-1497). That scopes credentials
  and workspace data; this scopes where files land. Two axes that share a word.
- Migrating an existing global install into a repo. `orq connect skills --local` in a repo
  that already has the global set simply adds the local one; both are recorded, both are
  removable.

## Tests

- `Targets`: local returns exactly `<cwd>/.claude/skills` and `<cwd>/.agents/skills` for
  the selected agents; global returns `~/.claude/skills` and `~/.agents/skills` and
  nothing else — no `~/.codex/skills`, no `~/.kimi-code/skills`, no XDG path on Linux.
  `~/.agents/skills` appears once however many shared readers are selected.
- `--local` from `$HOME` is refused for skills exactly as for mcp.
- The subdirectory warning fires inside a repo below its root, names kimi, and does not
  fire at the root or outside a repo.
- Bare `disconnect skills` removes both scopes at cwd and reports the count of local
  links left in other directories; `--local` and `--global` each leave the other alone.
- Launch links at cwd, joins an existing local install rather than reprojecting it, and
  releases on exit; at `$HOME` it links globally.
- `--status` shows `local`/`global` on skills rows; local rows only for the current cwd.
- An upgrade path: a manifest recording `~/.codex/skills` links from the old `ownDir`
  is cleaned up by the next `orq connect skills`.
- MCP round-trip for codex, opencode and kilo at the project path, leaving the global
  file byte-identical, plus the existing credential canary on the project file.
- One golden-output test for the `connect skills --local` summary, so the warning, the
  gitignore line and the trust lines are reviewed in a diff rather than by hand.
