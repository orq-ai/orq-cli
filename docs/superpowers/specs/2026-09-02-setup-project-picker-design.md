# Setup Project Picker

## Goal

Make project selection part of the default interactive `orq setup` experience.
When setup runs in a terminal and the chosen workspace contains multiple
projects, it should ask the user which project to activate immediately after
workspace selection.

## Behavior

- A TTY run with multiple selectable projects shows the existing project
  picker.
- A TTY run with one selectable project activates it without prompting.
- A non-TTY run, or a run with `--no-input`, does not prompt and activates the
  workspace's default project when one exists.
- In session-backed setup, `--project <id|key|name>` resolves and activates that
  project without prompting.
- `--no-project` skips selection and clears any project already active on the
  session.
- Archived projects remain excluded from every selection path.
- A workspace with no selectable projects continues without making a new
  project selection.
- API-key-only setup continues without project selection because it has no
  login session on which to persist the choice. Project flags cannot scope a
  supplied key and are not acted upon in that mode; setup keeps its existing
  explanatory note.

The broader `--interactive` (`-i`) behavior remains unchanged: it can force
workspace selection and API-key confirmation. Project prompting no longer
depends on it. Update the flag and command help to describe those remaining
effects instead of claiming that `-i` controls every setup choice.

## Implementation

Keep project selection in the existing setup project step, which already runs
after authentication and workspace resolution and before API-key creation.
Change only the prompt decision. Preserve the existing branch order for
`--no-project`, a missing session, list failure, an empty selectable list,
explicit `--project`, and a single selectable project. When those branches do
not decide the result, call the picker if `!opts.noInput`. Do not include
`opts.interactive` in this condition. Otherwise retain the current
default-project fallback.

The entry point already turns on `noInput` when stdin or stdout is not a
terminal, and when `--no-input` is set. It also clears `interactive` in that
case. Using only `noInput` keeps terminal detection and flag precedence
centralized and avoids a second, potentially inconsistent TTY check inside the
project step.

Add a project-picker function to `setupOptions`, analogous to its existing
`confirmFn` test seam. A small method uses the injected function when present
and otherwise delegates to the existing `pickProject`. This makes the normal
TTY branch deterministic in unit tests without package-global mutation or a
pseudo-terminal.

The chosen project continues through the existing data flow:

1. Persist its ID and name on the login session.
2. Copy its ID into the setup authentication state.
3. Scope the gateway API key created by the following step to that project.
4. Include the existing `project` object in structured setup output.

No command, flag, or structured-output field changes.

## Errors and fallback

When selectable projects were listed, an explicit unknown or ambiguous
`--project` remains an error. Cancelling the TTY picker remains an error and
stops setup before an API key is created. Failure to list projects remains
non-fatal: setup warns and makes no new project selection. An empty selectable
list likewise makes no new selection. If no default exists during a
non-interactive run, setup makes no new selection. These fallback paths retain
the existing session state and reporting behavior; cleaning up stale project
state is outside this change.

## Testing

Extend the project-step tests to cover the prompt decision independently from
the broader `-i` behavior. The tests should prove that:

- `interactive=false, noInput=false` with multiple projects takes the injected
  picker path;
- a non-interactive setup with multiple projects takes the default project;
- `noInput=true`, including when `interactive=true` is also present in a direct
  unit-test fixture, cannot reach the picker;
- explicit `--project`, `--no-project`, zero-project, and single-project paths
  retain their current behavior;
- the selected project is persisted and passed forward for API-key scoping.

Update `CHANGELOG.md` under `## Unreleased` because this changes visible setup
behavior. Describe it as a fix: the project picker is now part of default TTY
setup rather than requiring `-i`. The command surface does not change, so
`surface.json` should remain unchanged.
