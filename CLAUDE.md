# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
make build                          # ./bin/orq
make test                           # go test ./cli/custom/...
go test ./cli/custom/commands -run TestVersionCommandReportsBothVersions   # one test
go test ./... && go vet ./... && gofmt -l .                                # what CI runs
go run ./cmd/surface-dump -check    # command-surface gate; -write to accept a change
go run ./cmd/orq --json doctor      # or: make doctor
```

CI additionally runs, and these are worth reproducing locally when you touch what
they cover: `dash -n install.sh` (the installer must stay POSIX — macOS `/bin/sh`
is bash in POSIX mode and accepts things dash rejects), and
`python3 scripts/stamp-changelog.py --self-test`.

The second module has its own checks: `cd packages/orq-rc && go build ./... && go vet ./...`.

## Architecture

**Two Go modules, one set of hand-written commands.** The root module builds the
stable CLI from `openapi.yaml` (production schema); `packages/orq-rc` is a
separate module that builds the rc CLI from the staging schema. It reaches the
root's `cli/custom` through `replace orq => ../..` in its `go.mod`, so **anything
you change under `cli/custom/` ships on both lines** and must compile against
both schemas. Only the generated tree and `cmd/orq` differ between them.

**Generated vs. hand-written.** `cli/generated/` is produced by `bartolo` from
`openapi.yaml` and is never edited by hand — `bartolo generate` wipes it, and it
also **rewrites `.bartolo.json` from its own struct**, so a field added to that
file is silently dropped on the next regen (this is why the CLI's own version
lives in `VERSION`). `cli/custom/` is ours and bartolo leaves it alone.

**How a binary is assembled.** `cmd/orq/main.go` (and its rc twin) pass a version,
an API version and a `registerGenerated` callback into `custom.Run`
(`cli/custom/run.go`), which owns what the two modules must not drift on: the
bartolo `Init` config, signal handling, and the exit-code contract (0 ok, 1
command error, 130 SIGINT, 143 SIGTERM). `custom.Register` then wires middleware
and the hand-written commands on top of the generated tree.

**Guards that live in `register.go`,** and are the reason a new command sometimes
fails in a non-obvious way:

- `profileExemptCommands` — commands that must work before a session exists
  (`login`, `setup`, `doctor`, `update`, `version`, …). A new command that never
  calls the orq API belongs here.
- `interactiveWizardCommands` — bartolo-owned prompts that ignore `--no-input`,
  refused up front so `--no-input` never prompts.
- `commandGroup` in `groups.go` — every visible command needs an entry, or
  `groups_test.go` fails.

**The surface gate.** ~95% of the command tree comes from the schema, so a
renamed API field reaches users' flags with nothing forcing a human to look.
`cmd/surface-dump` walks the registered tree and writes `surface.json`; CI diffs
it, so a surface change has to be committed deliberately and shows up in the PR.
It ships to nobody and exists only for that diff.

**Subpackages worth knowing before adding to them:** `cli/custom/auth` (OAuth
device login, the `~/.orq/sessions` profile store, self-hosted URL resolution),
`cli/custom/launch` (runs coding agents — one file per agent — with orq wired in
as their gateway), `cli/custom/skills` (agent skills embedded with `go:embed`,
installed into each agent's config dir).

**Distribution:** five cross-compiled binaries per release, wrapped as
`npm/cli-<os>-<arch>` packages behind the `@orq-ai/cli` shim, plus raw binaries,
checksums and a stamped `install.sh` on the GitHub release. `install.sh` is
POSIX sh, has no `pipefail`, and is exercised under `dash` in CI.

## Commits

Conventional commits: `type(scope): subject`. Types in use here are `feat`,
`fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `ci`. A breaking change is a
`!` after the type/scope (`feat(auth)!: ...`) or a `BREAKING CHANGE:` footer.

The type is not decoration — it is what decides the version and whether the
changelog moves. Pick it for the user-visible effect, not the size of the diff:
a large refactor with no observable change is `refactor`, and a one-line change
to a flag's meaning is `feat!`.

## Versioning

Full rules, and how the pipeline resolves a number, are in `CHANGELOG.md` under
[Versioning](CHANGELOG.md#versioning). What you need while writing a PR: the
commit type is the version.

| Commit type | CLI version |
|---|---|
| `feat!`, any `type!:`, `BREAKING CHANGE:` footer | major |
| `feat` | minor |
| `fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `ci` | patch |

The release pipeline reads the commits since the last release, takes the largest
bump they earn, and compares it with the field the orq API version moved —
whichever is larger wins. **You do not edit `VERSION` and you do not tag.** That
file records what was last released; the pipeline writes it.

**Never write a breaking commit without asking first.** A single `!` or
`BREAKING CHANGE:` footer anywhere in the range cuts a major release, and it
cannot be walked back once published. If a change is genuinely breaking, say so
in the PR and get a decision; otherwise land it as `feat:`/`fix:` and describe
the break in the PR body and the changelog entry.

## Changelog

**Every PR updates `CHANGELOG.md`.** Add entries under `## Unreleased`, in the
same PR as the change, in the same voice as the entries already there: what
changed, what a script or a user sees differently, and why if it is not obvious.
The release pipeline renames that section to the version being cut and publishes
it as the release notes, so this is the only description of a release anyone
writes.

- `feat` → `**Added:**` (or `**Changed:**` when it alters existing behaviour)
- `fix` → `**Fixed:**`, and say what the wrong behaviour was — that is what
  tells a reader whether they were hit by it
- breaking → `**Removed:**` or `**Changed:**`, and name the migration
- `refactor`, `test`, `chore`, `ci`, and `docs` that do not change user-facing
  documentation → **no entry**. The first paragraph of `CHANGELOG.md` is the
  test: internal refactors do not belong there.

If a PR's only changes are of the no-entry kind, say so in the PR description
rather than inventing an entry. An empty `## Unreleased` between releases is a
correct state; a changelog padded with refactors is not.

## What scripts may depend on

`CHANGELOG.md`'s [Stability contract](CHANGELOG.md#stability-contract) is
binding, not aspirational. The parts that most often catch a change out:
`--json` on stdout is the machine contract and mirrors the API response shape;
TOON (the default terminal format) is presentation-only and may change; errors
go to stderr and results to stdout; and removing or renaming a command or flag
is a breaking change that must be announced at least one release ahead.
