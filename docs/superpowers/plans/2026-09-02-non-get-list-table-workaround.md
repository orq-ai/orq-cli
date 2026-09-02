# Non-GET List Table Workaround Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the allowlisted POST-backed list commands render tables at a terminal while preserving their exact API envelopes for JSON, YAML, TOON, raw, and piped output.

**Architecture:** Wrap only the generated RunE handlers named in a declarative compatibility list. During one wrapped invocation, an adapter turns the generated Formatter.Format call into a direct call to the captured formatter's optional FormatList; for Bartolo's default terminal-table path, it aliases the configured top-level row field to data in a shallow presentation-only copy so empty nonconventional envelopes remain tabular. Stable paths are registration invariants, RC-only paths are optional in the stable tree and asserted in the RC module, and ENG-2942 is the removal boundary.

**Tech Stack:** Go 1.25, Cobra, Viper, Bartolo v0.10.0, net/http/httptest, root and packages/orq-rc Go modules.

## Global Constraints

- Never edit cli/generated/ or packages/orq-rc/cli/generated/; Bartolo regeneration replaces both trees.
- Put shared runtime behavior under cli/custom/ so stable and RC binaries compile against the same implementation.
- Preserve the complete API response shape for --json, YAML, TOON, --raw, custom formatters, pipes, and redirects.
- Restrict the workaround to the 13 reviewed operations; do not infer list behavior from arbitrary array-bearing responses.
- Keep logs query, traces query-oql, and logs get-context out of scope because they require nested or multiple row paths.
- Treat every stable path as required; only audit-logs query and knowledge-bases preview-chunks may be absent from the stable tree.
- Never call package-level bartolocli.FormatList from the adapter; call the captured delegate directly to avoid recursion.
- Do not mark command-execution tests t.Parallel; Bartolo, Cobra, Viper, and this CLI use process-global execution state.
- Do not add dependencies, edit VERSION, or change surface.json.
- Add a **Fixed:** entry under ## Unreleased in CHANGELOG.md.

## File Structure

- Create cli/custom/generated_list_workaround.go: metadata, formatter adapter, table-only envelope view, strict path resolution, and handler wrapping.
- Create cli/custom/generated_list_workaround_test.go: formatter, envelope, registration, restoration, and stable integration coverage.
- Create packages/orq-rc/cmd/orq/generated_list_workaround_test.go: staging-only path assertions.
- Modify cli/custom/register.go: install after registerCommands.
- Modify CHANGELOG.md: describe the terminal fix and unchanged machine output.

---

### Task 1: Build the formatter adapter and table-only envelope view

**Files:**
- Create: cli/custom/generated_list_workaround.go
- Create: cli/custom/generated_list_workaround_test.go

**Interfaces:**
- Consumes: bartolocli.ResponseFormatter, optional FormatList(interface{}, ...string) error, bartolocli.OutputFormat(), stdoutIsTerminal, and Viper raw state.
- Produces: generatedListFormatter.Format(interface{}) error and tableEnvelope(interface{}, string) (interface{}, error).

- [ ] **Step 1: Write the failing adapter tests**

Create cli/custom/generated_list_workaround_test.go with these compile-complete helpers and tests:

~~~go
package custom

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

type recordingFormatter struct{ formatted interface{} }

func (f *recordingFormatter) Format(data interface{}) error {
	f.formatted = data
	return nil
}

type recordingListFormatter struct {
	recordingFormatter
	listed  interface{}
	columns []string
}

func (f *recordingListFormatter) FormatList(data interface{}, columns ...string) error {
	f.listed = data
	f.columns = append([]string(nil), columns...)
	return nil
}

func TestGeneratedListFormatterCallsCapturedDelegate(t *testing.T) {
	delegate := &recordingListFormatter{}
	envelope := map[string]interface{}{"matches": []interface{}{}}
	formatter := generatedListFormatter{delegate: delegate, rowField: "matches", columns: []string{"id", "text"}}
	if err := formatter.Format(envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delegate.listed, envelope) || strings.Join(delegate.columns, ",") != "id,text" {
		t.Fatalf("listed=%#v columns=%v", delegate.listed, delegate.columns)
	}
}

func TestGeneratedListFormatterFallsBackToFormat(t *testing.T) {
	delegate := &recordingFormatter{}
	envelope := map[string]interface{}{"matches": []interface{}{}}
	if err := (generatedListFormatter{delegate: delegate, rowField: "matches"}).Format(envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delegate.formatted, envelope) {
		t.Fatalf("formatted=%#v", delegate.formatted)
	}
}

func TestGeneratedListFormatterHeadsEmptyNonconventionalRows(t *testing.T) {
	previousTerminal, previousStdout := stdoutIsTerminal, bartolocli.Stdout
	previousRaw := viper.GetBool("raw")
	t.Cleanup(func() {
		stdoutIsTerminal, bartolocli.Stdout = previousTerminal, previousStdout
		viper.Set("raw", previousRaw)
	})
	stdoutIsTerminal = func() bool { return true }
	viper.Set("raw", false)
	var output bytes.Buffer
	bartolocli.Stdout = &output
	restore, err := bartolocli.SetOutputFormat("table")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	formatter := generatedListFormatter{
		delegate: bartolocli.NewDefaultFormatter(false, true),
		rowField: "matches",
		columns:  []string{"id", "text"},
	}
	if err := formatter.Format(map[string]interface{}{"matches": []interface{}{}, "has_more": false}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "ID") || !strings.Contains(got, "TEXT") {
		t.Fatalf("empty response did not render headers: %q", got)
	}
}

func TestGeneratedListFormatterPreservesJSONEnvelope(t *testing.T) {
	previousTerminal, previousStdout := stdoutIsTerminal, bartolocli.Stdout
	t.Cleanup(func() { stdoutIsTerminal, bartolocli.Stdout = previousTerminal, previousStdout })
	stdoutIsTerminal = func() bool { return true }
	var output bytes.Buffer
	bartolocli.Stdout = &output
	restore, err := bartolocli.SetOutputFormat("json")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	formatter := generatedListFormatter{
		delegate: bartolocli.NewDefaultFormatter(false, true),
		rowField: "matches",
		columns:  []string{"id", "text"},
	}
	if err := formatter.Format(map[string]interface{}{"matches": []interface{}{}, "has_more": false}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, found := decoded["data"]; found {
		t.Fatalf("JSON gained table-only alias: %#v", decoded)
	}
	if _, found := decoded["matches"]; !found {
		t.Fatalf("JSON lost wire rows: %#v", decoded)
	}
}

func TestGeneratedListFormatterPreservesPipedEnvelope(t *testing.T) {
	previousTerminal, previousStdout := stdoutIsTerminal, bartolocli.Stdout
	t.Cleanup(func() { stdoutIsTerminal, bartolocli.Stdout = previousTerminal, previousStdout })
	stdoutIsTerminal = func() bool { return false }
	var output bytes.Buffer
	bartolocli.Stdout = &output
	restore, err := bartolocli.SetOutputFormat("table")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	formatter := generatedListFormatter{
		delegate: bartolocli.NewDefaultFormatter(false, false),
		rowField: "matches",
		columns:  []string{"id", "text"},
	}
	if err := formatter.Format(map[string]interface{}{"matches": []interface{}{}, "has_more": false}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, found := decoded["data"]; found {
		t.Fatalf("piped envelope gained table-only alias: %#v", decoded)
	}
	if _, found := decoded["matches"]; !found {
		t.Fatalf("piped envelope lost wire rows: %#v", decoded)
	}
}

func TestTableEnvelopeSelectsConfiguredRows(t *testing.T) {
	view, err := tableEnvelope(map[string]interface{}{
		"matches": []interface{}{map[string]interface{}{"id": "match-1"}},
		"events":  []interface{}{map[string]interface{}{"id": "event-1"}},
	}, "matches")
	if err != nil {
		t.Fatal(err)
	}
	object := view.(map[string]interface{})
	dataRows := object["data"].([]interface{})
	if got := dataRows[0].(map[string]interface{})["id"]; got != "match-1" {
		t.Fatalf("selected id=%v", got)
	}
	if _, found := object["matches"]; !found {
		t.Fatal("table view removed wire row field")
	}
}

func TestTableEnvelopeRejectsMissingConfiguredRows(t *testing.T) {
	_, err := tableEnvelope(map[string]interface{}{"events": []interface{}{}}, "matches")
	if err == nil || !strings.Contains(err.Error(), "row field") {
		t.Fatalf("error=%v", err)
	}
}
~~~

- [ ] **Step 2: Run the tests to verify the red state**

Run:

~~~bash
go test ./cli/custom -run 'TestGeneratedListFormatter|TestTableEnvelope' -count=1
~~~

Expected: build failure because generatedListFormatter and tableEnvelope do not exist.

- [ ] **Step 3: Implement the minimal adapter**

Create cli/custom/generated_list_workaround.go:

~~~go
package custom

import (
	"fmt"
	"maps"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

type listResponseFormatter interface {
	FormatList(interface{}, ...string) error
}

type generatedListFormatter struct {
	delegate bartolocli.ResponseFormatter
	rowField string
	columns  []string
}

func (f generatedListFormatter) Format(data interface{}) error {
	listFormatter, ok := f.delegate.(listResponseFormatter)
	if !ok {
		return f.delegate.Format(data)
	}
	formatted := data
	if _, isDefault := f.delegate.(*bartolocli.DefaultFormatter); isDefault &&
		stdoutIsTerminal() && !viper.GetBool("raw") && bartolocli.OutputFormat() == "table" {
		var err error
		formatted, err = tableEnvelope(data, f.rowField)
		if err != nil {
			return err
		}
	}
	return listFormatter.FormatList(formatted, f.columns...)
}

func tableEnvelope(data interface{}, rowField string) (interface{}, error) {
	object, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("generated list response is %T, want an object with row field %q", data, rowField)
	}
	rows, found := object[rowField]
	if !found {
		return nil, fmt.Errorf("generated list response has no configured row field %q", rowField)
	}
	view := maps.Clone(object)
	view["data"] = rows
	return view, nil
}
~~~

- [ ] **Step 4: Format the new Go files**

Run:

~~~bash
gofmt -w cli/custom/generated_list_workaround.go cli/custom/generated_list_workaround_test.go
~~~

Expected: gofmt exits 0 and rewrites only the two new files.

- [ ] **Step 5: Run the focused adapter tests**

Run:

~~~bash
go test ./cli/custom -run 'TestGeneratedListFormatter|TestTableEnvelope' -count=1
~~~

Expected: all focused tests pass.

- [ ] **Step 6: Commit the adapter**

Run:

~~~bash
git add cli/custom/generated_list_workaround.go cli/custom/generated_list_workaround_test.go
git commit -m "fix(output): adapt POST list responses for tables"
~~~

Expected: the commit succeeds.

---

### Task 2: Strictly wire stable and RC operations

**Files:**
- Modify: cli/custom/generated_list_workaround.go
- Modify: cli/custom/generated_list_workaround_test.go
- Modify: cli/custom/register.go
- Create: packages/orq-rc/cmd/orq/generated_list_workaround_test.go

**Interfaces:**
- Consumes: Task 1's adapter, Cobra root.Find([]string), and generated RunE handlers.
- Produces: generatedListOperation, generatedListOperations, wrapGeneratedListOperations, and annotation orq.ai/eng-2942-list-format.

- [ ] **Step 1: Write failing strict-registration tests**

Add errors, orq/cli/generated, and Cobra to the existing test imports, then append:

~~~go
func TestGeneratedListOperationsResolveInStableTree(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	generated.Register(root)
	if err := wrapGeneratedListOperations(root, generatedListOperations); err != nil {
		t.Fatal(err)
	}
	for _, operation := range generatedListOperations {
		if !operation.requiredInStable {
			continue
		}
		command, args, err := root.Find(operation.path)
		if err != nil || len(args) != 0 || command.Annotations[generatedListAnnotation] != "true" {
			t.Fatalf("required path %q was not wrapped: command=%v args=%v err=%v",
				strings.Join(operation.path, " "), command, args, err)
		}
	}
}

func TestGeneratedListOperationsEnforceAvailability(t *testing.T) {
	required := generatedListOperation{
		path: []string{"traces", "search"}, rowField: "data",
		columns: []string{"trace_id"}, requiredInStable: true,
	}
	if err := wrapGeneratedListOperations(&cobra.Command{Use: "orq"}, []generatedListOperation{required}); err == nil {
		t.Fatal("missing stable path was accepted")
	}
	optional := required
	optional.path, optional.requiredInStable = []string{"audit-logs", "query"}, false
	if err := wrapGeneratedListOperations(&cobra.Command{Use: "orq"}, []generatedListOperation{optional}); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedListOperationsRejectCommandWithoutRunE(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	parent := &cobra.Command{Use: "traces"}
	parent.AddCommand(&cobra.Command{Use: "search"})
	root.AddCommand(parent)
	operation := generatedListOperation{
		path: []string{"traces", "search"}, rowField: "data",
		columns: []string{"trace_id"}, requiredInStable: true,
	}
	err := wrapGeneratedListOperations(root, []generatedListOperation{operation})
	if err == nil || !strings.Contains(err.Error(), "has no RunE") {
		t.Fatalf("error=%v", err)
	}
}

func TestGeneratedListWrapperRestoresFormatter(t *testing.T) {
	previous := bartolocli.Formatter
	t.Cleanup(func() { bartolocli.Formatter = previous })
	delegate := &recordingListFormatter{}
	bartolocli.Formatter = delegate
	wantErr := errors.New("request failed")
	command := &cobra.Command{Use: "search", RunE: func(*cobra.Command, []string) error {
		if err := bartolocli.Formatter.Format(map[string]interface{}{"data": []interface{}{}}); err != nil {
			return err
		}
		return wantErr
	}}
	root := &cobra.Command{Use: "orq"}
	parent := &cobra.Command{Use: "traces"}
	parent.AddCommand(command)
	root.AddCommand(parent)
	operation := generatedListOperation{
		path: []string{"traces", "search"}, rowField: "data",
		columns: []string{"trace_id"}, requiredInStable: true,
	}
	if err := wrapGeneratedListOperations(root, []generatedListOperation{operation}); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); !errors.Is(err, wantErr) {
		t.Fatalf("RunE error=%v", err)
	}
	if bartolocli.Formatter != delegate || delegate.listed == nil {
		t.Fatal("formatter was not delegated to and restored")
	}
}

func TestGeneratedListWrapperRejectsNilFormatter(t *testing.T) {
	previous := bartolocli.Formatter
	t.Cleanup(func() { bartolocli.Formatter = previous })
	bartolocli.Formatter = nil
	command := &cobra.Command{Use: "search", RunE: func(*cobra.Command, []string) error { return nil }}
	root := &cobra.Command{Use: "orq"}
	parent := &cobra.Command{Use: "traces"}
	parent.AddCommand(command)
	root.AddCommand(parent)
	operation := generatedListOperation{
		path: []string{"traces", "search"}, rowField: "data",
		columns: []string{"trace_id"}, requiredInStable: true,
	}
	if err := wrapGeneratedListOperations(root, []generatedListOperation{operation}); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err == nil || !strings.Contains(err.Error(), "formatter is not initialized") {
		t.Fatalf("RunE error=%v", err)
	}
}

func TestGeneratedListWrapperRestoresFormatterAfterSuccessAndPanic(t *testing.T) {
	previous := bartolocli.Formatter
	t.Cleanup(func() { bartolocli.Formatter = previous })
	for _, panicValue := range []interface{}{nil, "boom"} {
		delegate := &recordingFormatter{}
		bartolocli.Formatter = delegate
		command := &cobra.Command{Use: "search", RunE: func(*cobra.Command, []string) error {
			if panicValue != nil {
				panic(panicValue)
			}
			return nil
		}}
		root := &cobra.Command{Use: "orq"}
		parent := &cobra.Command{Use: "traces"}
		parent.AddCommand(command)
		root.AddCommand(parent)
		operation := generatedListOperation{
			path: []string{"traces", "search"}, rowField: "data",
			columns: []string{"trace_id"}, requiredInStable: true,
		}
		if err := wrapGeneratedListOperations(root, []generatedListOperation{operation}); err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				recovered := recover()
				if recovered != panicValue {
					t.Fatalf("recovered=%v, want %v", recovered, panicValue)
				}
			}()
			if err := command.RunE(command, nil); err != nil {
				t.Fatal(err)
			}
		}()
		if bartolocli.Formatter != delegate {
			t.Fatalf("formatter was not restored for panic=%v", panicValue)
		}
	}
}

func TestGeneratedListOperationsAreIdempotent(t *testing.T) {
	previous := bartolocli.Formatter
	t.Cleanup(func() { bartolocli.Formatter = previous })
	command := &cobra.Command{Use: "search", RunE: func(*cobra.Command, []string) error { return nil }}
	root := &cobra.Command{Use: "orq"}
	parent := &cobra.Command{Use: "traces"}
	parent.AddCommand(command)
	root.AddCommand(parent)
	operation := generatedListOperation{
		path: []string{"traces", "search"}, rowField: "data",
		columns: []string{"trace_id"}, requiredInStable: true,
	}
	if err := wrapGeneratedListOperations(root, []generatedListOperation{operation}); err != nil {
		t.Fatal(err)
	}
	// Preserve the annotation but replace RunE with a sentinel. A second install
	// must leave it alone; wrapping it would reject the deliberately nil formatter.
	command.RunE = func(*cobra.Command, []string) error { return nil }
	bartolocli.Formatter = nil
	if err := wrapGeneratedListOperations(root, []generatedListOperation{operation}); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal("second installation stacked another wrapper")
	}
}
~~~

- [ ] **Step 2: Run the wiring tests to verify the red state**

Run:

~~~bash
go test ./cli/custom -run 'TestGeneratedListOperations|TestGeneratedListWrapper' -count=1
~~~

Expected: build failure because the operation metadata and wrappers do not exist.

- [ ] **Step 3: Add the complete allowlist and strict wrapper**

Add errors, strings, and Cobra to generated_list_workaround.go, then append:

~~~go
const generatedListAnnotation = "orq.ai/eng-2942-list-format"

type generatedListOperation struct {
	path             []string
	rowField         string
	columns          []string
	requiredInStable bool
}

// Delete this ENG-2942 compatibility metadata when both schemas generate list formatting.
var generatedListOperations = []generatedListOperation{
	{[]string{"documentation", "search"}, "results", []string{"path", "content"}, true},
	{[]string{"knowledge-bases", "list-chunks-paginated"}, "data", []string{"_id", "status", "enabled", "created"}, true},
	{[]string{"knowledge-bases", "search"}, "matches", []string{"id", "text"}, true},
	{[]string{"models", "azure-foundry-deployments"}, "deployments", []string{"id", "model", "publisher", "wire"}, true},
	{[]string{"reporting", "query"}, "data", []string{"timestamp", "dimensions", "metrics"}, true},
	{[]string{"webhooks", "query"}, "items", []string{"_id", "display_name", "enabled", "failure_count"}, true},
	{[]string{"logs", "aggregate"}, "buckets", []string{"timestamp", "total_count", "severity_counts"}, true},
	{[]string{"logs", "get-patterns"}, "data", []string{"id", "template", "count", "percentage"}, true},
	{[]string{"logs", "search"}, "data", []string{"id", "timestamp", "severity_text", "body"}, true},
	{[]string{"traces", "aggregate"}, "data", []string{"group", "metrics"}, true},
	{[]string{"traces", "search"}, "data", []string{"trace_id", "name", "status", "duration_ms"}, true},
	{[]string{"audit-logs", "query"}, "audit_logs", []string{"audit_log_id", "created_at", "action", "actor_display"}, false},
	{[]string{"knowledge-bases", "preview-chunks"}, "chunks", []string{"page_number", "text"}, false},
}

func installGeneratedListWorkarounds(root *cobra.Command) {
	if err := wrapGeneratedListOperations(root, generatedListOperations); err != nil {
		panic(fmt.Sprintf("install generated list workaround: %v", err))
	}
}

func wrapGeneratedListOperations(root *cobra.Command, operations []generatedListOperation) error {
	for _, operation := range operations {
		command, args, err := root.Find(operation.path)
		if err != nil || len(args) != 0 {
			if operation.requiredInStable {
				return fmt.Errorf("required generated command %q was not found", strings.Join(operation.path, " "))
			}
			continue
		}
		if command.RunE == nil {
			return fmt.Errorf("generated command %q has no RunE", strings.Join(operation.path, " "))
		}
		if command.Annotations != nil && command.Annotations[generatedListAnnotation] == "true" {
			continue
		}
		wrapGeneratedListCommand(command, operation)
	}
	return nil
}

func wrapGeneratedListCommand(command *cobra.Command, operation generatedListOperation) {
	original := command.RunE
	command.RunE = func(command *cobra.Command, args []string) error {
		delegate := bartolocli.Formatter
		if delegate == nil {
			return errors.New("bartolo formatter is not initialized")
		}
		bartolocli.Formatter = generatedListFormatter{
			delegate: delegate, rowField: operation.rowField, columns: operation.columns,
		}
		defer func() { bartolocli.Formatter = delegate }()
		return original(command, args)
	}
	if command.Annotations == nil {
		command.Annotations = make(map[string]string)
	}
	command.Annotations[generatedListAnnotation] = "true"
}
~~~

- [ ] **Step 4: Install after custom command registration**

In cli/custom/register.go, make this exact insertion:

~~~go
	registerCommands(root)
	installGeneratedListWorkarounds(root)
	// Help presentation: runs last so it sees the complete tree.
~~~

- [ ] **Step 5: Add the RC-tree regression test**

Create packages/orq-rc/cmd/orq/generated_list_workaround_test.go:

~~~go
package main

import (
	"testing"

	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"
	bartolocli "github.com/orq-ai/bartolo/cli"
)

func TestRCOnlyGeneratedListCommandsAreWrapped(t *testing.T) {
	bartolocli.Init(&bartolocli.Config{
		AppName: "orq", EnvPrefix: "ORQ", APIKeyEnvVar: "ORQ_API_KEY",
		SerializationFormat: "toon", Version: "test",
	})
	previousPreRun := bartolocli.PreRun
	bartolocli.PreRun = nil
	t.Cleanup(func() { bartolocli.PreRun = previousPreRun })
	root := bartolocli.Root
	generated.Register(root)
	custom.Register(root)
	for _, path := range [][]string{{"audit-logs", "query"}, {"knowledge-bases", "preview-chunks"}} {
		command, args, err := root.Find(path)
		if err != nil || len(args) != 0 {
			t.Fatalf("RC path %q did not resolve: command=%v args=%v err=%v", path, command, args, err)
		}
		if command.Annotations["orq.ai/eng-2942-list-format"] != "true" {
			t.Fatalf("RC path %q was not wrapped", path)
		}
	}
}
~~~

- [ ] **Step 6: Format the wiring changes**

Run:

~~~bash
gofmt -w cli/custom/generated_list_workaround.go cli/custom/generated_list_workaround_test.go cli/custom/register.go packages/orq-rc/cmd/orq/generated_list_workaround_test.go
~~~

Expected: gofmt exits 0.

- [ ] **Step 7: Run stable and RC wiring tests**

Run:

~~~bash
go test ./cli/custom -run 'TestGeneratedListOperations|TestGeneratedListWrapper' -count=1
(cd packages/orq-rc && go test ./cmd/orq -run TestRCOnlyGeneratedListCommandsAreWrapped -count=1)
~~~

Expected: stable wraps 11 required commands and RC wraps both staging-only commands.

- [ ] **Step 8: Commit strict stable and RC wiring**

Run:

~~~bash
git add cli/custom/generated_list_workaround.go cli/custom/generated_list_workaround_test.go cli/custom/register.go packages/orq-rc/cmd/orq/generated_list_workaround_test.go
git commit -m "fix(output): wire POST list commands to tables"
~~~

Expected: the commit succeeds.

---

### Task 3: Prove the real generated traces command end to end

**Files:**
- Modify: cli/custom/generated_list_workaround_test.go

**Interfaces:**
- Consumes: buildRoot(t), the real stable generated tree, Bartolo's HTTP client/default formatter, and root --server/-o flags.
- Produces: regression coverage for empty and populated tables and exact JSON output from orq traces search.

- [ ] **Step 1: Add the real-command integration test**

Add context, fmt, net/http, and net/http/httptest to the test imports, then append:

~~~go
func TestTracesSearchUsesTableOnlyForTerminalPresentation(t *testing.T) {
	testCases := []struct {
		name, response, format string
		assert                 func(*testing.T, string)
	}{
		{
			name: "empty table", format: "table",
			response: "{\"object\":\"list\",\"data\":[],\"has_more\":false,\"next_page_token\":\"\",\"meta\":{\"row_count\":0}}",
			assert: func(t *testing.T, output string) {
				if !strings.Contains(output, "TRACE_ID") || strings.Contains(output, "data[0]:") {
					t.Fatalf("empty response was not a headed table: %q", output)
				}
			},
		},
		{
			name: "populated table", format: "table",
			response: "{\"object\":\"list\",\"data\":[{\"trace_id\":\"trace-1\",\"name\":\"checkout\",\"status\":\"ok\",\"duration_ms\":42}],\"has_more\":false}",
			assert: func(t *testing.T, output string) {
				if !strings.Contains(output, "trace-1") || !strings.Contains(output, "checkout") {
					t.Fatalf("table lacks row: %q", output)
				}
			},
		},
		{
			name: "complete JSON", format: "json",
			response: "{\"object\":\"list\",\"data\":[],\"has_more\":false,\"next_page_token\":\"next-1\",\"meta\":{\"row_count\":0,\"request_id\":\"req-1\"}}",
			assert: func(t *testing.T, output string) {
				var decoded map[string]interface{}
				if err := json.Unmarshal([]byte(output), &decoded); err != nil {
					t.Fatal(err)
				}
				meta := decoded["meta"].(map[string]interface{})
				if decoded["object"] != "list" || decoded["next_page_token"] != "next-1" || meta["request_id"] != "req-1" {
					t.Fatalf("JSON envelope changed: %#v", decoded)
				}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/v3/traces/search" {
					t.Errorf("request=%s %s", request.Method, request.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, testCase.response)
			}))
			t.Cleanup(server.Close)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("ORQ_API_KEY", "test-key")
			root := buildRoot(t)
			previousTerminal, previousStdout := stdoutIsTerminal, bartolocli.Stdout
			previousStderr, previousFormatter := bartolocli.Stderr, bartolocli.Formatter
			t.Cleanup(func() {
				stdoutIsTerminal, bartolocli.Stdout = previousTerminal, previousStdout
				bartolocli.Stderr, bartolocli.Formatter = previousStderr, previousFormatter
			})
			stdoutIsTerminal = func() bool { return true }
			var stdout, stderr bytes.Buffer
			bartolocli.Stdout, bartolocli.Stderr = &stdout, &stderr
			bartolocli.Formatter = bartolocli.NewDefaultFormatter(false, true)
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{"--server", server.URL, "-o", testCase.format, "traces", "search"})
			if _, err := root.ExecuteContextC(context.Background()); err != nil {
				t.Fatalf("execute: %v; stderr=%q", err, stderr.String())
			}
			testCase.assert(t, stdout.String())
		})
	}
}
~~~

- [ ] **Step 2: Format the integration test**

Run:

~~~bash
gofmt -w cli/custom/generated_list_workaround_test.go
~~~

Expected: gofmt exits 0.

- [ ] **Step 3: Run the vertical regression test**

Run:

~~~bash
go test ./cli/custom -run TestTracesSearchUsesTableOnlyForTerminalPresentation -count=1 -v
~~~

Expected: all three vertical subtests pass.

- [ ] **Step 4: Run the complete custom suite**

Run:

~~~bash
go test ./cli/custom/... -count=1
~~~

Expected: the complete custom suite passes.

- [ ] **Step 5: Commit the integration coverage**

~~~bash
git add cli/custom/generated_list_workaround_test.go
git commit -m "test(output): cover POST list table rendering"
~~~

---

### Task 4: Document and verify the release

**Files:**
- Modify: CHANGELOG.md

**Interfaces:**
- Consumes: the completed implementation and both generated command trees.
- Produces: a user-visible release note and release-grade verification evidence.

- [ ] **Step 1: Add the Unreleased changelog entry**

Under ## Unreleased, add:

~~~markdown
- **Fixed: POST-backed list commands now render as terminal tables.** Search,
  query, aggregate, and preview commands such as orq traces search previously
  printed serialized TOON because Bartolo classified only GET operations as
  lists. Their default terminal output now uses headed tables, including for
  empty responses, while JSON, YAML, TOON, raw, and piped output preserve the
  complete API response envelope unchanged.
~~~

- [ ] **Step 2: Run root-module validation**

Run:

~~~bash
go test ./... && go vet ./... && test -z "$(gofmt -l $(git ls-files '*.go'))"
~~~

Expected: exit status 0 and no formatting output.

- [ ] **Step 3: Run RC-module validation**

Run:

~~~bash
(cd packages/orq-rc && go build ./... && go test ./... && go vet ./...)
~~~

Expected: exit status 0, including the RC-only wrapper test.

- [ ] **Step 4: Verify the public command surface**

Run:

~~~bash
go run ./cmd/surface-dump -check
~~~

Expected: exit status 0 and surface.json stays unchanged.

- [ ] **Step 5: Run CI-adjacent script checks**

Run:

~~~bash
dash -n install.sh
python3 scripts/stamp-changelog.py --self-test
git diff --check origin/main...
~~~

Expected: all commands exit 0 and the diff has no whitespace errors.

- [ ] **Step 6: Inspect the final scope**

Run:

~~~bash
git status --short
git diff --stat origin/main...
git diff -- cli/custom/generated_list_workaround.go cli/custom/generated_list_workaround_test.go cli/custom/register.go packages/orq-rc/cmd/orq/generated_list_workaround_test.go CHANGELOG.md
~~~

Expected: only planned hand-written code, tests, registration, changelog, spec, and plan changes; neither generated tree, VERSION, nor surface.json changes.

- [ ] **Step 7: Commit the changelog**

Run:

~~~bash
git add CHANGELOG.md
git commit -m "docs: document POST list table rendering"
~~~

Expected: the commit succeeds.

- [ ] **Step 8: Record the clean handoff**

Run:

~~~bash
git status --short
git log --oneline origin/main..HEAD
~~~

Expected: a clean worktree and conventional commits for design, adapter, wiring, regression coverage, and changelog.
