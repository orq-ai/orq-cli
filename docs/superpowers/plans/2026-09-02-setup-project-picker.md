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

Add before `resolveProjectStep`:

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

- [ ] **Step 6: Format and run focused tests**

```sh
gofmt -w cli/custom/commands/setup.go cli/custom/commands/setup_project.go cli/custom/commands/setup_project_test.go
go test ./cli/custom/commands -run 'TestProjectStep|TestSetupNoProject'
```

Expected: all selected tests pass.

- [ ] **Step 7: Commit**

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
- Produces: help that reserves `-i` for forced workspace reselection and API-key confirmation.

- [ ] **Step 1: Write the failing help test**

Add after `TestSetupHasNoAgentFlags`:

```go
func TestSetupInteractiveFlagDescribesItsRemainingChoices(t *testing.T) {
	cmd := NewSetupCommand()
	flag := cmd.Flags().Lookup("interactive")
	if flag == nil {
		t.Fatal("setup missing --interactive")
	}
	if got, want := flag.Usage, "Re-select the workspace and ask before creating an API key"; got != want {
		t.Errorf("--interactive help = %q, want %q", got, want)
	}
	if strings.Contains(cmd.Long, "asked about every choice") {
		t.Errorf("setup long help still claims -i controls every choice:\n%s", cmd.Long)
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

Replace the long-help sentence with this Go source:

```go
Run it bare for guided setup, with ` + "`-i`" + ` to re-select the workspace and ask before
creating an API key, or fully flagged with ` + "`--no-input`" + ` for CI.
```

Replace the flag declaration with:

```go
	f.BoolVarP(&opts.interactive, "interactive", "i", false, "Re-select the workspace and ask before creating an API key")
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
```

Expected: both commands exit 0 and `surface.json` remains unchanged.

- [ ] **Step 5: Inspect the final diff**

```sh
git status --short
git diff origin/main... --stat
git diff origin/main... --check
```

Expected: only the design, plan, setup implementation/tests, and changelog are present; no whitespace errors.
