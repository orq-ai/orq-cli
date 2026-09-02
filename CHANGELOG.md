# Changelog

All notable, user-visible changes to the `orq` CLI belong here: new commands,
removed or renamed commands and flags, output-format changes, and behavior
changes scripts could observe. Internal refactors do not.

## Stability contract

What you may depend on, and what you may not:

- **`--json` output on stdout is the machine contract.** Field names and
  structure follow the orq API response for the endpoint behind the command.
  Scripts should parse this and nothing else. Caveat on what CI enforces: the
  `surface.json` gate below covers command paths and flags only, not response
  field shapes. A renamed or dropped API response field flows through
  regeneration into `--json` with nothing in CI failing, so response
  field-shape changes are announced here by hand, not caught automatically.
  Fingerprinting response types per command so the gate covers them too is
  tracked in RES-1133.
- **TOON (the default terminal format) is presentation-only.** It exists for
  human readability and may change rendering between releases without notice.
  The `toon-go` dependency is pinned in `go.mod` and a golden-output test
  (`cli/custom/toon_golden_test.go`) makes any rendering change a deliberate,
  reviewed event rather than a silent one, but it is still not a contract.
- **Exit codes:** `0` success, `1` any failure, `130` interrupted (SIGINT),
  `143` terminated (SIGTERM). Two exceptions: `orq launch` and `orq orqi` run
  another program and propagate that program's exit status verbatim, so any
  value from `2` to `255` can come back — `127` when the child binary does not
  start, `128+signum` when it is killed by a signal (`137` for SIGKILL), and
  otherwise whatever the child itself returned. Scripts wrapping `orq launch`
  or `orq orqi` should treat any non-zero code as failure rather than matching
  on the four above.
- **Errors go to stderr; results go to stdout.**
- **The command surface is tracked in `surface.json`.** CI fails any change to
  commands or flags that is not consciously committed, so the surface cannot
  drift silently under an OpenAPI regeneration. Removing or renaming a
  command or flag is a breaking change: announce it here at least one release
  before it disappears.

## Versioning

The CLI has its own semver, independent of the orq API line (RES-1434). The line
starts at `5.0.0`: everything at `4.12.x`–`4.15.0-rc.x` was the orq API's number
rather than the CLI's, and starting above all of it means no release this line
ever cuts can collide with a version npm has already published.

The tag `v<version>` is the authority — the release pipeline creates it, and
writes the same number back to `VERSION` at the repo root so a checkout knows
what it last shipped. You never tag by hand, and hand edits to `VERSION` are for
one case only (below). A pre-release build of the next release is published on
the `rc` line, under that release's own number: an rc previewing `5.2.0` is
`5.2.0-rc.N`, and an rc previewing a patch is `5.1.4-rc.N`. The rc line carries
its own orq API schema, so its bump is resolved from that schema and can be
larger than the stable line's on the same commit.

### What moves the version

Every release takes the larger of two answers:

- **What the orq API did** — the CLI moves the same field the orq API version
  moved: their major is our major, their minor our minor, their patch our patch.
  The API is the contract these commands are generated from, so the size of
  their change is the size of ours. A schema republished at the same
  `app_version` contributes nothing above a patch.
- **What our own commits did** — read from their conventional-commit types since
  the last release: a `!` or a `BREAKING CHANGE:` footer earns a major, `feat:` a
  minor, everything else a patch. The type table is in `CLAUDE.md`, next to the
  commit rules, so it is read while the commit is being written.

So an orq patch release carrying a `feat:` of ours is a minor, and a `fix:` on
its own is a patch.

`VERSION` is the record of what was last released on this line — the pipeline
writes it on every release — and the bump is applied to it exactly once, rather
than to whatever the highest tag happens to be. Applying it to a tag range the
bump was itself derived from is what would count the same commits twice.

A `VERSION` with no tag of its own means the line has not started yet — that is
how `5.0.0` is published as `5.0.0` and not as `5.0.1` — or that someone is
forcing a number the rules would not reach on their own. That hand edit is the
one case for touching the file. The pipeline takes the next free tag from what
you write, so it is a floor rather than a promise — and it refuses to release at
all when what it resolves would sort below a version already published, which is
what a stale or mis-merged `VERSION` looks like.

### Release notes

The `## Unreleased` section below is the release notes. At release time the
pipeline renames it to the version being cut, opens a fresh empty one, commits
that back to `main`, and puts the section at the top of the GitHub release —
above the generated commit list, under the orq API line. An empty section is
left alone, and that release carries the generated list alone.

That is the whole reason entries are written per PR: nobody writes release notes
at release time, so they have to already exist.

The orq API version a build was generated against is recorded, not encoded:

- `orq --version` prints it under the CLI version, and `orq version` reports
  both plus the install method (`--json` for scripts).
- Every GitHub release's notes open with **Built against orq API <version>**.
- `orq doctor` carries it as `binary.api_version` in the structured report
  (`--json`) and in the `--report` bug-report body.
- `npm view @orq-ai/cli orqApiVersion` reads it off the published package.

A release is now cut for any change that reaches a binary, including
hand-written CLI code and `install.sh` — not only for an API schema bump, which
used to be the sole trigger and left CLI fixes unreleased until an unrelated
API version happened to land. The `surface.json` gate plus this file remain the
controls on surface changes, whichever side they originate from.

## Unreleased

## [6.1.0](https://github.com/orq-ai/orq-cli/releases/tag/v6.1.0) — 2026-09-02

- **Fixed: the key `orq setup` exports no longer shadows the session.** When
  `ORQ_API_KEY` holds exactly the key setup minted and wrote into `~/.orq/env`,
  and a session exists, the session wins — that key is ours, not a deliberate
  override, and letting it rank first made `orq workspace use` and `orq projects
  use` silent no-ops on every machine that had run setup and sourced the file.
  Any key we did not mint, and any key in a credentials profile, still wins.

- **Added: `orq setup` asks which project to work in**, between authenticating
  and creating the key. It skips itself when the workspace has one project or
  none, never offers to create one, and is skipped entirely on an `--api-key`
  run, which has no session to record the choice on. `--project <id|key|name>`
  pre-answers it and `--no-project` skips it.
- **Changed: the key `orq setup` mints is scoped to the selected project.** It
  is the credential the coding agents use for model calls, so a config file
  another program reads can no longer reach the rest of the workspace. A saved
  key belonging to a different project is replaced rather than reused. Keys
  minted before this stay workspace-wide until they expire. This does not
  affect the agents' MCP tools, which authenticate over OAuth.

- **Added: an active project.** `orq projects use <project>` selects one by id,
  key or name and persists it, and every later invocation mints its access
  token scoped to that project — the server then narrows both reads and creates
  to it, so `orq agents list` shows that project's agents and `orq agents
  create` lands there without a `--project-id` on every call. `orq projects
  use` with no argument prints the active project (or opens a picker at a
  terminal); `--clear` unsets it.
- **Added: a global `--project` flag (`ORQ_PROJECT`)**, a per-invocation
  override that takes a project id, key or name. Under an explicit API key
  there is no session token to narrow, so it fills the command's own
  `--project-id` instead; an explicit `--project-id` always wins.

- **Added: `orq status`**, aliased to `whoami`. It shows the active user,
  workspace, and project, plus which credential the next command will
  actually authenticate with and that credential's scope — all projects, or
  one. A key can outrank the session, so "signed in as X" on its own can
  describe a state no command runs in. The old hidden `orq whoami` keeps
  working. The `credential` object in `orq status --json` gains a `scope`
  field, which reads `unknown` for the opaque key shape rather than claiming
  workspace-wide reach it cannot see, and its `source` can now be `session`
  alongside the environment variable and named-profile answers it already
  gave. It is `null` only when there is neither a session nor a configured
  key — a state in which `orq status` reports that you are not logged in.
- **Added: `orq switch [workspace] [project]`**, which picks a workspace and
  then a project in one walk. It is two stages rather than one combined
  picker on purpose: the project list needs a token for the workspace it
  belongs to, so a single picker would mint a token for every workspace the
  user can see. Without a terminal it requires both halves named: it rewrites
  your whole identity, and guessing at either half would replace a
  deliberately chosen project with the workspace default and report success.
  To move workspace alone, `orq workspace use <workspace>` leaves the project
  untouched, and still re-asserts rather than failing when given no argument.
- **Fixed: `orq workspace use <other>` no longer leaves the previous
  workspace's project on the session.** The active project belongs to the
  workspace it was chosen in, so carrying it across a move left every later
  command asking for a token narrowed to a project the new workspace does not
  contain. The clear now lives in the one place both `workspace use` and
  `switch` go through, and only fires when the workspace actually changes —
  re-asserting the workspace already active keeps the chosen project.
- **Fixed: `orq status` names the credential of an `ORQ_TOKEN` or
  `ORQ_AUTHORIZATION` user.** It resolved the key from `ORQ_API_KEY` and the
  profile's `api_key` only, so those users got no `key:` line and a `null`
  `credential` while the session line was still printed — describing a state
  no command runs in. The `source` now names whichever of the three variables
  (or the profile) actually wins.
- **Changed: `orq status` marks the active project as inactive** when a
  credential outranks the session, instead of printing a project no command
  will narrow a token to.
- **Fixed: credential introspection now reads every token shape in
  circulation**, not just the opaque `sk-orq-<ULID>-<secret>` one. A
  workspace JWT (`workspace_id`, `key_id`) and a project JWT (`workspace_id`,
  `projects[]`, no `key_id`) previously reported as unknown, which is most
  keys in a real credentials file. The `sk-orq-` prefix is optional on the
  JWT shapes, since dashboard-issued keys arrive without it. This affects
  `orq status` and `orq doctor` only — the claims are unverified and never
  grant access.
- **Added: `orq doctor` and `orq setup` diagnose rejected project-scoped
  keys.** A project-scoped JWT has no `key_id`, so the diagnosis now falls
  back to matching the masked token the key-list endpoint returns
  (`eyJhbG********kK-d2A`) — without it, the diagnosis that explains a
  rejected key went silent for exactly the keys `orq setup` now mints. A
  mask whose visible head is too short to identify a single key is ignored
  rather than guessed at.
- **Changed: `orq doctor`'s gateway-key check reports the state that actually
  holds.** It used to warn that commands "will be refused" and tell you to
  unset `ORQ_API_KEY` even where the session already wins, which was false
  advice and a no-op remedy; that machine now gets a `pass` row. The warning
  is kept for a key we did not mint, and it now also fires when a minted key
  is exported with no session behind it.

## [6.0.0](https://github.com/orq-ai/orq-cli/releases/tag/v6.0.0) — 2026-09-02

- **Added:** `-o table` renders a list command as a table, and it is the default
  for a list at a terminal. Piped, redirected and explicitly formatted runs are
  unchanged — they still serialize to TOON, so no script's output moves. Pin a
  serialization with `orq default-format toon` to opt out; `orq default-format
  table` restores tables.
- **Added:** `--columns id,name` picks and orders the columns of a table.
- **Added:** an `orq auth profile` command group — `add`, `list`, `current`,
  `use`, `clear`. `auth add-profile` and `auth list-profiles` keep working.
- **Changed:** `orq auth add-profile` is still supported and works when invoked
  by name, but it is hidden from help output; use `orq auth profile add` to
  discover the current spelling.
- **Changed:** a credentials profile now outranks `ORQ_API_KEY` and `ORQ_SERVER`
  rather than losing to them, and a profile named with `--profile` that has no
  key is an error instead of a silent fall-through to whatever key is exported.
  If you saved an API-key profile once and now export `ORQ_API_KEY` for a
  different workspace, the saved profile wins and the exported key is ignored —
  the calls go to the profile's workspace, silently. `--profile ""` turns
  profiles off for a single call.
- **Changed:** the gateway key `orq setup` mints, its id and expiry, and the
  workspace it was minted for moved out of `profiles.<name>` in
  `credentials.json` into a `state` section of the same file. Existing files are
  migrated in place on the next command: the fields move, and a profile this
  CLI wrote that is left holding no API key is deleted. A keyless profile with
  none of those fields is someone else's and is left alone. If that profile was the selected one,
  `profile-selected` is also removed from `~/.orq/config.json`. A profile now
  exists only when it holds an API key, so a login that authenticates with a
  session no longer writes one that authenticates nothing.
  `orq auth list-profiles` output is unchanged. For now, trust that command
  when listing profiles: `orq auth profile list` reads Bartolo's `profiles` map
  alone and can omit a session-only login. A follow-up will make `--profile`
  mean an API key and key sessions by server.
- **Changed:** boolean query parameters are typed flags — `--include-budget`
  instead of `--include-budget=true`. Passing `true`/`false` explicitly still
  works (`--include-budget=false`).
- **Fixed:** `--verbose` redacts secrets at every depth of the config dump, not
  only at the top level.

## [5.3.0](https://github.com/orq-ai/orq-cli/releases/tag/v5.3.0) — 2026-09-02

- **Added:** `orq connect skills --local` installs the skill set into the current
  directory (`./.claude/skills` for Claude Code, `./.agents/skills` for every
  other agent) instead of `$HOME`; `--global` stays the default. A bare
  `orq disconnect skills` removes both scopes and counts local installs made
  from other directories. `orq connect --status`, `--json` and `orq doctor`
  label each skills directory `global` or `local` for the directory you run
  them from. `orq setup` asks the scope question when skills are selected.
- **Changed:** `orq launch` links session skills into the current directory, or
  into `$HOME` when launched from there. Kimi (launcher-owned home) and Windows
  (no session links) are unchanged.
- **Changed:** skills for codex and kimi are written to `~/.agents/skills` only,
  no longer to `~/.codex/skills` and `~/.kimi-code/skills` as well. Both agents
  read the shared directory, and codex listed every orq skill twice. The next
  `orq connect skills` removes the old links. The Linux-only
  `~/.config/agents/skills` directory, which no agent reads, is no longer written.
- **Added:** `orq connect <agent> mcp --local` writes a project config for codex
  (`.codex/config.toml`), opencode (`opencode.json`) and kilo (`kilo.json`)
  instead of warning and writing the machine-wide file. Codex loads its project
  config only for a repository marked trusted in `~/.codex/config.toml`;
  connect prints the line to add.
## [5.2.1](https://github.com/orq-ai/orq-cli/releases/tag/v5.2.1) — 2026-09-01

- **Changed:** `rc` releases are numbered as the stable release they preview.
  The resolver applied a second minor bump on top of the resolved stable target,
  so the rc line drifted a minor further ahead on every minor bump — `5.3.0-rc.5`
  was published while stable was on `5.1.3`, naming a version the stable line
  will never cut. An rc now carries the base of the release it previews.
- **Changed:** a `VERSION` that lags the highest published tag now fails the rc
  release too, not only the stable one. The rc channel used to invent the next
  minor above that tag. Fix `VERSION` and re-run.
- **Fixed:** a run that cuts both channels no longer resolves the two from the
  same `VERSION` and tag set. The rc is resolved last, from the release stable is
  about to cut, so it previews the release after that one — `5.3.0-rc.1` next to
  a `5.2.0` that came from a minor bump, rather than a `5.2.0-rc.1` that races
  the release it names and fails its own floor check depending on which job tags
  first. When the rc's own bump lagged stable's, the rc also resolved *under* the
  release being cut and failed change detection outright, taking the stable
  release down with it.
- **Fixed:** both places that look for the last rc tag — release notes, and the
  change detection that decides whether an rc is worth cutting — walk the commit
  graph instead of sorting version numbers. Sorting assumed rc numbers only ever
  move forward, which is no longer guaranteed: an rc is numbered as the release
  it previews, so a correction to that number can leave higher rc tags behind on
  the line, and notes anchored to one of those would re-report what an earlier rc
  already said.
- **Changed:** `cmd/release-version/` now counts as a release-worthy change on
  both channels. A fix to the resolver used to wait for an unrelated commit
  before it could take effect.

## [5.2.0](https://github.com/orq-ai/orq-cli/releases/tag/v5.2.0) — 2026-09-01

- **Added:** `orq orqi` runs [orqi](https://github.com/orq-ai/orqi), the orq.ai
  assistant in your terminal, and installs it first when it is missing — by
  running the orqi project's own `install.sh`, after asking, or refusing and
  printing the one-liner under `--no-input`. Your prompt and any orqi flags are
  passed straight through, so `orq orqi "why did my agent fail today?"` and
  `orq orqi --version` both reach orqi untouched. orq's own global flags go in
  front of the command word and orqi's behind it, so nothing orqi grows can
  collide with ours: `orq --profile staging orqi "why did it fail?"`.
  `--install` installs or reinstalls and exits without starting a session, and
  the binary lands in `$ORQI_INSTALL_DIR` or `~/.local/bin`. macOS (arm64,
  x86_64) and Linux x86_64 only.

## [5.1.5](https://github.com/orq-ai/orq-cli/releases/tag/v5.1.5) — 2026-09-01

- **Fixed:** the "update available" notice now appears on every human-facing
  run while an update is out, `orq --version` included, instead of only on the
  one run per day that refreshed the check. The 24h cache still bounds how
  often the npm registry is asked; it no longer bounds how often you are told.
  Suppression is unchanged: `ORQ_NO_UPDATE_CHECK`, `CI`, `--json`/`-o`,
  non-terminal output and `orq update` itself stay silent. (RES-1480)

## [5.1.1](https://github.com/orq-ai/orq-cli/releases/tag/v5.1.1) — 2026-08-28

- **Changed:** `orq launch` now follows your login's active workspace. When the
  exported `ORQ_API_KEY` is the key `orq setup` minted and your session has
  since moved to a different workspace (`orq workspace use`), launch uses the
  session's workspace token for that run and says so on stderr, instead of
  silently running against the workspace the key was minted for. Nothing on
  disk changes: the agent's own config, `~/.orq/env` and `credentials.json`
  are untouched. A key you brought yourself, or one whose workspace was never
  recorded, still wins exactly as before — CI and bring-your-own-key setups
  are unaffected.
- **Added:** `orq connect` records which workspace each agent was wired
  against (`agents.<id>` in `~/.orq/credentials.json` — workspace, key id and
  timestamp, no key material). `orq connect --status` shows it in a new
  WORKSPACE column, and `orq doctor` adds an info row naming the workspace an
  agent is pinned to when it differs from the active one, with the exact
  commands to move it. Agents wired by earlier versions have no record and
  show no workspace until the next `orq connect`.
- **Changed:** a failed session-token fetch during `orq launch` only advises
  `orq auth login` when a superseded `ORQ_API_KEY` was set aside for the
  session; on the plain session path the underlying error is reported
  unchanged, since network or server failures are not fixed by re-login.

- **Fixed:** `orq auth list-profiles` now uses the CLI's terminal table renderer
  for its default interactive view, with readable name, server and credential
  columns instead of the dense comma-separated TOON rows. The internal auth
  handler type is omitted from the human table. `--json`, `-o yaml` and
  explicit TOON output keep their structured formats.

## [5.0.0](https://github.com/orq-ai/orq-cli/releases/tag/v5.0.0) — 2026-08-28

- **Fixed (security):** path parameters are URL-escaped and rejected when
  empty. `orq datasets retrieve '../../etc/passwd'` used to be pasted into the
  request path verbatim, so a crafted id could traverse to a different endpoint
  and return data the command was never meant to reach (BACK-2115); it now
  requests `/v2/datasets/..%2F..%2Fetc%2Fpasswd` and 404s. An empty id, which
  previously built a collection URL — `orq agents delete ""` hitting
  `/v2/agents` — now fails client-side before any request. This lands through
  the bartolo generator (0.4.6 to 0.6.0), so it covers every generated command.
- **Changed (breaking):** generated DELETE commands now ask for confirmation
  and refuse to run without `--force` when there is no terminal, matching the
  `orq request DELETE` change below. 40 commands are affected — `orq agents
  delete`, `orq datasets delete`, `orq prompts delete` and the rest. CI jobs
  calling any of them exit non-zero until `--force` is added.

- **Fixed (security):** `~/.orq/credentials.json` is no longer written
  world-readable. On Unix it is now 0600 from the moment the file exists and is
  swapped in by rename, so there is no window in which the key is readable by
  other accounts, and an interrupted write leaves the previous file intact.
  Windows has no equivalent; the file inherits the directory's ACLs there.

  **Check your existing file: `ls -l ~/.orq/credentials.json`.** Earlier
  versions could leave it at 0644 permanently, not just briefly. `orq setup`
  chmodded the file after writing it, but `orq auth add-profile` did not chmod
  at all, so a file created by that command has been world-readable ever since,
  with the key in plaintext. This release does not repair an existing file
  automatically: it keeps whatever mode it has until the next successful save.
  If yours is 0644, run `chmod 600 ~/.orq/credentials.json` (or let
  `orq doctor --fix` do it, below), and treat any key in it as exposed to other
  accounts on that machine.
- **Added:** `orq doctor` runs the manual check the entry above asks for, on
  every run, over `~/.orq` itself, `credentials.json`, `env`, `env.fish`, each
  per-profile file under `sessions/`, and the legacy `session.json`. A finding
  names the path, its mode and the `chmod` that fixes it, plus what to do about
  the credential that leaked: revoke and replace an API key, `orq auth logout`
  for a session file. A symlinked path is judged on its target, since that is
  what the CLI reads, and the finding names that target alongside the path you
  know, so nothing is inspected or repaired off-screen. A clean run says
  nothing at all. Unix only.
- **Added:** `orq doctor --fix` chmods what that check flags — 0600 for files,
  0700 for directories — and reports each path it changed. Without the flag
  doctor still repairs nothing. A chmod that fails is reported as a failure
  naming the path and the error, and the advice stands after a repair: a chmod
  cannot un-expose a credential that was already readable. A `--fix` run that
  failed to repair something now exits `1` — it is an action, and a failed
  action must not report success to a script. This is covered by the existing
  exit-code contract (`1` any failure); nothing else about doctor's exit code
  changed, and a run that only *reports* findings still exits `0`. The check
  is Unix only — Windows ACLs do not map onto the bits it reads. `--fix` is
  accepted on every platform's command surface but rejected with a message on
  Windows, where there is nothing for it to change.
- **Added (security):** `orq setup` warns when the `~/.orq/env` file it is
  about to overwrite was group- or other-accessible. It has always chmodded a
  pre-existing env file to 0600, silently — which erased the only evidence
  doctor's check would have had, on a run started for some unrelated reason.
  The warning carries the same revoke-and-rotate advice doctor gives. Unix
  only.
- **Changed (security):** `orq auth list-profiles` masks stored credentials.
  It previously printed the full API key in plaintext, so keys reached terminal
  scrollback, CI logs and screen recordings. Keys now render as
  `sk-o********wxyz`.
- **Changed (breaking):** `orq auth list-profiles` output moved from a rendered
  ASCII table to the standard response formatter, so it honors `--json`, `-o
  yaml` and the default TOON format like every other command. It previously
  printed the table regardless of `--output-format`. Anything parsing that table
  needs updating; the masking above means the key can no longer be scraped from
  it at all.
- **Changed (breaking):** `orq request DELETE` now asks for confirmation, and
  refuses to run without `--force` when there is no terminal. A CI job doing a
  raw `orq request DELETE ...` exits non-zero until `--force` is added.
- **Changed:** `orq server list` no longer emits a per-entry `override` field;
  a top-level `overridden` replaces it. `orq server current` gains
  `profile_server` and `server_default`.
- **Added:** `orq auth add-profile --api-key-file` reads the key from a file,
  or from stdin with `-`, and its positional `<api-key>` becomes optional. A key
  passed as an argument is visible to every process on the machine through `ps`.
- **Added:** `orq request --force`, which skips the DELETE confirmation above.
- **Added:** the `mcp` capability returns to `orq setup`, `orq connect`,
  `orq disconnect`, `--status` and `--dry-run`, on OAuth. `orq connect claude mcp`
  writes the orq MCP server's URL into the agent's own config and nothing else —
  no key, no header, no bearer variable — and the agent logs in to that server
  itself; each write names the agent's login command. Supported for Claude Code,
  Codex, opencode, Kilo Code and Kimi Code; pi has no MCP support and says so
  rather than reporting a wire. `orq doctor` gains an MCP check group, which
  reports that an entry is present rather than claiming MCP works: the CLI holds
  no token and cannot know. This is the capability v4.13.10 wrote a
  workspace-admin key for, on a credential model that writes no credential.
- **Added:** `--global` and `--local` return to `orq connect` and
  `orq disconnect`, and `orq setup` asks where the MCP entry goes, defaulting to
  global. Only `mcp` has a project scope; a run that names `--local` alongside a
  machine-wide capability says which ones it could not scope rather than
  narrowing them. `--local` from your home directory is refused: `$HOME` is not a
  project, and the `~/.mcp.json` it would produce would follow you into every
  session started from home.
- **Changed:** `orq launch` wires the orq MCP server by default again;
  `--no-mcp` declines. It writes no credential — the agent authenticates by
  OAuth — and it no-ops when `orq connect` has already wired that agent, so a
  session entry cannot shadow the persisted one. MCP tool calls still share the
  free plan's daily request quota with model calls; `--no-mcp` is how you keep
  the quota for model calls.
- **Fixed:** `orq launch` no longer wires opencode or Kilo Code with an `oauth`
  value their config schema rejects. Both type the field as
  `McpOAuthConfig | false`, so the `true` this wrote made opencode refuse the
  whole config document and start with no MCP servers at all — including the
  ones you configured yourself.
- **Changed:** `orq auth logout --disconnect` now removes MCP entries and
  installed skills as well as gateway configuration, matching what a bare
  `orq connect` writes. The consent prompt is unchanged, and the preview lists
  every file before anything is removed.
- **Added:** `orq version`. Prints the CLI version, the orq API version the
  build was generated against, and the install method it was installed through
  (`installer`, `npm`, or `unknown`). `--json` emits `cli`, `api_version` and
  `install_method`. `orq --version` keeps `orq version <semver>` as its first
  line, so anything parsing that line is unaffected, and prints the API line
  under it — a script that reads the whole output rather than the first line
  will now see two lines.
- **Added:** `install.sh --channel rc` (or `ORQ_CLI_CHANNEL=rc`) installs the
  pre-release line instead of the stable one. The default is unchanged.
- **Deprecated:** the `--api-base-url` flag on `orq auth login`, `orq auth
  logout`, `orq whoami`, `orq workspace list`, `orq workspace use` and `orq
  doctor`. The CLI had two names for one value: those six commands took
  `--api-base-url` and rejected `--server`, while every generated command took
  the global `--server` and rejected `--api-base-url`. There is now one name —
  the global `--server <url>` (env: `ORQ_SERVER`), which works on every command,
  including `orq auth login --server https://orq.acme.internal`. Replace
  `--api-base-url <url>` with `--server <url>`; it is the same root URL. The old
  flag still works for one release: it is hidden from help and warns on stderr,
  and it will be removed in a following minor.
- **Changed:** the default host is `https://my.orq.ai`, the `servers[0]` entry
  of the API spec and already the fallback the generated commands used. Auth,
  `whoami`, `workspace` and `doctor` defaulted to `https://api.orq.ai` instead,
  so a run with no session and no override could reach two hosts at once. Both
  names answer the same routes from the same origin, so nothing moves for users
  on either. `orq launch`'s gateway defaults (`/v3/router`, `/v3/anthropic`,
  `/v2/mcp`) hang off that same host, so there is one host literal in the
  binary; they give way to the resolved server whenever it is not orq's own
  service under either of its two names.
- **Changed:** `--server` / `ORQ_SERVER` now reach `orq setup` and `orq launch`,
  and `orq doctor` reports where the host came from (`flag`, `env`, `config`,
  `session`, `default`) from the point the value was decided rather than by
  comparing it against the session. An explicit host also outranks the session
  on `orq setup` and `orq launch`, so `--server` diverts the configs they write
  instead of being overruled by the host the session was authenticated against.
  `setup` and `launch` previously read only `ORQ_API_BASE_URL`, so `--server`
  was silently ignored on both, and a coding agent could be wired to a
  different host than the one the CLI was talking to.
  On `orq launch` the flag goes before the agent name — either side of the
  `launch` word (`orq --server <url> launch claude`, `orq launch --server <url>
  claude`); everything after the agent name is forwarded to the agent.
- **Changed:** a profile now carries its own host and its own credentials, and
  both beat the wider setting. `orq auth login --server <url>` (and `orq setup`)
  bind the host to the profile they authenticate, so `orq --profile acme ...`
  routes to acme's backend with no flag and no session read; that binding
  outranks a host persisted globally with `orq server set`, which stays global.
  An explicitly typed `--profile` also outranks an exported `ORQ_API_KEY`,
  `ORQ_TOKEN` or `ORQ_AUTHORIZATION` — previously a key left in the shell was
  sent to whatever host the named profile resolved, silently. The CLI says on
  stderr when it overrides one. `ORQ_PROFILE` does not do this: env against env
  has no statement of intent to break the tie.
- **Deprecated:** `ORQ_API_BASE_URL`. It still resolves, now as a spelling of
  `ORQ_SERVER`, and prints a warning on stderr; it will be removed in a future
  release. It also reaches the generated API commands for the first time, which
  never honored it. Set `ORQ_SERVER` instead.

- **Changed:** the CLI version no longer tracks the orq API version. See
  [Versioning](#versioning) above. The first decoupled release is `5.0.0` — the
  first number above everything the old `4.12.x`–`4.15.0-rc.x` line ever
  published, so the sequence only ever moves forward and no future release can
  collide with a version npm already holds (npm never allows a version string to
  be reused, and a collision fails the publish mid-release). Nothing about a
  version number tells you the API line any more — `orq version` does.

  **If you installed through npm, this one release needs `npm install -g
  @orq-ai/cli@latest`.** `npm update -g` treats a global install as pinned to a
  caret range of the installed version, so a machine on `4.x` will report itself
  up to date forever rather than crossing into `5.x`. `orq update` and
  `install.sh` are unaffected — both resolve and install an exact version.
- **Changed:** `orq mcp-servers` and `orq mcp-gateways` now appear under **AI
  Gateway** in `orq --help`, where the docs put them. The commands themselves
  shipped in 4.14.0, from that release's orq API schema.
- **Changed:** `orq auth logout` exits non-zero when it fails to remove orq from
  a coding agent. It previously printed the failure and then reported success, so
  a script saw exit 0 while kimi's config still held the key. The `--json`
  payload gains `coding_agents_remove_failed`.
- **Added:** `orq update`. Replaces this binary with the latest published
  release through the install method it was installed with: npm installs get
  `npm install -g @orq-ai/cli@<version>`, install.sh installs re-run install.sh
  with `--version` set to that same version, and it verifies the release's
  published `.sha256` and swaps the binary in atomically. Both install methods
  are given the exact version the check resolved rather than resolving "newest"
  again, so what lands is what was reported — and an rc build updates along the
  rc line instead of being silently moved onto the older stable release. A
  binary that arrived any other way is refused, naming its path and both
  install methods' commands, rather than overwritten. `--check` reports
  current, latest and install method and changes nothing; `--json` carries
  `update_available` for scripts. npm's and the installer's own progress goes to
  stderr, so `--json` stdout stays parseable. A dev build refuses and says to rebuild.
- **Added:** update notice. Once every 24 hours, after a command has finished
  successfully, the CLI compares its own version against the npm dist-tag for
  its release line (`latest`, or `rc` for an rc build) and prints one stderr
  line naming the newer version and the command that installs it: `orq update`,
  or the `install.sh` one-liner when the binary arrived through an install method
  `orq update` cannot act on. The check is skipped entirely, with no network request, when
  `ORQ_NO_UPDATE_CHECK` or `CI` is set, when stdout is not a terminal, when
  `--json`/`-o` asked for a machine format, and for unstamped dev builds. Any
  failure or timeout is silent: the check can never fail a command, and never
  delays one by more than two seconds. The result is cached in
  `~/.orq/update-check.json`.
- **Changed (security):** the key `orq setup` and `orq connect` mint is now
  restricted to gateway permissions instead of every permission in the
  workspace. Keys minted by v4.13.10 carry `permission_mode: "all"`, which
  resolves to the whole capability catalog — including `member`, `billing`,
  `sso`, `group` and `workspace` — and is written in cleartext into coding-agent
  config files. The new key holds the server's router preset — the gateway
  domains plus read-only access to the model list. The exact set is chosen
  server-side by `source: "router"`, not by the CLI, so the dashboard's
  permission list is the source of truth. A call to any platform endpoint is
  refused with `insufficient_scope` (verified against a live key). **Existing keys are not
  changed, and re-running `orq setup` will not replace one** — it reuses whatever
  is saved. To move to a gateway-scoped key today: `orq auth logout`, log in
  again, then `orq setup`. Revoke the old key in the dashboard. A guided path for
  this is not built yet.
- **Changed:** minted keys are now named `orq-cli gateway <hostname>` rather than
  `orq-cli <hostname>`. The dashboard lists every restricted key as `Restricted`
  without saying restricted to what, so the name is the only place the purpose
  shows — and it leaves room for the MCP key that will sit beside it. Existing
  keys keep their old name until they are renewed.
- **Changed:** the minted key is stored as `gateway_key` in `credentials.json`,
  not `api_key`. A stored `api_key` takes precedence over your login for every
  generated command, so a gateway-scoped key there would have started refusing
  `orq prompts list` and friends. Commands now use your login session, and only
  agent configs get the gateway key. Keys you bring yourself via
  `orq login`/`--api-key` still go to `api_key` and still take precedence.
- **Changed:** `orq disconnect` output. The result line now reads
  `orq removed from kimi` rather than `kimi     gateway   removed from <path>`,
  which put the agent where the object of "removed" goes and read as though the
  agent itself had been uninstalled. The path appears once per file: in the
  preview when there is one, on the result line when there is not.
- **Changed:** the surviving-key notice is one line and leads with the fact that
  matters: `not revoked: the key still works. To revoke it: orq api-keys delete
  <id>`. It previously said the key was "untouched — still valid, and still
  saved for the next `orq connect`", mixing a convenience fact about local
  storage with a security one about the server, where the reassuring half is the
  half people read. Disconnect removes the wire; the key stays live in the
  workspace and still works anywhere else it was copied.
- **Added:** `orq auth logout` offers to remove orq from this machine's coding
  agents. Logout clears the credentials but never touched the copies already
  written into agent configs, and kimi's holds the key literally, so signing out
  left a working credential in a file and only printed a line telling you to run
  `orq disconnect` yourself. The offer defaults to **no**, is skipped without a
  TTY, and is suppressed (not auto-accepted) by `--yes`. Pass `--disconnect` to
  opt in from a script.
- **Changed:** logout no longer discards `gateway_key_id` and
  `gateway_key_expires_at`. Neither can authenticate anything, logout does not
  revoke the key server-side, and throwing them away destroyed the only record
  of the live key — so logout could not tell you what to revoke and `orq doctor`
  could not keep counting down to its expiry. The credentials themselves
  (`api_key`, `gateway_key`) are still cleared.
- **Added:** logout now prints `the gateway key is still active — revoke it
  with: orq api-keys delete <id>`, the line `orq disconnect` already showed. The
  more destructive command was the silent one.
- **Changed:** onboarding output is quieter. `orq setup` and `orq connect` no
  longer report the files they wrote (`credentials.json`, `~/.orq/env`, each
  agent's config path), the minted key's id, or the mechanics of which
  credential won and why. Those details moved to where someone looks for them:
  `orq doctor` and `orq connect --status`. Paths still appear inside commands
  you are meant to run, and `orq disconnect` still names every file before it
  removes it.
- **Changed:** the per-agent wiring line now names the gateway rather than the
  agent: `Orq AI Gateway configured for kimi  (137 models available)` in place of
  `kimi  gateway (137 models)  ~/.kimi-code/config.toml`, which read as though
  kimi supplied the gateway. The name matches the one the agent shows in its own
  model list.
- **Removed:** the `orq budgets create` suggestion printed after a mint. It
  carried the key id purely to be copy-pasteable; `orq api-keys list` has the id
  when you need it.
- **Fixed (security):** minting a gateway key now clears any `api_key` left in
  the profile. Versions before the split wrote the minted key to `api_key` with
  a workspace beside it, so an upgraded machine that switched workspace minted a
  new key while the old one survived — and because a stored `api_key` takes
  precedence, every `orq <entity>` command kept authenticating with a
  full-permission key scoped to the workspace you just left.
- **Fixed:** `orq doctor` no longer reports `authenticated: credentials.json`
  when the profile holds only a `gateway_key`. No command authenticates with
  that key, so the row claimed a working login on the one screen whose job is to
  explain why nothing works.
- **Added:** `orq doctor` warns when an agent config holds an older key than the
  one saved. Only kimi stores the credential literally, and a literal copy
  cannot follow a renewal, so an agent left out of the run that renewed kept a
  key that would die on the old expiry date while every row stayed green. Rewire
  with `orq connect kimi`.
- **Fixed:** `orq setup` no longer reports `Setup complete` when an agent was
  skipped rather than wired. Declining the mint, or a workspace with no models
  to offer, left the agent unwired under a green verdict; `setup_complete` is
  now `false` and the exit code is `1`, with the reason in the new `skipped`
  field of each agent result.
- **Fixed:** `orq connect --status` now applies the same two filters as `connect`
  and `disconnect`. `--status tracing` reported "nothing wired" on a wired
  machine, and `--status claude` reported claude as unwired when it has no
  provider config to wire. Both commands now also scope the empty verdict to the
  agents named: `nothing wired for codex` rather than a claim about the machine.
- **Changed:** minted keys now expire after 90 days instead of never. `orq setup`
  replaces one with under 30 days left; run `orq connect` afterwards to rewrite
  the agent configs. The superseded key is **not** revoked
  — it stays valid until its own expiry, so the cutover overlaps and an agent
  config that has not been rewritten yet keeps working. Keys minted before this
  release record no expiry and are never renewed automatically, and no command
  replaces them in place — see the note above.
- **Fixed:** `orq launch` no longer warns about a workspace mismatch on every
  run. Two causes, both introduced by the `gateway_key` split: the check read
  `api_key`, which is now empty, and the session token the CLI injects into
  `ORQ_API_KEY` was being compared against the saved key as though the user had
  exported it. The credential lookup now lives in one place (`auth.SavedAgentKey`)
  that `launch`, `setup` and `doctor` share.
- **Fixed:** `orq launch --mcp` warns again when your login predates MCP scopes.
  The CLI injects the session's own workspace token into `ORQ_API_KEY`, and
  `launch` was treating that as a key you had exported — which left it marked as
  "not from a session", so the scope check could never fire and the MCP server
  rejected the call instead with an unexplained `insufficient_scope`.
- **Fixed:** `orq setup --api-key <key>` is no longer silently overridden. A
  previously minted `gateway_key` outranks `api_key`, so the supplied key was
  used for that run and then ignored by the next `orq connect`. Saving an
  explicit key now clears the minted one.
- **Fixed:** `orq disconnect tracing` reported "nothing wired" on a wired
  machine; it now says tracing is not available yet, like `orq connect` does.
- **Fixed:** `orq connect --status <agent>` no longer reports agents you did not
  ask about, and `orq connect --help` no longer claims it never creates keys —
  an interactive run with no credential offers to mint one.
- **Fixed:** `orq setup --json` names its list `coding_agents`, matching
  `connect` and `disconnect`.
- **Fixed:** `orq auth logout` now clears the exported key from `~/.orq/env`
  (and `env.fish`). That file is written by `orq setup`, and a shell profile
  that sources it kept exporting a live key into every new shell after logout —
  so you were signed out while still authenticated everywhere. The file is
  emptied rather than deleted, because a profile carrying `. ~/.orq/env` would
  then error on every new shell. `--json` gains `env_files_cleared`.
- **Changed:** `orq connect --json` and `orq disconnect --json` name their list
  `coding_agents`, not `agents`. `agents` is orq's own entity — the Agents you
  build and invoke — so the old key made the output read like a listing of
  those. `disconnect`'s `removed` is now a list of capability names rather than
  a `+`-joined string, and the payload carries an `api_key` object naming the
  key that survived. Neither command has shipped, so nothing depends on the old
  shape.
- Added: `orq disconnect` now says that the API key survives — it removes the
  wiring, not the credential — and prints the `orq api-keys delete <id>` command
  that would revoke it. Removing the config used to read as "orq is off this
  machine" while the key stayed valid, saved, and exported.
- **Removed:** the gateway credit check, in `orq doctor` and in `orq setup`. It
  reported a zero balance as a problem, but a zero balance blocks nothing on its
  own: the gateway still serves calls when a provider key (BYOK) is connected,
  when the model is private, when the workspace carries the recently-created
  flag, or when the subscription has not disabled shared-key use — **which is
  the default**. None of those four are readable from the CLI, so the warning
  fired more often on working workspaces than broken ones. A workspace that
  genuinely cannot serve a call says so at the point of use, with a request id
  and a documentation link. **`gateway_funded` is gone from `orq setup --json`**,
  and the `gateway_funding` check is gone from `orq doctor --json`.
- Added: `orq doctor` counts down to the gateway key's expiry, warns under 30
  days, and fails once it has lapsed. Also warns when `ORQ_API_KEY` in your shell
  is the gateway-scoped key, which would make platform commands fail in a shell
  where your login would have worked.
- Added: after minting, `orq setup` prints a ready-to-run `orq budgets create`
  command scoped to the new key. It is a suggestion, never applied: a silent
  spend ceiling would stop an agent mid-task, and budgets are workspace-scoped,
  so non-admin members cannot create one anyway.
- Added: `orq connect` now says when an agent's config holds the key itself
  rather than a reference to `ORQ_API_KEY` — today only Kimi Code, whose TOML
  has no env indirection.
- **Removed (breaking):** MCP and skills support in `orq setup`, `orq connect`
  and `orq disconnect`. The `mcp` capability is no longer accepted, the skills
  option is gone from the capability prompt, and `disconnect` no longer removes
  MCP entries. An entry written by v4.13.10 stays until you delete it by hand —
  remove the `orq` key under `mcpServers` (or `mcp`) in `~/.claude.json`,
  `./.mcp.json`, `~/.kimi-code/mcp.json`, `~/.config/opencode/opencode.json`,
  `~/.config/kilo/kilo.json`, and the `[mcp_servers.orq]` table in codex's
  `config.toml`. `orq launch --mcp` is unaffected: it wires MCP for a single
  session using your login, and writes nothing to disk.
- **Removed (breaking):** `--global` and `--local` on `orq connect` and
  `orq disconnect`. No provider config is project-scoped — every agent reads its
  gateway configuration from one absolute path — so both flags had no effect
  once MCP was removed.
- **Changed:** `orq connect` no longer offers Claude Code, and `--status` no
  longer reports it as unwired. Claude reads its endpoint from the environment
  and has no gateway provider config, so with MCP gone there is nothing to
  write for it — `orq launch claude` is still the way to route it through orq.
  Naming it explicitly still works and explains itself.
- Added: `orq connect --status` — the read-only answer to "what is wired?":
  one line per wired capability with its file, one naming detected-but-unwired
  agents. No prompt, no auth, no writes, exit 0.
- Changed: `orq doctor` no longer warns about agents you simply have not
  connected. Healthy per-agent rows collapse into one `coding_agents` summary
  (`2 of 5 wired: kimi, opencode`); the state that breaks something — wired
  without `ORQ_API_KEY` in the shell — still gets its own warning row.
  `--json` keeps every per-agent row plus the summary.
- Changed: interactive `orq connect` with no saved key offers to log in (or
  mint a key when you are already signed in) and continues, instead of erroring
  after you finished selecting. Declining exits 0 with a hint; non-interactive
  and `--yes` runs keep the error and never create credentials.
- Added: `orq connect [agent...] [capability...]` and `orq disconnect` — wire
  coding agents to orq permanently, and remove exactly what was written.
  Capabilities are positional (`gateway`); no agents named means every
  detected agent, which both verbs ask about before
  acting: naming agents is intent, a bare run is not. Without a TTY they refuse
  rather than guess — name the agents or pass `--yes`. `disconnect` lists the
  files it would touch before asking (default no), takes `--dry-run`, and names
  the pre-orq backup when one exists. `tracing` is reserved vocabulary and says
  so when selected.
- Changed: `orq setup` owns the machine, not the agents. It authenticates,
  mints or reuses the key, and ends by offering to connect detected agents
  (a consent gate, then agent selection). Non-interactive runs
  wire nothing and print `Next: orq connect`. The unreleased
  `setup coding-agents` subcommand and the agent flags (`--coding-agent`,
  `--no-coding-agents`, `--no-mcp`, `--no-gateway`) moved to `connect` or were
  dropped; none of them ever shipped.
- Changed: `orq auth logout` warns when wired agents remain, naming
  `orq disconnect`.
- **Removed (breaking):** `orq setup --agent` and `orq setup --no-agent`, the
  v4.13 spellings, are gone. Use `--coding-agent` and `--no-coding-agents`.
  They were briefly kept as hidden aliases; removing them means a script still
  passing the old name fails at parse time rather than having the flag ignored.
- Added: `orq launch <agent>` — starts claude, codex, opencode, kilo, kimi or
  pi preconfigured to route every model call through the orq AI Router, with
  `--dry-run`, `--model`, and `--mcp` to wire the orq MCP server.
  Per-invocation only: nothing is written to your agent's own configuration.
- **Changed:** `orq launch` runs locally only. `--sandbox`, `--mount-cwd`,
  `--rebuild` and `--local` are gone, along with the Docker image build and the
  "local execution" safety prompt — with no sandbox to switch to, a prompt
  offering one option was a keystroke tax. The names now belong to the agent,
  which fixes a collision: codex's own `--sandbox <mode>` is reachable again.
  Launcher flags are still recognised only before the first agent-owned
  argument, so put them first: `orq launch codex --dry-run --sandbox read-only`.
  Sandboxing returns as its own piece of work.
- **Changed (breaking):** `orq launch --mcp` is opt-in. The orq MCP server used
  to be wired on every launch with `--no-mcp` to decline; it is now off unless
  you pass `--mcp`, and `--no-mcp` is accepted but does nothing. The skills
  plugin follows MCP, so it is off by default too. A wrapper relying on the old
  default gets an agent with no orq tools and no error.
- **Changed:** the API key `orq setup` mints is owned by you, not by a service
  account. Service accounts can only be created by workspace admins, so the old
  behaviour failed for Developer and Researcher members of an Enterprise
  workspace. The consequence worth knowing: a user-owned key is revoked when
  that user leaves the organisation, where a service-account key outlived them.
  Runs with no session, or a session carrying no user record, still mint against
  a service account.
- Added: `orq setup` — signs you in, creates (or reuses) a workspace API key,
  and registers the orq MCP server and gateway provider in the config files of
  the coding agents it detects. Unlike `orq launch`, these writes are
  persistent. It does not ask about projects: API keys are workspace-scoped.
  `pi` is wired gateway only — an `orq` provider merged into its agent
  directory's `models.json` (`$PI_CODING_AGENT_DIR`, or `~/.pi/agent`), reached
  with `pi --model orq/<model>` — because pi has no native MCP.
- Fixed: declining "Create a workspace API key now?" no longer wires the
  session's expiring access token into agent configs. kimi embeds the
  credential literally, so the token landed in `~/.kimi-code/config.toml`,
  worked for under an hour, then failed every prompt with a 401. The provider
  write is now skipped with a warning until a durable key exists.
- Changed: when `ORQ_API_KEY` in the environment points at a different
  workspace than your login, setup says which workspace wins; the note no
  longer appears when both point at the same place. After MCP wiring, setup
  suggests the agent-skills install, and the step-3 agent rows drop the
  scope-note suffix.
- Changed: the API key `orq setup` saves is reused only for the workspace it
  was minted in. Running setup against a different workspace (`--workspace`, or
  the interactive picker) mints a fresh key for it instead of silently wiring
  every agent config to the old one; `orq connect` refuses in that
  situation, since it never creates keys. Keys saved by earlier builds, and keys
  supplied with `--api-key`, carry no workspace record — their provenance is
  unknowable, so they are reused as before rather than refused. `orq setup
  coding-agents --api-key` no longer saves the key it was handed: it is used for
  that run only, leaving the credential every other command authenticates with
  untouched.
- Removed: `orq setup` no longer writes `ORQ_API_KEY` into `./.env`, and the
  `--no-env` flag is gone with it. The key lives in `~/.orq/credentials.json`
  for the CLI and `~/.orq/env` for your agents: configs reference the
  `ORQ_API_KEY` environment variable, which that file exports (kimi is the
  exception — its config carries the key literally). An existing `./.env` keeps working — and keeps taking
  precedence over your login, including after `orq auth logout`, which is the
  behaviour that made writing one a poor default.
  **--json field change:** `orq setup` no longer emits `api_key.env_file`.
- Changed: `orq setup` no longer sends a model call. It used to spend one billed
  completion to prove the gateway answers, which also opened a trace in your
  workspace, for a request you did not make, often as the first thing a new
  account ever saw in Traces. The default model is chosen from the catalogue
  ranking instead, and the first real agent request is the test. `orq doctor`
  reports gateway funding for free.
- Changed: `orq setup` asks about credits only when it is wiring the gateway,
  and only on a deployment with a known dashboard. Choosing "MCP tools only",
  passing `--no-coding-agents`, or running against a self-hosted install now
  says nothing about credits. The balance is irrelevant to all three, and the
  gateway does not meter on-premise deployments at all. The wording states the
  rule rather than asserting a state the CLI cannot read: a zero balance still
  serves calls through a BYOK provider key, a private model, or a subscription
  that has not disabled shared-key use.
- Added: `orq doctor` reports coding-agent wiring per detected agent, and warns
  when an agent is wired but `ORQ_API_KEY` is absent from the current shell —
  the state every agent launched from a terminal older than your last `orq
  setup` is in, and one that looks like a broken install from inside the agent.
  The warning names what actually breaks: for kimi, whose provider config holds
  the key literally, model calls keep working and only the MCP tools fail.
  It also reports gateway funding when a login session is present. Detected but
  unwired is a warning, never a failure, so the exit code stays 0.
  **--json field change:** `checks[]` gains `coding_agent_<id>` entries and,
  with a session, `gateway_funding`.
- **--json field change:** `orq setup` gains `gateway_funded`, a string of
  `funded`, `unfunded` or `unknown`. Not a boolean: "nobody asked" is a normal
  outcome now that the balance is only read when the gateway is wired, and a
  script testing truthiness would read it as "cannot pay". `models_enabled` is
  omitted entirely on runs that never listed the catalogue, rather than
  reported as zero.
- Fixed: `orq setup` no longer ends in an unexplained gateway error on a
  workspace that cannot serve model calls. It says so once, with the links that
  fix it, and still wires everything that works without funding, exiting 0
  because nothing is broken. The "check again" retry prompt is gone with it.
- Fixed: the "connect a provider" link pointed at a page that does not exist.
  Dashboard links are now workspace-scoped.
- Fixed: `orq launch` sends model calls to the gateway the session authenticated
  against, deriving it from the API base. On an on-prem deployment every agent
  except claude wired its MCP server to the customer's host while sending
  prompts and file contents to the public api.orq.ai, using a key that host had
  issued. Explicit `--base-url`, `ORQ_GATEWAY_URL` and the per-agent env vars
  still take precedence.
- Changed: `orq setup` writes every chat model the workspace has enabled into an
  agent's provider config, instead of four hand-picked families, matching what
  `orq launch` already does. It also fills `default_model` when the config has
  none — never replacing one, since agents persist the user's own pick there.
- Changed: `orq setup` makes **no** billed completions. It briefly made five,
  then one; it now makes none at all, for the reasons in the entry above. Model
  selection and gateway verification used to probe separately, and probing
  existed to compensate for selecting from the whole catalogue rather than the
  workspace's enabled set, which is no longer how the default is chosen.
- Added: `orq setup -y/--yes` — answer yes to every confirmation instead of
  being asked. `orq setup` now asks before registering the orq MCP server in an
  agent's config, since that writes into files you own and grants the agent
  read/write access to your workspace; `--yes` is the auto-approve, `--no-mcp`
  the decline, and `--no-input` still never prompts.
- Added: `orq setup --no-mcp` — instrument agents for gateway routing without
  registering the orq MCP server, for anyone who wants their model calls routed
  through orq but does not want the agent reading and writing their workspace.
  `orq setup -i` asks the same question interactively.
- Added: `orq man-pages` output and a `.sha256` published next to every release
  asset, which `install.sh` now verifies.
- Changed: `orq auth logout` also clears the stored API-key profile, not just
  the session. Previously a "logged out" CLI kept authenticating from
  `credentials.json`. **`--json` field change:** the payload gains
  `api_key_profile_cleared`, and in the no-session case `cleared` is now `true`
  when a stored key was removed (it was always `false`).
- Changed: `orq doctor` reports authentication from `ORQ_API_KEY` or
  `credentials.json` instead of reporting "missing" when only those are present.
- Fixed: API-key profiles written by `orq setup` carry an auth type the CLI can
  resolve. Profiles written by earlier builds were unusable — every generated
  command failed with "no authentication handler configured" — and are now
  repaired on read.

### Known gap in the surface gate

`orq launch` sets `DisableFlagParsing` so that agent arguments can be forwarded
without collisions. Global `orq` flags must appear before the agent name; its
launcher-specific flags are parsed by hand at the start of the agent arguments
and therefore do not
appear in `surface.json`, so the CI gate covers the seven `orq launch` command
paths but **not** their flags: renaming or removing `--sandbox`, `--dry-run`,
`--model`, `--mount-cwd`, `--rebuild`, `--mcp`, `--no-skills` or
`--no-fetch-models` will not fail CI. Treat them as covered by the stability
contract anyway and announce changes here by hand, exactly as with `--json`
response field shapes.

- Added: `surface.json` command-surface manifest and CI gate; changes to the
  command tree now fail CI until the manifest is regenerated and reviewed
  (`go run ./cmd/surface-dump -write`).
- Added: CI workflow running build, vet, gofmt, tests, and the surface gate on
  every PR for both modules.
- Added: this changelog and the stability contract above.

## Earlier

Releases before this file existed are documented by their GitHub Releases and
tags only.
