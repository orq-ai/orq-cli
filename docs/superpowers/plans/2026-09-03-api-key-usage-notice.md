# API Key Usage Notice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tell interactive users when an exported API key authenticates an API command instead of their active profile, with an environment-variable opt-out.

**Architecture:** Use the shared authentication pre-run hook to snapshot the candidate environment credential before session-token injection. Emit from Bartolo's `before dial` middleware only when the actual outgoing Authorization header contains that credential, so session-only and local commands cannot produce false notices.

**Tech Stack:** Go, Cobra, Viper, Bartolo CLI writers, standard `testing` package.

## Global Constraints

- The notice goes to stderr and never includes any part of an API key.
- `--json` and explicit machine formats remain free of the notice.
- Any non-empty `ORQ_NO_API_KEY_NOTICE` suppresses the notice.
- `cli/custom/` must compile against both stable and rc schemas.
- Never edit generated command trees.

---

### Task 1: Detect and display environment API-key usage

**Files:**
- Modify: `cli/custom/register.go`
- Modify: `cli/custom/commands/identity.go`
- Modify: `cli/custom/commands/ui.go`
- Test: `cli/custom/server_test.go`

**Interfaces:**
- Consumes: `ownExportedKey()`, `auth.ActiveProfile()`, `auth.Session`, `bartolocli.Stderr`, and the outgoing Bartolo request.
- Produces: `configureAPIKeyUsageNotice(cmd *cobra.Command, explicitKey bool, session *auth.Session)` and `apiKeyUsageNoticeBeforeDial`, backed by the shared `commands.UserAPIKeyCredential()` and `commands.MachineFormatRequested()` decisions.

- [ ] **Step 1: Write failing table tests for the display predicate**

Add a harness to `cli/custom/server_test.go` that swaps `bartolocli.Stderr`, forces `stdoutIsTerminal`, installs a temporary credentials store, restores Viper/profile globals, and builds a root plus an API-backed child. Cover these cases:

```go
cases := []struct {
	name        string
	envVar      string
	explicitKey bool
	session     *auth.Session
	profileKey  string
	noNotice    string
	tty         bool
	json        bool
	wantPending bool
}{
	{name: "ORQ_API_KEY displaces session on tty", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, tty: true, wantPending: true},
	{name: "ORQ_TOKEN displaces session on tty", envVar: "ORQ_TOKEN", explicitKey: true, session: &auth.Session{}, tty: true, wantPending: true},
	{name: "ORQ_AUTHORIZATION displaces session on tty", envVar: "ORQ_AUTHORIZATION", explicitKey: true, session: &auth.Session{}, tty: true, wantPending: true},
	{name: "stored profile wins", envVar: "ORQ_TOKEN", explicitKey: true, profileKey: "profile-key", tty: true},
	{name: "opt out", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, noNotice: "1", tty: true},
	{name: "non tty", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}},
	{name: "machine format", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, tty: true, json: true},
	{name: "no displaced profile", envVar: "ORQ_API_KEY", explicitKey: true, tty: true},
	{name: "session bridge owns exported key", envVar: "ORQ_API_KEY", session: &auth.Session{}, tty: true},
}
```

For positive cases, send a request through the middleware, assert its Authorization header uses the exported key, and assert stderr is exactly `Using ORQ_API_KEY from environment` while excluding the key. Cover all three supported variables and a mismatched session credential that must remain silent.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
go test ./cli/custom -run 'Test(ConfigureAPIKeyUsageNotice|APIKeyUsageNotice)'
```

Expected: compilation fails because the request-bound notice helpers are undefined.

- [ ] **Step 3: Implement the request-bound notice**

Export the existing machine-format predicate and environment credential-source resolver from `cli/custom/commands`. In `cli/custom/register.go`, let `configureAPIKeyUsageNotice` require `explicitKey`, a login session, terminal human output, no opt-out, and no stored profile key. Register a `before dial` middleware that compares the actual Authorization header with the candidate exported key and emits at most once:

```go
fmt.Fprintln(bartolocli.Stderr, "Using ORQ_API_KEY from environment")
```

- [ ] **Step 4: Wire the helper into pre-run**

After `applyProfileAPIKey` and `auth.ReadSession()`, configure the candidate:

```go
configureAPIKeyUsageNotice(cmd, explicitKey, session)
```

Install the middleware once during `Register`. Do not move session-token injection or project-resolution logic.

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run:

```sh
gofmt -w cli/custom/register.go cli/custom/server_test.go
go test ./cli/custom -run 'Test(ConfigureAPIKeyUsageNotice|APIKeyUsageNotice)'
go test ./cli/custom/...
```

Expected: all commands exit 0 and the focused tests report `ok orq/cli/custom`.

- [ ] **Step 6: Commit the tested behavior**

```sh
git add cli/custom/register.go cli/custom/commands/identity.go cli/custom/commands/ui.go cli/custom/server_test.go
git commit -m "feat(auth): show environment API key usage"
```

### Task 2: Document and verify the behavior

**Files:**
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: Task 1's notice behavior.
- Produces: an `Unreleased` stability note for the TTY notice and opt-out.

- [ ] **Step 1: Add the changelog entry**

Under `## Unreleased`, add:

```markdown
- **Added: interactive commands now say when an exported API key is used instead
  of the active user profile.** The fixed `Using ORQ_API_KEY from environment`
  banner never includes the credential, is written to stderr, and stays out of
  piped or explicitly machine-formatted output. Set `ORQ_NO_API_KEY_NOTICE` to
  any non-empty value to hide it.
```

- [ ] **Step 2: Run repository verification**

Run:

```sh
go test ./... && go vet ./... && test -z "$(gofmt -l $(git ls-files '*.go'))"
(cd packages/orq-rc && go build ./... && go vet ./...)
go run ./cmd/surface-dump -check
git diff --check origin/main...
```

Expected: every command exits 0; the surface gate reports no difference.

- [ ] **Step 3: Commit the changelog**

```sh
git add CHANGELOG.md
git commit -m "docs: document API key usage notice"
```
