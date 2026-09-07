# Installed Skills Version Reporting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Report the installed orq skills bundle version in "orq doctor" and "orq connect --status".

**Architecture:** Derive one installed-version value inside cli/custom/skills by reading the source commit from the materialized snapshot and falling back to the manifest fingerprint. Add that value to skills.Status; the doctor and connect commands format the same value without changing persistence, refresh, or command registration.

**Tech Stack:** Go 1.25, the Go standard library (encoding/json, os, filepath, strings), Cobra command tests, and the existing skills manifest/status APIs.

## Global Constraints

- Structured doctor output uses the full assistant-plugins source commit.
- Human output abbreviates versions longer than seven characters to seven characters.
- Missing, malformed, or blank SOURCE.json metadata falls back to the manifest fingerprint.
- The manifest schema stays at version 1; do not add persisted fields.
- A machine with no permanent skills install does not gain a skills check or version line.
- Connect status reports a version only when skills are requested and a selected agent has a recorded permanent link in the current view.
- Installation, refresh, ownership, and removal behavior must not change.
- Do not add commands or flags; surface.json must remain unchanged.

---

### Task 1: Derive the installed bundle version

**Files:**
- Create: cli/custom/skills/version.go
- Create: cli/custom/skills/version_test.go
- Modify: cli/custom/skills/status.go:69-92

**Interfaces:**
- Consumes: Manifest.Generation, Manifest.Fingerprint, and snapshot SOURCE.json.
- Produces: Status.Version string and installedVersion(*Manifest) string.

- [ ] **Step 1: Write failing tests**

Create cli/custom/skills/version_test.go:

~~~go
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusReportsInstalledSourceCommit(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	gen := filepath.Join(home, ".orq", "snapshot", "gen-test")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(filepath.Join(gen, "SOURCE.json"), []byte("{\"commit\":\""+commit+"\"}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(&Manifest{Fingerprint: "fingerprint", Generation: gen}); err != nil {
		t.Fatal(err)
	}

	status, err := ReadStatus()
	if err != nil || status == nil {
		t.Fatalf("ReadStatus() = %+v, %v", status, err)
	}
	if status.Version != commit {
		t.Errorf("Version = %q, want %q", status.Version, commit)
	}
}

func TestReadStatusFallsBackToFingerprintWithoutSourceCommit(t *testing.T) {
	for name, source := range map[string]string{
		"missing":   "",
		"malformed": "{not-json}",
		"blank":     "{\"commit\":\"\"}",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			setHome(t, home)
			gen := filepath.Join(home, ".orq", "snapshot", "gen-test")
			if err := os.MkdirAll(gen, 0o755); err != nil {
				t.Fatal(err)
			}
			if source != "" {
				if err := os.WriteFile(filepath.Join(gen, "SOURCE.json"), []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := SaveManifest(&Manifest{Fingerprint: "bundle-fingerprint", Generation: gen}); err != nil {
				t.Fatal(err)
			}

			status, err := ReadStatus()
			if err != nil || status == nil {
				t.Fatalf("ReadStatus() = %+v, %v", status, err)
			}
			if status.Version != "bundle-fingerprint" {
				t.Errorf("Version = %q, want fingerprint fallback", status.Version)
			}
		})
	}
}
~~~

- [ ] **Step 2: Verify the tests fail for the missing field**

Run:

~~~sh
go test ./cli/custom/skills -run 'TestReadStatus(ReportsInstalledSourceCommit|FallsBackToFingerprintWithoutSourceCommit)$'
~~~

Expected: FAIL with "status.Version undefined".

- [ ] **Step 3: Implement source parsing and fallback**

Create cli/custom/skills/version.go:

~~~go
package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type sourceMetadata struct {
	Commit string `json:"commit"`
}

// installedVersion names the exact bundle an existing projection uses. Source
// metadata is diagnostic, so damage falls back to the refresh fingerprint.
func installedVersion(m *Manifest) string {
	fallback := strings.TrimSpace(m.Fingerprint)
	if strings.TrimSpace(m.Generation) == "" {
		return fallback
	}
	data, err := os.ReadFile(filepath.Join(m.Generation, "SOURCE.json"))
	if err != nil {
		return fallback
	}
	var source sourceMetadata
	if json.Unmarshal(data, &source) != nil {
		return fallback
	}
	if commit := strings.TrimSpace(source.Commit); commit != "" {
		return commit
	}
	return fallback
}
~~~

Add Version to Status in cli/custom/skills/status.go and initialize it in ReadStatus:

~~~go
type Status struct {
	Links []LinkStatus
	// Version identifies the installed bundle: source commit when available,
	// content fingerprint otherwise.
	Version string
	// Stale reports that the recorded fingerprint is behind this CLI's.
	Stale bool
}
~~~

~~~go
	s := &Status{
		Version: installedVersion(m),
		Stale:   m.Fingerprint != Fingerprint(),
	}
~~~

- [ ] **Step 4: Format and verify the focused tests pass**

Run:

~~~sh
gofmt -w cli/custom/skills/version.go cli/custom/skills/version_test.go cli/custom/skills/status.go
go test ./cli/custom/skills -run 'TestReadStatus(ReportsInstalledSourceCommit|FallsBackToFingerprintWithoutSourceCommit)$'
~~~

Expected: PASS.

- [ ] **Step 5: Run the whole skills package**

Run: "go test ./cli/custom/skills"

Expected: PASS.

- [ ] **Step 6: Commit**

~~~sh
git add cli/custom/skills/version.go cli/custom/skills/version_test.go cli/custom/skills/status.go
git commit -m "feat(skills): expose installed bundle version"
~~~

---

### Task 2: Show the version in doctor and connect status

**Files:**
- Modify: cli/custom/commands/agents.go:1179-1253
- Modify: cli/custom/commands/doctor_test.go:177-353
- Modify: cli/custom/commands/connect.go:354-442,1170-1240
- Modify: cli/custom/commands/connect_test.go:1277-1313

**Interfaces:**
- Consumes: skills.Status.Version from Task 1.
- Produces: shortSkillsVersion(string) string, doctor details.version, doctor message suffix, and connect status version line.

- [ ] **Step 1: Add failing doctor assertions**

Inside TestSkillsCheck in doctor_test.go, add this constant before newManifest:

~~~go
	const testSkillsVersion = "0123456789abcdef0123456789abcdef01234567"
~~~

After gen is assigned inside newManifest, materialize its SOURCE.json:

~~~go
		if err := os.MkdirAll(gen, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gen, "SOURCE.json"), []byte("{\"commit\":\""+testSkillsVersion+"\"}"), 0o644); err != nil {
			t.Fatal(err)
		}
~~~

In the "all present passes" subtest, add:

~~~go
		if check.Details["version"] != testSkillsVersion {
			t.Errorf("version = %v, want %s", check.Details["version"], testSkillsVersion)
		}
		if !strings.Contains(check.Message, "version "+testSkillsVersion[:7]) {
			t.Errorf("human message does not include the short version: %q", check.Message)
		}
~~~

In the stale-install subtest, add:

~~~go
		if !strings.Contains(check.Message, "version "+testSkillsVersion[:7]) {
			t.Errorf("stale message does not include the installed version: %q", check.Message)
		}
~~~

- [ ] **Step 2: Add failing connect status assertions**

In TestConnectStatusGroupsByAgent, after the connect call, derive the expected displayed version:

~~~go
	status, err := skills.ReadStatus()
	if err != nil || status == nil {
		t.Fatalf("ReadStatus() = %+v, %v", status, err)
	}
	wantVersion := status.Version
	if len(wantVersion) > 7 {
		wantVersion = wantVersion[:7]
	}
~~~

Immediately after the block that reports `status did not report claude's skills`, add:

~~~go
	if !strings.Contains(out, "skills version "+wantVersion) {
		t.Errorf("status did not report installed skills version %q:\n%s", wantVersion, out)
	}
~~~

Add this scoping test after TestConnectStatusGroupsByAgent:

~~~go
func TestConnectStatusOmitsSkillsVersionWhenSkillsWereNotRequested(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false, false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "mcp", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if strings.Contains(out, "skills version") {
		t.Errorf("MCP-only status reported unrelated skills metadata:\n%s", out)
	}
}
~~~

- [ ] **Step 3: Verify the command tests fail for missing output**

Run:

~~~sh
go test ./cli/custom/commands -run 'TestSkillsCheck|TestConnectStatus(GroupsByAgent|OmitsSkillsVersionWhenSkillsWereNotRequested)$'
~~~

Expected: FAIL because doctor details/messages and connect status do not report the version.

- [ ] **Step 4: Add shared human formatting and doctor output**

Add near skillsCheck in agents.go:

~~~go
func shortSkillsVersion(version string) string {
	if len(version) > 7 {
		return version[:7]
	}
	return version
}
~~~

Add version to the doctor details map:

~~~go
		Details: map[string]any{
			"recorded":  recorded,
			"missing":   missing,
			"foreign":   foreign,
			"elsewhere": elsewhere,
			"stale":     status.Stale,
			"version":   status.Version,
		},
~~~

Immediately before the current `return check, true` at the end of `skillsCheck`, add:

~~~go
	if status.Version != "" {
		check.Message += fmt.Sprintf(" (version %s)", shortSkillsVersion(status.Version))
	}
	return check, true
~~~

- [ ] **Step 5: Add scoped connect status output**

In `runConnectStatus`, replace the block that calls only
`reportBrokenSkillLinks(rep, agents)` with:

~~~go
	if hasCap(caps, capSkills) {
		reportSkillsVersion(rep, agents)
		reportBrokenSkillLinks(rep, agents)
	}
~~~

Add immediately before reportBrokenSkillLinks in connect.go:

~~~go
// reportSkillsVersion names the bundle behind permanent links visible to this
// request. Other projects and session-only links do not make it appear.
func reportSkillsVersion(rep *reporter, agents []string) {
	status, err := skills.ReadStatus()
	if err != nil || status == nil || status.Version == "" {
		return
	}
	for _, link := range status.Links {
		if skills.Belongs(link.Agent, agents) && link.Place != skills.PlaceElsewhere {
			rep.info("skills version %s", shortSkillsVersion(status.Version))
			return
		}
	}
}
~~~

- [ ] **Step 6: Format and run focused tests**

~~~sh
gofmt -w cli/custom/commands/agents.go cli/custom/commands/doctor_test.go cli/custom/commands/connect.go cli/custom/commands/connect_test.go
go test ./cli/custom/commands -run 'TestSkillsCheck|TestConnectStatus(GroupsByAgent|OmitsSkillsVersionWhenSkillsWereNotRequested)$'
~~~

Expected: PASS.

- [ ] **Step 7: Run all custom command tests**

Run: "go test ./cli/custom/commands"

Expected: PASS.

- [ ] **Step 8: Commit**

~~~sh
git add cli/custom/commands/agents.go cli/custom/commands/doctor_test.go cli/custom/commands/connect.go cli/custom/commands/connect_test.go
git commit -m "feat(skills): report installed bundle version"
~~~

---

### Task 3: Document the output contract

**Files:**
- Modify: CHANGELOG.md:112
- Modify: README.md:95,210-240

**Interfaces:**
- Consumes: doctor details.version and connect status output from Task 2.
- Produces: Unreleased release note and user guidance.

- [ ] **Step 1: Add the Unreleased changelog entry**

Under "## Unreleased":

~~~markdown
- **Added:** `orq doctor` and `orq connect --status` now report the version of
  the installed orq agent-skills bundle. Human output shows the abbreviated
  source commit, while `doctor --json` exposes the full value as
  `checks[id=skills].details.version`.
~~~

- [ ] **Step 2: Update README**

After the connect-capabilities paragraph:

~~~markdown
`orq connect --status` reports the abbreviated source version of any installed
orq skills bundle in scope. `orq doctor` shows the same version in its skills
check, and `orq doctor --json` returns the full value at
`checks[id=skills].details.version`.
~~~

- [ ] **Step 3: Check the documentation diff**

~~~sh
git diff --check
git diff -- CHANGELOG.md README.md
~~~

Expected: no whitespace errors and only the intended output documentation.

- [ ] **Step 4: Commit**

~~~sh
git add CHANGELOG.md README.md
git commit -m "docs(skills): document bundle version output"
~~~

---

### Task 4: Verify both modules and the command surface

**Files:**
- Verify only: all changed files and surface.json

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces: evidence that both CLI modules pass and commands/flags are unchanged.

- [ ] **Step 1: Run focused packages without cached results**

~~~sh
go test -count=1 ./cli/custom/skills ./cli/custom/commands
~~~

Expected: both packages PASS.

- [ ] **Step 2: Build the stable CLI**

Run: "make build"

Expected: ./bin/orq builds successfully.

- [ ] **Step 3: Reproduce root-module CI**

~~~sh
go test ./... && go vet ./... && test -z "$(gofmt -l $(git ls-files '*.go'))"
~~~

Expected: tests and vet pass; formatting prints no filenames.

- [ ] **Step 4: Compile and vet the rc module**

~~~sh
cd packages/orq-rc && go build ./... && go vet ./...
~~~

Expected: both commands pass.

- [ ] **Step 5: Verify command surface stability**

From the repository root:

~~~sh
go run ./cmd/surface-dump -check
~~~

Expected: PASS and no surface.json diff.

- [ ] **Step 6: Inspect final state**

~~~sh
git diff --check
git status --short
git diff --stat origin/main...
git diff origin/main... -- surface.json
~~~

Expected: no whitespace errors, a clean worktree after commits, expected source/test/docs changes, and no surface.json changes.
