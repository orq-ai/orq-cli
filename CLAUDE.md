# Working in this repo

The `orq` CLI. `cli/generated/` is generated from `openapi.yaml` by `bartolo` and
is not edited by hand; `cli/custom/` is where hand-written commands live.
`packages/orq-rc` is a second Go module that compiles the same `cli/custom`
against the staging schema, so a change under `cli/custom/` ships on both lines.

Before pushing: `make test`, `go vet ./...`, and `go run ./cmd/surface-dump -check`
(committed as `surface.json`; `-write` regenerates it).

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
