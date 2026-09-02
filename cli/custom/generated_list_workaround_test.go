package custom

import (
	"bytes"
	"encoding/json"
	"errors"
	"orq/cli/generated"
	"reflect"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
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

func TestGeneratedListOperationsResolveInStableTree(t *testing.T) {
	bartolocli.Init(&bartolocli.Config{
		AppName:             "orq",
		EnvPrefix:           "ORQ",
		APIKeyEnvVar:        "ORQ_API_KEY",
		SerializationFormat: "toon",
		Version:             "test",
	})
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
