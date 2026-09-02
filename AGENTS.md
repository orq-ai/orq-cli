# Contributing conventions

Guidance for humans and coding agents working in this repo.

## Commands

```sh
make build                          # ./bin/orq
make test                           # go test ./cli/custom/...
go test ./cli/custom/commands -run TestVersionCommandReportsBothVersions   # one test
go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go')            # what CI runs
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

**Who owns what in `~/.orq/credentials.json`.** `profiles.<name>` is bartolo's:
a profile that exists but holds no `api_key` fails every request rather than
falling back to `ORQ_API_KEY`, so this CLI writes one only for a real API key.
Everything else it tracks per profile — the minted gateway key, its id and
expiry, the workspace it was minted for, and a session-bound server — lives
under the `state` key of the same file, read and written through
`cli/custom/auth/state.go`. `auth.MigrateProfileState` moves any older layout
across on the next command, so never add a field to a profile. One exception:
an API-key profile keeps its own `server`, because bartolo resolves that one
itself — state only carries the server of a profile that has no key.
`state.go` also depends on two field names bartolo owns and does not export,
`profile-selected` and `profile-decided` in `config.json`; a rename upstream
breaks it silently, so check them when bumping the generator.

**Distribution:** five cross-compiled binaries per release, wrapped as
`npm/cli-<os>-<arch>` packages behind the `@orq-ai/cli` shim, plus raw binaries,
checksums and a stamped `install.sh` on the GitHub release. `install.sh` is
POSIX sh, has no `pipefail`, and is exercised under `dash` in CI.

## Pull request titles

**Every PR title must be a conventional commit.** The `pr title is a conventional
commit` job in `.github/workflows/pr-title.yml` fails CI otherwise. It runs when a
PR is opened, edited, reopened, or synchronized. `edited` fires on a retitle, so a
corrected title is revalidated without a push.

```
<type>[optional scope][!]: <description>

feat(auth): add device-login retry
fix: stop doctor panicking on a missing profile
chore(release): cut cli 4.15.0-rc.4
```

Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`,
`ci`, `chore`, `revert`.

The title is not cosmetic. `.github/scripts/label-pr.js`, run by
`.github/workflows/label-pr.yml`, maps its type to a dedicated `release:*`
automation label, and `gh release create --generate-notes` groups the release
body by that label:

| Type                                      | Label                    | Release-notes section |
| ----------------------------------------- | ------------------------ | --------------------- |
| `feat`                                    | `release:features`       | 🚀 Features           |
| `fix`, `perf`, `revert`                   | `release:bug-fixes`      | 🐛 Bug Fixes          |
| `docs`                                    | `release:documentation`  | 📚 Documentation      |
| `chore`, `build`, `test`, `style`, `ci`, `refactor` | `release:maintenance` | 🧰 Maintenance        |

The `release:*` namespace is reserved for this automation. On PR open, edit, or
reopen, the labeler provisions a missing release label, adds it, and only then
removes the stale ones. That order matters: a run that dies mid-transition leaves
too many release labels rather than none, and too many is what the next edit
repairs. `addLabels` would create a missing label by itself, but with a colour
GitHub picks and no description; provisioning first is what makes both ours.

Retitling a mapped PR replaces its previous automation label. A title the labeler
cannot parse, or whose type is unmapped, removes all automation labels. Ordinary,
human-applied labels are preserved. A PR with no automation label falls through
`.github/release.yml`'s `"*"` catch-all into `Other Changes`.

The labeler's pattern mirrors what the validator accepts: a lowercase type, an
optional greedily-matched scope, a literal `": "`, and a subject with at least one
non-space character. A title that fails the title check gets no automation label
either. Nested-paren scopes (`feat(a(b)): x`) are labelled, because the
validator's own parser captures the scope greedily and accepts them.

One degenerate case still diverges, and nothing in CI flags it: a subject of two
or more spaces and nothing else (`feat:` followed by three spaces) passes the
title check — the validator rejects only an *empty* subject — while the labeler
refuses it, so such a PR would merge unlabelled and land in `Other Changes`. The
labeler's half of that is pinned by `label-pr.test.js`; the divergence itself is
not asserted anywhere, and it is not reachable by a title anyone would write.

Adding a type or renaming a label means keeping `label-pr.js`, `pr-title.yml`,
`.github/release.yml`, and the table above synchronized. You do not have to
remember to: the `workflow and release-label validation` CI job runs
`.github/scripts/check-release-label-config.js`, which fails when they disagree.
It checks an allowed type with no mapping, a mapped label missing from the
release categories, a mapped label whose `labelDetails` entry has no six-digit
hex colour or no description, a missing `"*"` catch-all, the table above, and
the labeler's own wiring (the `edited` trigger, the two write permissions, the
SHA pins, and that the checkout stays `ref:`-less so a fork's code never runs in
a job holding a write token). It also pins both `pull_request_target` triggers
and asserts that `pr-title.yml` never checks anything out, since a switch to
`pull_request` or an added checkout would put fork code inside a privileged job
while passing every other check.

It does not check how `label-pr.js` is written, only what it configures.
Renaming a variable there is not a regression. That module's behaviour is
covered by `node --test .github/scripts/label-pr.test.js`, which also runs in
that CI job: label provisioning, the error paths, pagination, and the
add-before-remove ordering.

Reverts made with GitHub's button are titled `Revert "feat: ..."`, which is not
conventional. Retitle to `revert: ...` before the check will pass.

## Commits

Conventional commits: `type(scope): subject`. Types in use here are `feat`,
`fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `ci` — the title check accepts
`style`, `build` and `revert` too. A breaking change is a `!` after the type/scope
(`feat(auth)!: ...`) or a `BREAKING CHANGE:` footer.

The type is not decoration — it is what decides the version and whether the
changelog moves. Pick it for the user-visible effect, not the size of the diff:
a large refactor with no observable change is `refactor`, and a one-line change
to a flag's meaning is `feat!`.

Commit messages and PR titles follow the same convention, and a PR title is a
merge gate — see [Pull request titles](#pull-request-titles). `CHANGELOG.md` is
written by hand and stays the source of truth for the stability contract; the
generated release notes complement it with per-PR attribution.

## Versioning

Full rules, and how the pipeline resolves a number, are in `CHANGELOG.md` under
[Versioning](CHANGELOG.md#versioning). What you need while writing a PR: the
commit type is what your change earns. The release cuts the larger of that and
whatever the orq API version moved on its own.

| Commit type | Bump it earns |
|---|---|
| `feat!`, any `type!:`, `BREAKING CHANGE:` footer | major |
| `feat` | minor |
| `fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `ci` | patch |

**You do not tag, and you edit `VERSION` only to force a number the rules would
not reach on their own** — how the pipeline resolves a number from the table
above is in [Versioning](CHANGELOG.md#versioning), and stating it twice is how
the two copies drift.

**Never write a breaking commit without asking first.** A `!` on a commit
subject, or a `BREAKING CHANGE:` footer in its body, anywhere in the range cuts
a major release, and it
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
