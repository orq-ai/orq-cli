# Default Setup Project Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the existing project picker during ordinary TTY-backed `orq setup` runs without requiring `-i`, while preserving explicit and non-interactive paths.

**Architecture:** Keep the project step between workspace resolution and API-key creation. Use `setupOptions.noInput` as the single resolved prompt gate, with an option-scoped picker function so tests can exercise the TTY branch without a pseudo-terminal or mutable package global.

**Tech Stack:** Go, Cobra, Survey, `net/http/httptest`, standard `testing` package.

## Global Constraints

- Never edit `cli/generated/`. Code under `cli/custom/` must compile in the root and `packages/orq-rc` modules.
- Multiple projects prompt in a TTY; one project is auto-selected.
- Non-TTY and `--no-input` runs never prompt and retain the default-project fallback.
- Session-backed `--project` and `--no-project` keep overriding selection. Archived projects stay excluded.
- API-key-only, list-error, empty-list, and missing-default fallback behavior stays unchanged.
- Do not change the command/flag surface or structured output. `surface.json` must stay unchanged.
- Add a `CHANGELOG.md` entry under `## Unreleased`.

---

## File Structure

- `cli/custom/commands/setup.go`: picker seam and corrected setup help.
- `cli/custom/commands/setup_project.go`: TTY/default selection branch.
- `cli/custom/commands/setup_project_test.go`: prompt-gate regression tests.
- `cli/custom/commands/setup_test.go`: help-text regression test.
- `CHANGELOG.md`: user-visible fix entry.

### Task 1: Make the project picker the normal TTY path

**Files:**
- Modify: `cli/custom/commands/setup.go:39-76`
- Modify: `cli/custom/commands/setup_project.go:17-86`
- Test: `cli/custom/commands/setup_project_test.go:34-108`

**Interfaces:**
- Consumes: `setupOptions.noInput bool` and `pickProject([]auth.Project) (*auth.Project, error)`.
- Produces: `setupOptions.pickProjectFn func([]auth.Project) (*auth.Project, error)` and `(*setupOptions).pickProject([]auth.Project) (*auth.Project, error)`.

- [ ] **Step 1: Write the failing TTY-path test**

Add after `TestProjectStepRecordsTheChoice`:

```go
// A normal TTY setup is already interactive; choosing a project must not
// require the broader -i flag.
func TestProjectStepUsesPickerInDefaultTTYMode(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A","is_default":true},{"project_id":"id-2","key":"b","name":"B"}`)
	state := projectStepState(t, srv.URL)
	pickerCalled := false
	opts := &setupOptions{
		pickProjectFn: func(projects []auth.Project) (*auth.Project, error) {
			pickerCalled = true
			if len(projects) != 2 {
				t.Fatalf("picker received %d projects, want 2", len(projects))
			}
			return &projects[1], nil
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err != nil {
		t.Fatalf("resolveProjectStep: %v", err)
	}
	if !pickerCalled {
		t.Fatal("default TTY setup did not call the project picker")
	}
	if got == nil || got.ProjectID != "id-2" {
		t.Fatalf("chose %+v, want picker result id-2", got)
	}
	if state.session.ActiveProjectID != "id-2" || state.projectID != "id-2" {
		t.Errorf("project was not persisted and forwarded: session=%q state=%q", state.session.ActiveProjectID, state.projectID)
	}
}
```

- [ ] **Step 2: Verify the test fails for the missing seam**

```sh
go test ./cli/custom/commands -run TestProjectStepUsesPickerInDefaultTTYMode
```

Expected: compilation fails because `setupOptions` has no `pickProjectFn`.

- [ ] **Step 3: Implement the option-scoped picker seam**

Add beside `project` and `noProject` in `setupOptions`:

```go
	// pickProjectFn overrides the picker in tests. Keeping it on the options
	// avoids a mutable package global and models a TTY deterministically.
	pickProjectFn func([]auth.Project) (*auth.Project, error)
```

Add beside the existing `setupOptions.confirm` and `confirmPersistent` methods
in `setup.go`:

```go
func (o *setupOptions) pickProject(projects []auth.Project) (*auth.Project, error) {
	if o.pickProjectFn != nil {
		return o.pickProjectFn(projects)
	}
	return pickProject(projects)
}
```

- [ ] **Step 4: Implement the minimal prompt-gate change**

Replace `case opts.interactive` in `resolveProjectStep` with:

```go
	case !opts.noInput:
		chosen, err = opts.pickProject(projects)
		if err != nil {
			return nil, err
		}
```

Do not change the branches before it or the default-project branch after it.

- [ ] **Step 5: Pin `noInput` precedence**

In the multi-project half of `TestProjectStepAutoSelects`, replace `&setupOptions{}` with:

```go
	nonInteractive := &setupOptions{
		noInput:     true,
		interactive: true,
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("--no-input reached the project picker")
			return nil, nil
		},
	}
```

Pass `nonInteractive` to `resolveProjectStep` and retain the existing `id-2` assertion. The contradictory direct fixture proves `noInput` alone wins; production `applyGlobalFlags` also clears `interactive`.

- [ ] **Step 6: Pin explicit-project precedence and picker cancellation**

Add a picker that fails the test to the existing explicit-project fixture in
`TestProjectStepRecordsTheChoice`:

```go
	opts := &setupOptions{
		project: "banking",
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			t.Fatal("an explicit --project reached the picker")
			return nil, nil
		},
	}
```

Add this cancellation test after the TTY-path test:

```go
func TestProjectStepReturnsPickerError(t *testing.T) {
	srv := projectsServer(t, `{"project_id":"id-1","key":"a","name":"A"},{"project_id":"id-2","key":"b","name":"B"}`)
	state := projectStepState(t, srv.URL)
	opts := &setupOptions{
		pickProjectFn: func([]auth.Project) (*auth.Project, error) {
			return nil, fmt.Errorf("picker cancelled")
		},
	}

	got, err := resolveProjectStep(newReporter(true), auth.NewClient(srv.URL), state, opts)
	if err == nil || err.Error() != "picker cancelled" {
		t.Fatalf("resolveProjectStep = (%+v, %v), want nil project and picker cancellation", got, err)
	}
	if state.projectID != "" {
		t.Errorf("state.projectID = %q after cancellation, want empty", state.projectID)
	}
}
```

- [ ] **Step 7: Format and run focused tests**

```sh
gofmt -w cli/custom/commands/setup.go cli/custom/commands/setup_project.go cli/custom/commands/setup_project_test.go
go test ./cli/custom/commands -run 'TestProjectStep|TestSetupNoProject'
```

Expected: all selected tests pass.

- [ ] **Step 8: Verify the project-to-key scoping boundary**

The project step already proves that the chosen ID reaches `state.projectID`,
and the auth package already pins how that ID enters the key request. Run both
tests together rather than duplicating the key-body implementation in a setup
test:

```sh
go test ./cli/custom/commands ./cli/custom/auth -run 'TestProjectStepUsesPickerInDefaultTTYMode|TestCreateAPIKeyScopesThroughProjects'
```

Expected: both packages pass.

- [ ] **Step 9: Commit**

```sh
git add cli/custom/commands/setup.go cli/custom/commands/setup_project.go cli/custom/commands/setup_project_test.go
git commit -m "fix(setup): show the project picker in tty setup"
```

### Task 2: Correct setup help and release notes

**Files:**
- Modify: `cli/custom/commands/setup.go:194-220`
- Test: `cli/custom/commands/setup_test.go:928-955`
- Modify: `CHANGELOG.md:112`

**Interfaces:**
- Consumes: `NewSetupCommand() *cobra.Command` and Cobra `Flag.Usage`/`Command.Long` fields.
- Produces: help that describes `-i` as revisiting inferred workspace and API-key choices, without implying that it overrides explicit flags or always prompts.

- [ ] **Step 1: Write the failing help test**

Add after `TestSetupHasNoAgentFlags`:

```go
func TestSetupInteractiveFlagDescribesItsRemainingChoices(t *testing.T) {
	cmd := NewSetupCommand()
	flag := cmd.Flags().Lookup("interactive")
	if flag == nil {
		t.Fatal("setup missing --interactive")
	}
	if got, want := flag.Usage, "Revisit inferred workspace and API-key choices"; got != want {
		t.Errorf("--interactive help = %q, want %q", got, want)
	}
	for _, phrase := range []string{"guided setup", "revisit inferred workspace and API-key choices", "--no-input"} {
		if !strings.Contains(cmd.Long, phrase) {
			t.Errorf("setup long help does not contain %q:\n%s", phrase, cmd.Long)
		}
	}
}
```

`setup_test.go` already imports `strings`.

- [ ] **Step 2: Verify the help test fails**

```sh
go test ./cli/custom/commands -run TestSetupInteractiveFlagDescribesItsRemainingChoices
```

Expected: FAIL because usage currently says `Ask about every choice instead of inferring`.

- [ ] **Step 3: Correct the long and flag help**

Replace the complete `Long` field with this valid Go expression:

```go
		Long: bartolocli.Markdown(`Gets a new machine from zero to working: signs you in, creates a ` +
			`workspace API key, and wires your coding agents to route model calls through the orq AI Gateway.

Run it bare for guided setup, with ` + "`-i`" + ` to revisit inferred workspace and API-key choices,
or fully flagged with ` + "`--no-input`" + ` for CI.

Supported agents: ` + strings.Join(agentIDs(), ", ") + `.

Credential order is ` + "`--api-key`" + ` → login session → ` + "`ORQ_API_KEY`" + `. Note this is
deliberately not the order ` + "`orq launch`" + ` uses: launch prefers an explicit
` + "`ORQ_API_KEY`" + ` over the session, because it configures one throwaway process. Setup
writes persistent configuration, so the workspace you picked in ` + "`orq auth login`" + `
wins over a key left exported in your shell.`),
```

Replace the flag declaration with:

```go
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Revisit inferred workspace and API-key choices")
```

Do not change flag behavior.

- [ ] **Step 4: Add the Unreleased changelog entry**

Insert directly below `## Unreleased`:

```markdown
- **Fixed: `orq setup` now shows the project picker during ordinary terminal
  setup.** With multiple projects it previously prompted only when the optional
  `-i` flag was present, so the default interactive flow silently selected the
  workspace's default project. Non-interactive and `--no-input` runs still use
  that default without prompting, and a sole project is still selected
  automatically.
```

- [ ] **Step 5: Format and run focused tests**

```sh
gofmt -w cli/custom/commands/setup.go cli/custom/commands/setup_test.go
go test ./cli/custom/commands -run 'TestSetupInteractiveFlag|TestProjectStep|TestSetupNoProject'
```

Expected: all selected tests pass.

- [ ] **Step 6: Commit**

```sh
git add cli/custom/commands/setup.go cli/custom/commands/setup_test.go CHANGELOG.md
git commit -m "docs(setup): clarify interactive project selection"
```

### Task 3: Verify both modules and the stable surface

**Files:**
- Verify: all files changed in Tasks 1 and 2
- Verify unchanged: `surface.json`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: CI-equivalent evidence for both CLI lines.

- [ ] **Step 1: Run root checks**

```sh
go test ./... && go vet ./...
```

Expected: both commands exit 0.

- [ ] **Step 2: Verify formatting**

```sh
test -z "$(gofmt -l $(git ls-files '*.go'))"
```

Expected: exit 0 with no output.

- [ ] **Step 3: Build and vet rc**

```sh
cd packages/orq-rc && go build ./... && go vet ./...
```

Expected: both commands exit 0. Return to the repository root afterwards.

- [ ] **Step 4: Verify surface and changelog tooling**

```sh
go run ./cmd/surface-dump -check
python3 scripts/stamp-changelog.py --self-test
git diff --exit-code origin/main -- surface.json
```

Expected: both commands exit 0 and `surface.json` remains unchanged.

- [ ] **Step 5: Inspect the final diff**

```sh
git status --short
git diff origin/main... --stat
git diff origin/main... --check
```

Expected: only the design, plan, setup implementation/tests, and changelog are present; no whitespace errors.
