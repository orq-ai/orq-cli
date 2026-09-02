package main

import (
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"
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
