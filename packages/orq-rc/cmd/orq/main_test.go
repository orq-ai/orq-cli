package main

import (
	"reflect"
	"testing"
)

// TestTraceAPIWiresEveryOperation fails when commands.TraceAPI grows a field
// this binary does not fill: an unfilled one is not a compile error, it is a
// feature the rc build silently lacks at runtime.
func TestTraceAPIWiresEveryOperation(t *testing.T) {
	api := reflect.ValueOf(traceAPI())
	for index := 0; index < api.NumField(); index++ {
		if api.Field(index).IsNil() {
			t.Errorf("TraceAPI.%s is nil", api.Type().Field(index).Name)
		}
	}
}
