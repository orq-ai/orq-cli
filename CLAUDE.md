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
[Versioning](CHANGELOG.md#versioning). What you need while writing a PR:

| Commit type | CLI version | You edit `VERSION` |
|---|---|---|
| `feat!`, `BREAKING CHANGE:` | major | yes — `X+1.0.0` |
| `feat` | minor | yes — `X.Y+1.0` |
| `fix`, `perf`, `refactor`, `docs`, `test`, `chore`, `ci` | patch | no |

The pipeline takes the next free tag from whatever `VERSION` says, so the number
you write is a floor, not a promise. It also applies the orq API's own bump on
top when a new schema lands in the same release — that part is automatic and is
never a reason to edit `VERSION`.

Do not edit `VERSION` for a release, only for a change of our own that earns a
major or a minor. Never edit it to "the version I am releasing".

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
