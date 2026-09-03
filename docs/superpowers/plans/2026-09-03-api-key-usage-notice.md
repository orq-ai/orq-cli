# API Key Usage Notice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tell interactive users when an exported API key authenticates an API command instead of their active profile, with an environment-variable opt-out.

**Architecture:** Extend the shared authentication pre-run hook, which already snapshots credentials before session-token injection. Isolate the display predicate in a helper so precedence, TTY and machine-output gating, command exemptions, and the opt-out can be tested without network requests.

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
- Test: `cli/custom/server_test.go`

**Interfaces:**
- Consumes: `apiKeyEnvVars`, `ownExportedKey()`, `profileExemptCommands`, `auth.ActiveProfile()`, `auth.Session`, `bartolocli.Stderr`, and Cobra/Viper output-format state.
- Produces: `configuredAPIKeyEnvVar() string` and `showAPIKeyUsageNotice(cmd *cobra.Command, envVar string, explicitKey bool, session *auth.Session)`.

- [ ] **Step 1: Write failing table tests for the display predicate**

Add a harness to `cli/custom/server_test.go` that swaps `bartolocli.Stderr`, forces `stdoutIsTerminal`, installs a temporary credentials store, restores Viper/profile globals, and builds a root plus an API-backed child. Cover these cases:

```go
cases := []struct {
	name        string
	commandPath string
	envVar      string
	explicitKey bool
	session     *auth.Session
	profileKey  string
	noNotice    string
	tty         bool
	json        bool
	wantNotice  bool
}{
	{name: "session displaced on tty", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, tty: true, wantNotice: true},
	{name: "stored profile displaced on tty", envVar: "ORQ_TOKEN", explicitKey: true, profileKey: "profile-key", tty: true, wantNotice: true},
	{name: "opt out", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, noNotice: "1", tty: true},
	{name: "non tty", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}},
	{name: "machine format", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, tty: true, json: true},
	{name: "no displaced profile", envVar: "ORQ_API_KEY", explicitKey: true, tty: true},
	{name: "session bridge owns exported key", envVar: "ORQ_API_KEY", session: &auth.Session{}, tty: true},
	{name: "local command", commandPath: "version", envVar: "ORQ_API_KEY", explicitKey: true, session: &auth.Session{}, tty: true},
}
```

For positive cases, assert stderr contains the environment-variable name and active profile, excludes the test key, and stdout remains empty. Add a separate test proving `configuredAPIKeyEnvVar()` follows `ORQ_API_KEY`, `ORQ_TOKEN`, `ORQ_AUTHORIZATION` precedence.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```sh
go test ./cli/custom -run 'Test(ShowAPIKeyUsageNotice|ConfiguredAPIKeyEnvVar)$'
```

Expected: compilation fails because both helpers are undefined.

- [ ] **Step 3: Implement the minimal helpers**

In `cli/custom/register.go`, add:

```go
const noAPIKeyNoticeEnvVar = "ORQ_NO_API_KEY_NOTICE"

func configuredAPIKeyEnvVar() string {
	for _, envVar := range apiKeyEnvVars {
		if strings.TrimSpace(os.Getenv(envVar)) != "" {
			return envVar
		}
	}
	return ""
}
```

Implement `showAPIKeyUsageNotice` to require a non-empty `envVar`, `explicitKey`, terminal stdout, no opt-out, a non-exempt command, no explicit machine format, and either a session or stored profile key. Suppress it when an explicit `--profile` has a key and therefore wins through `applyProfileAPIKey`. Otherwise write exactly one safe line:

```go
fmt.Fprintf(bartolocli.Stderr, "Using %s instead of profile %q.\n", envVar, auth.ActiveProfile())
```

- [ ] **Step 4: Wire the helper into pre-run**

Before session injection, capture:

```go
envKeyVar := configuredAPIKeyEnvVar()
explicitKey := apiKeyConfigured() && !ownExportedKey()
```

After `applyProfileAPIKey` and `auth.ReadSession()`, call:

```go
showAPIKeyUsageNotice(cmd, envKeyVar, explicitKey, session)
```

Call it before the no-session early return. Do not move session-token injection or project-resolution logic.

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run:

```sh
gofmt -w cli/custom/register.go cli/custom/server_test.go
go test ./cli/custom -run 'Test(ShowAPIKeyUsageNotice|ConfiguredAPIKeyEnvVar)$'
go test ./cli/custom/...
```

Expected: all commands exit 0 and the focused tests report `ok orq/cli/custom`.

- [ ] **Step 6: Commit the tested behavior**

```sh
git add cli/custom/register.go cli/custom/server_test.go
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
  of the active user profile.** The notice names the winning environment variable
  but never the credential, is written to stderr, and stays out of piped or
  explicitly machine-formatted output. Set `ORQ_NO_API_KEY_NOTICE` to any
  non-empty value to hide it.
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
