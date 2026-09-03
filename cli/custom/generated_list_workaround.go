package custom

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
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
	if rows == nil {
		rows = []map[string]interface{}{}
	}
	view := maps.Clone(object)
	view[rowField] = rows
	for _, key := range []string{"items", "data", "results", "records", "entries", "servers"} {
		if key != rowField && isObjectRowArray(view[key]) {
			delete(view, key)
		}
	}
	view["data"] = rows
	return view, nil
}

func isObjectRowArray(value interface{}) bool {
	switch rows := value.(type) {
	case []map[string]interface{}:
		return true
	case []interface{}:
		for _, row := range rows {
			if _, ok := row.(map[string]interface{}); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

const generatedListAnnotation = "orq.ai/eng-2942-list-format"

type generatedListOperation struct {
	path             []string
	rowField         string
	columns          []string
	requiredInStable bool
}

// Delete this ENG-2942 workaround only after both schemas generate list formatting
// for all 13 operations and their x-cli-list-fields replace the local columns below.
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
