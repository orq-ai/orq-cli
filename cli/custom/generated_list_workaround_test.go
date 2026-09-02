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
