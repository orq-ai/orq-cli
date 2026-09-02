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
