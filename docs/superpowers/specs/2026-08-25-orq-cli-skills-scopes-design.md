# orq CLI: `--local` and `--global` scopes for skills

Date: 2026-08-25
Status: proposed design, not yet implemented
Ticket: RES-1437
Assumes: skills and MCP are both live capabilities (`availableCapabilities()` returns
`gateway, skills, mcp`), i.e. this design sits on top of RES-1435's MCP wiring
(`Baukebrenninkmeijer/cli-mcp-support`) and PR #31 (`arianpasquali/skills-safety-fixes`).

## Problem

Skills install to the agent's home directory and nowhere else. `orq connect skills` and
`orq launch` both resolve targets through `skills.Targets()` in
`cli/custom/skills/targets.go`, which returns `~/.claude/skills`, `~/.codex/skills`,
`~/.kimi-code/skills` and the shared `~/.agents/skills`. A user cannot scope a skill set
to one repo, and every repo on the machine sees the same set. MCP is getting a
`--local` / `--global` split (RES-1435 §3); skills needs the matching one.

**How to see it:** `orq connect skills` from inside any repo, then `ls ~/.claude/skills` —
the links are in `$HOME`, not the repo. No flag changes this.

## Which agents actually have a project scope

Verified on this machine against the installed agents (codex-cli 0.144.6, pi, kimi) and
their shipped docs; opencode and kilo were not installed and are V2 below.

| Agent | Home dir (today) | Project dir | Evidence |
| --- | --- | --- | --- |
| claude | `~/.claude/skills` | `./.claude/skills` | documented Claude Code behaviour; corroborated by kimi's migration doc, which reads `<project root>/.claude/skills/` |
| pi | `~/.agents/skills` (shared) | `./.agents/skills`, walking ancestors to the git root | `pi-coding-agent/docs/sdk.md:348`, `docs/security.md:14` |
| kimi | `~/.kimi-code/skills` | `./.kimi-code/skills` | kimi bundle discovers "user and project directories"; migration doc maps project skills to `<project root>/.kimi-code/skills/` |
| codex | `~/.codex/skills` | unconfirmed — binary carries `/.codex/skills`, `.agents/skills` and a `SkillsExtraRootsSet` RPC | V1 |
| opencode, kilo | `~/.agents/skills` (shared) | unconfirmed | V2 |

So at least three of six have a real project scope, and the shared agents-spec directory
(`.agents/skills`) has a defined project form that pi already walks. That is enough to
build the split; agents that turn out to be home-only fall back the way the MCP design
handles them — warn, write global.

## Design

### 1. Two flags on connect and disconnect, not on launch

`--local` and `--global` land on `orq connect` and `orq disconnect`, the same two
commands RES-1435 puts them on, and mean the same thing for skills as for MCP: which
directory receives the wiring. Default stays `--global`, which is today's behaviour.

`orq launch --local` keeps its unrelated RES-1349 meaning (skip the sandbox safety
prompt) and gains no scope flag. The collision is real but confined to a command that
does not take a scope at all, and inventing a third word (`--project`) for connect would
make skills disagree with MCP on the same flag surface. Consistency wins; `launch --help`
should say what its `--local` means so the two do not read as one flag.

Naming a scope on a run that includes no scope-capable capability warns rather than
silently doing nothing — same rule as RES-1435 §3.

### 2. One resolver, scope as a parameter

`Targets(agents []string)` becomes `Targets(agents []string, scope Scope)` with
`ScopeGlobal` and `ScopeLocal`. Local resolution reuses the existing structure:

- `ownDir` gains a project sibling: claude → `.claude/skills`, kimi → `.kimi-code/skills`,
  codex → `.codex/skills` (pending V1).
- shared readers → `.agents/skills`.
- All local paths are anchored at the **git root** when the cwd is inside a repo, and at
  the cwd otherwise. Claude and kimi both read from the project root, and pi walks
  ancestors to the git root, so anchoring at the root is the only choice all three see
  from a subdirectory.
- An agent with no project directory warns and is skipped for a `--local` run — it does
  *not* silently fall back to writing `$HOME`. The MCP design writes global in that case
  because an MCP entry has to exist somewhere for the agent to work at all; a skipped
  skill install just leaves the machine as it was.
- The Linux XDG branch for kilo is global-only. `$XDG_CONFIG_HOME` is a home location by
  definition and has no project form.

### 3. One manifest, with a scope on each link

Local installs record into the same `~/.orq/materialized-skills.json`. `Link` gains
`Scope string` (empty means global, so existing manifests stay version 1 and
`manifestVersion` does not move).

The alternative — a manifest per repo — forks the deletion allow-list and the manifest
lock, which PR #31 has just spent four commits making correct. Nothing is gained: paths
in the manifest are already absolute, `OwnedPaths()` is already the allow-list, and a
single file is what lets `orq connect --status` from any directory answer "what has this
CLI put on this machine" instead of only "what is under my cwd".

`disconnect --local` removes links whose `Scope` is local *and* whose path is under the
current anchor. `disconnect --global` removes only global links. A bare `disconnect`
removes everything the manifest owns, as it does now.

### 4. The snapshot stays global; local installs are not committable

Local installs project out of the same generation snapshot under `~/.orq/snapshot/`, so
on Unix a repo-local skill is a symlink into `$HOME` — worthless to anyone else who
checks the repo out, and a dangling link once generation collection retires the snapshot.

So: a local install prints one line telling the user to gitignore the directory, and does
not edit `.gitignore` itself. Writing to a user's tracked `.gitignore` is not something a
connect command should do unasked. On Windows, copy mode produces real directories that
*could* be committed; the same line is printed and the same advice holds, because
committing a materialized copy pins a generation the CLI will later expect to own.

### 5. `orq launch` inside a repo: unchanged

Session skills stay global. Launch's session links already have their own claim,
heartbeat and sweep machinery in `session.go`; giving them a second, cwd-dependent target
set doubles that surface to solve a problem nobody has stated — a session install is
already invisible to every other repo, because it disappears when the session ends.

If a repo has a local install, launch finds it in place through the agent's own
discovery, and the existing "somebody else's directory is used as it stands" branch
already leaves it alone.

### 6. Status and doctor

`orq connect --status` prints the scope next to each skills row, and reports local
installs for the current anchor only (a global CLI listing every repo on the machine it
has ever touched is noise). `doctor`'s `skillsCheck()` gains the same treatment: a local
install whose links are missing is a `warn` naming `orq connect skills --local`.

## Verify before implementing

**V1 — codex project skills.** The codex binary carries `/.codex/skills`, `.agents/skills`
and a `SkillsExtraRootsSet` RPC, but `codex --help` documents no skills surface at all.
Confirm whether codex 0.144 reads `<project>/.codex/skills`, and whether it reads
`~/.agents/skills` as well — if it does, `ownDir` is writing codex a second copy of every
skill it already sees through the shared directory.

**V2 — opencode and kilo project directories.** Neither is installed here. Confirm each
reads `./.agents/skills`, or names its own project directory (opencode is documented
elsewhere as using `.opencode/skill/`, singular, which would not be the shared shape).

**V3 — kimi double indexing.** kimi's bundle references `.agents/skills`, `.claude/skills`,
`.codex/skills`, `.kimi-code/skills` and `.kimi/skills`. If it reads the shared directory
as well as its own, today's global install already puts every skill in kimi's index twice
— which is the exact failure `sharedReaders` exists to prevent, on an agent that is not in
that map.

**V4 — anchoring from a subdirectory.** Install locally from `repo/sub/dir` and confirm
claude, pi and kimi each pick the skills up from the git root.

## Out of scope

- Editing `.gitignore`.
- Per-repo generation snapshots.
- A local scope for `orq launch` session skills.
- Migrating an existing global install into a repo (`orq connect skills --local` in a repo
  that already has the global set installed simply adds the local one; both are recorded,
  both are removable).
