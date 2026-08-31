# orq CLI: one command to install and run orqi

Date: 2026-08-31
Status: approved design, reviewed, ready for an implementation plan
Ticket: RES-1475
Related: RES-1241 (orqi v1), RES-1476 (orqi Windows support), RES-1431 (orqi via Homebrew and npm)

## Problem

orqi (aka TonyBot) is the orq.ai workspace assistant, shipped as its own binary from
[orq-ai/orqi](https://github.com/orq-ai/orqi). It already depends on this CLI: it reads the
login session at `~/.orq/sessions/<profile>.json` and shells out to `orq` for `/whoami`,
`/workspace` and `/doctor`.

The dependency runs one way only. Nothing in the CLI mentions orqi, so a user who has `orq`
installed and is already authenticated still has to find the repo, copy a `curl … | sh`
one-liner out of its README, and hope `~/.local/bin` is on their PATH. Discovery of the
assistant depends on someone linking the repo in Slack.

## Why not `orq launch orqi`

`launch` exists to configure a third-party agent for one session: it writes a models.json or
equivalent, resolves `--model` against the workspace catalogue, wires MCP, and links session
skills. orqi does all of that for itself, in-process, from the same login session. A launch
entry would be a near-empty `Resolve` advertising flags that do nothing (`--model`, `--models`,
`--no-mcp`, `--no-skills`), plus a second source of truth for wiring that orqi already owns —
which is the thing v1 moved away from when it stopped being `orq launch pi` (RES-1241).

So: a top-level command, not a launch agent. It still borrows the launch package's argv
convention and its child-process runner, which are agent-agnostic.

## Design

One new file, `cli/custom/commands/orqi.go`, plus registration.

### Command surface

`orq orqi`, registered with `DisableFlagParsing: true` and group `getting-started`.

`DisableFlagParsing` means **cobra parses nothing on this line** — not the wrapper's own flags,
and not the root's persistent `--profile`, wherever it appears. `cmd.Root().PersistentFlags()`
is never populated, so reading `.Changed` on it would always report false. The one existing
command family with this cobra shape, `orq launch <agent>`, solves it in
`cli/custom/launch/args.go`, and this command follows the same convention:

> Wrapper-owned flags are recognised only at the FRONT of argv. The first argument the wrapper
> does not own ends wrapper parsing, and everything from there on belongs to orqi verbatim. A
> leading `--` ends wrapper parsing explicitly; any later `--` is orqi's.

Four flags are wrapper-owned:

| Flag | Effect |
| --- | --- |
| `-h`, `--help` | Print this command's help and exit 0. Never triggers an install. |
| `--install` | Install or reinstall, then exit without starting a session. Terminal: any argument after it is an error, not a silently dropped passthrough. |
| `--no-input` | Never prompt. Also honoured from `ORQ_NO_INPUT` via viper, as everywhere else. |
| `--profile <name>` | Which login session orqi should use. Also accepts `--profile=<name>`. |

Because cobra strips only the subcommand name before dispatch, `orq --profile staging orqi`
arrives as `["--profile", "staging", …]` — at the front of argv, where the scanner sees it.
Typing it after a prompt (`orq orqi "why did it fail" --profile staging`) does not select a
profile; it is orqi's argument, by the same rule that keeps orqi's own future flags reachable.

Everything else passes through untouched, so `orq orqi "why did my agent fail today?"` and
`orq orqi --version` reach the child unchanged.

`ValidArgsFunction` advertises the four flags for shell completion, mirroring
`launch.CompletionFlags`, with a test asserting the completion list and the scanner agree —
`TestCompletionFlagsMatchParser` in `cli/custom/launch/args_test.go` is the model.

**Not reusing `launch.ParseArgv` itself.** Its flag set and its `GatewayFlags` return value are
gateway concerns (`--model`, `--base-url`, `--no-fetch-models`, `--mcp`); generalising it would
churn six agents to serve one caller that shares none of its flags. The convention is copied;
the function is not.

### Resolving the binary

The install location and PATH are different questions, and the command must not confuse them.
`install.sh` writes to `~/.local/bin` and only *prints* a PATH hint — it edits no shell rc. A
user who does not act on that hint has a working orqi that `exec.LookPath` cannot see, and a
`LookPath`-only design would then re-offer the install on every subsequent run, or, under
`--no-input`, report "not installed" for a machine where it plainly is.

So resolution checks two places, in order:

1. `exec.LookPath("orqi")`.
2. `<install dir>/orqi`, stat'd directly — where install dir is `ORQI_INSTALL_DIR` if the user
   exported one, else `~/.local/bin`, install.sh's own default.

Found either way → run it. Not found → install.

`--install` installs into the directory of an already-resolved binary when there is one, and
into the default otherwise. Blindly installing into `~/.local/bin` would fork a second copy for
anyone whose orqi came from source or, later, from Homebrew (RES-1431), leaving two binaries
where PATH order decides which one runs and `--install` maintains the other.

### Prompting

Reuse the CLI's one prompt gate. `hasInteractiveTTY()` (`cli/custom/commands/prompts.go`)
already folds `--no-input`, `ORQ_NO_INPUT` and both isatty checks into a single predicate, and
`promptStdio()` is why prompts go to stderr rather than into a redirected stdout.

- **Interactive** — `survey.Confirm{Message: "orqi is not installed. Install it now?", Default: true}`
  through `promptStdio()`. Declining exits 0 having done nothing.
- **Not interactive** — no prompt. Print the same "not on PATH, install it with:" shape
  `launch.Run` uses for a missing agent binary (`run.go`), naming orqi's one-liner, and exit 1.

### Installing

`updateViaInstaller` (`cli/custom/commands/update.go`) already is this flow, and its comment
already records why it downloads to a file rather than piping into a shell. Do not re-derive
that reasoning here; follow the code.

1. `exec.LookPath` for `curl` and `sh`, erroring with the missing binary's name — the same
   preflight `updateViaInstaller` runs. `GOOS != "windows"` is not a check that `sh` exists.
2. `os.MkdirTemp`, `defer os.RemoveAll(dir)`, so cleanup covers a failed download and a failed
   install as well as the happy path.
3. `curl -fsSL -o <dir>/install.sh https://raw.githubusercontent.com/orq-ai/orqi/main/install.sh`,
   through the same `runUpdateCommand`-style seam. Using `curl` rather than Go's http client
   keeps one proxy and cert-store story for every download this CLI makes, including the
   tarball `install.sh` itself fetches.
4. `sh <dir>/install.sh`, with `ORQI_INSTALL_DIR` set in the child environment to the directory
   resolved above, and stdio inherited so the installer's banner, progress bar and PATH hint
   reach the user.

Child stdout is routed to stderr, as `orq update` does: installer output is diagnostics.

The script is fetched from `main`, unpinned, and is not checksum-verified. Two reasons, both
worth revisiting when they stop being true: orqi publishes no stable installer alias to pin to
(this CLI has `cli.orq.ai/install.sh`; orqi has nothing equivalent), and `install.sh` verifies
the artifact that matters by running `orqi --version` before reporting success. Pinning belongs
with RES-1431, which is where orqi's distribution and signing story lives.

### Starting it

`launch.RunChild(path, args, env)` already owns the exit-code contract: 0 ok, 1 command error,
130 SIGINT, 143 SIGTERM, and the child's own code propagated verbatim. `path` is the resolved
binary, never a bare `"orqi"`, so a fresh install runs even before the shell's PATH catches up.

`env` is the parent environment plus `ORQ_PROFILE`, set from the scanned `--profile` or, when
the flag was absent, left alone so orqi resolves the profile itself exactly as it does today.

The parent environment already carries whatever `installSessionPreRun` injected into
`ORQ_API_KEY` (`cli/custom/register.go`), and the child inherits it. That is intentional: it is
the same session credential orqi would read from `~/.orq/sessions` on its own. Unlike
`launch.Run`, this command prints no shadow-warning when an exported `ORQ_API_KEY` disagrees
with the login session, because orqi resolves and reports its own credential source at startup
and through `/whoami`.

The command does not check whether the user is authenticated. orqi has its own credential
ladder and its own `LOGIN_HINT` ("Run `orq auth login` … or export a valid `ORQ_API_KEY`"), and
duplicating that check here would put two sources of truth on the one thing orqi already does
well.

`orq orqi` has no `--json` mode of its own: stdout belongs to the child from the moment it
starts, and the wrapper's own output is either a prompt or an error, both on stderr.

### Unsupported platforms

orqi ships macOS arm64, macOS x64 and linux x64 only. The command checks `runtime.GOOS` and
`runtime.GOARCH` against that set and fails immediately — before any lookup, prompt or download
— naming the supported platforms. Windows additionally has no `sh` to run `install.sh` with;
Windows support is RES-1476. Linux arm64 gets the same early, cheap refusal rather than a
prompt, a download and then install.sh's own error.

### Registration

Three things outside the new file, each of which fails loudly if forgotten:

- `commandGroup` in `cli/custom/groups.go` — `"orqi": groupGetStarted`, or `groups_test.go`
  fails.
- `profileExemptCommands` in `cli/custom/register.go` — the command never calls the orq API
  itself, so it must work before a session exists.
- `surface.json` — regenerated with `go run ./cmd/surface-dump -write` and committed, or CI's
  surface gate fails.

The command ships on both module lines automatically: it lives under `cli/custom/`, which
`packages/orq-rc` reaches through `replace orq => ../..`.

## Testing

Package-level overridable seams, in the style of `runUpdateCommand` in `update.go`: the binary
lookup, the installer commands (`curl` and `sh`), and the child launch. Tests never touch the
network, the real installer, or a real orqi.

| Case | Expected |
| --- | --- |
| orqi on PATH | no fetch, no install; child run with the arguments verbatim |
| Not on PATH, present at the install dir | runs it; no prompt, no fetch, no exit 1 |
| Missing, prompt answered yes | fetch, then install, then exec the resolved path |
| Missing, prompt answered no | exit 0, nothing fetched |
| Missing, `--no-input` | exit 1, nothing fetched, the install one-liner printed to stderr |
| `--help`, orqi missing | wrapper help, exit 0, no prompt and no fetch |
| `--install`, orqi present elsewhere on PATH | installs into that binary's directory, not the default |
| `--install` with trailing arguments | error; nothing installed |
| `--profile staging` at the front of argv | child env carries `ORQ_PROFILE=staging` |
| `--profile` after a passthrough argument | reaches the child as an argument; `ORQ_PROFILE` unset |
| No profile flag | child env does not set `ORQ_PROFILE` |
| `--` before a flag the wrapper owns | flag reaches the child; wrapper does not act on it |
| `curl` or `sh` missing from PATH | error naming the missing binary; nothing fetched |
| Download fails | error naming the URL; temp dir removed; no install attempted |
| Installer exits non-zero | error carrying the installer's status; temp dir removed; no session started |
| Installer output | written to stderr, not stdout |
| Unsupported GOOS/GOARCH | error naming the supported platforms; no lookup, no prompt, no fetch |
| Child exits 42 | `orq` exits 42 |

Plus the completion test: every flag the scanner consumes is advertised by `ValidArgsFunction`.

## Out of scope

- Teaching `orq doctor` about orqi, and making orqi an `orq connect` target. Both are known
  gaps carried forward from RES-1241 and belong in their own tickets.
- Updating an already-installed orqi. `--install` is the escape hatch; a freshness check
  belongs with RES-1431, which is where orqi's release channels are decided.
- Pinning the installer script or verifying its checksum — see "Installing" above.
- Vendoring or embedding orqi in the CLI binary.
- Windows, which is RES-1476.
