# orq-cli release pipeline (prod + rc) — plan

## Goal
Complete the CLI publish pipeline. orquesta-web commits the OpenAPI schema into
orq-cli (root for prod, `packages/orq-rc/` for rc) and bumps `app_version` in the
matching `.bartolo.json`. orq-cli then regenerates the CLI from that schema,
commits the generated files back to `main`, tags + publishes a GitHub release,
builds all platform binaries, and publishes to npm.

## Trigger (number-driven — orq-cli creates the tag)
orquesta-web pushes the schema + `app_version` bump to orq-cli `main` (no tag).
The push to `main` triggers the release; the version comes from `.bartolo.json`:
- root `openapi.yaml` / `.bartolo.json` changed  -> stable/prod  (`vX.Y.Z`)
- `packages/orq-rc/*` changed                    -> rc/staging   (`vX.Y.Z-rc.N`)

## Layout
- root module `orq`        = prod CLI (committed `cli/generated`, schema `openapi.yaml`)
- `packages/orq-rc` module `orq-rc` = rc CLI (committed `cli/generated`, schema `openapi.yaml`)
  - shares root custom code via `replace orq => ../..`
  - publishes SAME npm package `@orq-ai/cli` under npm dist-tag `rc`

## bartolo
- public module `github.com/orq-ai/bartolo`, pinned in each module's `go.mod` (v0.2.0)
- CI: `go install github.com/orq-ai/bartolo@<go.mod version>` (no auth — public via Go proxy)
- `bartolo generate <schema>` -> regenerates `cli/generated/*.go`, `examples/README.md`,
  and overwrites `README.md` (workflow restores the curated root `README.md`).
  Does NOT touch `cmd/main.go`, `cli/custom`.

## Status

### orquesta-web
- [x] publish-openapi-schema pushes prod `openapi.yaml` + root `.bartolo.json` bump
- [x] pushes staging schema to `packages/orq-rc/openapi.yaml` + `.bartolo.json` bump

### orq-cli — rc module scaffold
- [x] `packages/orq-rc/` module `orq-rc` (go.mod `replace orq => ../..`)
- [x] `packages/orq-rc/.bartolo.json` (module_path orq-rc, last_spec_path openapi.yaml)
- [x] `packages/orq-rc/cmd/orq/main.go` imports `orq/cli/custom` + `orq-rc/cli/generated`
- [x] initial generated rc code committed; `go build ./cmd/orq` verified

### orq-cli — scripts
- [x] `scripts/release-build.sh <ver> [module-dir]` (rc builds from packages/orq-rc)

### orq-cli — workflows
- [x] `.github/workflows/release.yml` — reusable `workflow_call` (regen -> commit-back
      -> tag + release -> build -> npm publish w/ dist-tag)
- [x] `.github/workflows/release-stable.yml`      (push:main, root paths, dist-tag latest)
- [x] `.github/workflows/release-pre-release.yml`  (push:main, packages/orq-rc paths, dist-tag rc)

### secrets (manual, user)
- [ ] `NPM_TOKEN` — already exists
- write-back / tag / release use the built-in `GITHUB_TOKEN` (contents: write)
- bartolo is public — no read token needed

## Loop safety
Commit-back stages only `cli/generated` / `examples` / `go.mod` / `go.sum` — none match the
trigger paths (`openapi.yaml` / `.bartolo.json`), and `GITHUB_TOKEN` pushes don't fire workflows.
Tags don't trigger (workflows fire on `branches: [main]`).

## Open notes
- Landing this change touches `openapi.yaml` + `.bartolo.json`, which matches the stable trigger.
  Land carefully (set secrets deliberately) so the first commit doesn't fire an unintended release.
- bartolo generator/library version parity: CI installs the go.mod-pinned version so generated
  code matches the runtime library.
