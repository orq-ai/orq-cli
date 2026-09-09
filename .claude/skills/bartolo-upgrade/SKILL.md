---
name: bartolo-upgrade
description: Use when upgrading, bumping, or pinning the bartolo generator (github.com/orq-ai/bartolo) in this repo — including "update the CLI to the latest bartolo", a dependabot bump of that module, or checking whether a regenerated cli/generated tree is complete.
---

# Upgrading the bartolo generator

The pin bump is the easy half. The work is deciding what the new generator
changed for users, because no gate in CI can tell you: they cover compilation
and the command surface, and neither reads an output format.

Ownership rules (`cli/generated/` vs `cli/custom/`, `.bartolo.json` vs `go.mod`
vs `VERSION`) are in [AGENTS.md](../../../AGENTS.md#architecture) and are not
repeated here. This file is the upgrade sequence and the review it requires.

## Steps

1. **Find the latest release and read its notes.** The module proxy lags; ask
   GitHub. `gh api repos/orq-ai/bartolo/releases --jq '.[].tag_name'` lists
   newest first, and the `.body` of every tag you cross is the raw material for
   steps 5, 6 and 7.
2. **Bump both modules to the same version.** CI diffs them; they must match.
   ```sh
   go get github.com/orq-ai/bartolo@vX.Y.Z && go mod tidy
   (cd packages/orq-rc && go get github.com/orq-ai/bartolo@vX.Y.Z && go mod tidy)
   ```
3. **Regenerate both trees with that exact version,** each module against its
   own schema:
   ```sh
   go run github.com/orq-ai/bartolo@vX.Y.Z generate "$(jq -r .last_spec_path .bartolo.json)"
   (cd packages/orq-rc && go run github.com/orq-ai/bartolo@vX.Y.Z generate "$(jq -r .last_spec_path .bartolo.json)")
   ```
   Never `bartolo sync` or `make sync` here, however much its `upgrade` alias
   sounds like this task: it rewrites `cmd/orq/main.go` from bartolo's template,
   taking the `custom.Run` wiring with it. `cli/custom/scaffold_canary_test.go`
   is what catches that.
4. **Run the gates:** everything under Commands in
   [AGENTS.md](../../../AGENTS.md#commands) — that list is what CI runs, so read
   it there rather than trusting a copy — plus `go run ./cmd/surface-dump -check`
   and the rc module's own `go build ./... && go vet ./...`.
5. **Ask what the release changed beyond the command tree.** Notes describing
   credential, config-file or on-disk state changes mean `cli/custom/` owes a
   migration, and a green build does not answer it: v0.9.0 moved where
   credential state lives and needed `cli/custom/auth/state.go` written for it,
   v0.12.0 dropped commands whose exemptions in `register.go` then had to go.
   Behaviour that used to be ours can become bartolo's without a compile error.
6. **Read the generated diff for user-visible behaviour.** REQUIRED: for each
   distinct changed line in
   `git diff cli/generated packages/orq-rc/cli/generated`, name the command it
   belongs to and what a user now sees. The enclosing `Use:` string is the
   command — for a hunk at line `L` of the file the hunk is in:
   ```sh
   awk -v L=<L> 'NR<=L && /Use: */ {u=$0} NR==L {print u}' <path>_commands.go
   ```
   A change in `openapi_client.go` has no enclosing `Use:`; identify it by the
   function instead and assume it reaches every command that calls it. Read the
   new call's source under
   `$(go env GOMODCACHE)/github.com/orq-ai/bartolo@vX.Y.Z` and, when reading
   does not settle it, exercise it in a scratch module rather than guessing.
7. **Write the `## Unreleased` entry in `CHANGELOG.md`,** naming the commands
   from step 6, both lines when the rc tree differs, and what is unchanged for
   scripts. If step 6 genuinely found nothing a user or script can observe,
   there is no entry and the commit is a `chore` —
   [AGENTS.md](../../../AGENTS.md#changelog) decides that, not the size of the
   diff. What is not allowed is skipping the entry because you skipped step 6.
8. **Commit as a conventional commit,** with a body naming the upstream releases
   crossed. `!` only when a documented contract in
   [CHANGELOG.md](../../../CHANGELOG.md#stability-contract) breaks under a
   caller; ask before writing one. A command or flag leaving `surface.json`
   without a deprecation notice in an earlier release is that case — keep the old
   spelling alive from `cli/custom/`, announce it, and drop it a release later.

## Reviewing someone else's bump (dependabot, a teammate's PR)

Same steps 4-8. To prove the committed tree really is that generator's output
rather than a stale or hand-edited one, copy the worktree elsewhere, regenerate
there with the pinned version, and diff — a genuine regen is byte-identical.

## Red flags

- "Tests and the surface gate pass, so nothing user-visible changed" — the
  surface gate does not read output format. Do step 6.
- "No changelog entry needed" before step 6 has been done. That conclusion is
  step 6's output, not an input.
- "It's just a pin bump" — every bump regenerates code, and two of the three
  bumps this repo has done also changed `cli/custom/`.
- "The release pipeline regenerates anyway" — it does, at release time, from
  whatever is committed. Shipping a stale tree hides the diff from review.
- Editing `cli/generated/` by hand, or committing a regen from a different
  bartolo version than `go.mod` pins.
