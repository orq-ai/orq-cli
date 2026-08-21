package launch

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const codexTemplateCatalog = `{"models":[{"slug":"gpt-5.4","display_name":"GPT-5.4","base_instructions":"You are Codex.","priority":1000}]}`

func codexCtx(envMap map[string]string, flags GatewayFlags, probe func(string, ...string) (string, error)) *AgentContext {
	return &AgentContext{
		Creds:     &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv:    env(envMap),
		Flags:     flags,
		Fetch:     fetcherOf("openai/gpt-5.4"),
		ExecProbe: probe,
	}
}

func okProbe(_ string, _ ...string) (string, error) {
	return "some log line\n" + codexTemplateCatalog, nil
}

func TestCodexOverrideArgs(t *testing.T) {
	got := BuildCodexOverrideArgs("openai/gpt-5.4", "https://api.orq.ai/v3/router", "")
	want := []string{
		"-c", `model="openai/gpt-5.4"`,
		"-c", `model_provider="orq"`,
		"-c", `model_providers.orq.name="Orq AI Gateway"`,
		"-c", `model_providers.orq.base_url="https://api.orq.ai/v3/router"`,
		"-c", `model_providers.orq.env_key="ORQ_API_KEY"`,
		"-c", `model_providers.orq.wire_api="responses"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}

	got = BuildCodexOverrideArgs("openai/gpt-5.4", "https://api.orq.ai/v3/router", "/tmp/cat.json")
	if got[len(got)-1] != `model_catalog_json="/tmp/cat.json"` {
		t.Fatalf("catalog arg: %v", got[len(got)-1])
	}
}

func TestCodexModelCatalog(t *testing.T) {
	catalogJSON, err := BuildCodexModelCatalog(codexTemplateCatalog, []string{"openai/gpt-5.4", "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal([]byte(catalogJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Models) != 2 ||
		parsed.Models[0]["slug"] != "openai/gpt-5.4" ||
		parsed.Models[1]["slug"] != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("models: %+v", parsed.Models)
	}
	if parsed.Models[0]["base_instructions"] != "You are Codex." {
		t.Fatalf("template fields not inherited: %+v", parsed.Models[0])
	}
	if parsed.Models[0]["priority"].(float64) <= parsed.Models[1]["priority"].(float64) {
		t.Fatal("priority should decrease")
	}

	if _, err := BuildCodexModelCatalog(`{"models":[]}`, []string{"x"}); err == nil {
		t.Fatal("empty template should error")
	}
}

func TestExtractJSONFromCodexOutput(t *testing.T) {
	got, err := ExtractJSONFromCodexOutput("log\nmore log\n{\"models\":[]}")
	if err != nil || got != `{"models":[]}` {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ExtractJSONFromCodexOutput("no json here"); err == nil {
		t.Fatal("want error")
	}
}

func TestCodexResolve(t *testing.T) {
	plan, err := resolveCodex(codexCtx(nil, GatewayFlags{}, okProbe))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cleanup != nil {
		defer plan.Cleanup()
	}
	if plan.Env["ORQ_API_KEY"] != "orq-key" || len(plan.Env) != 1 {
		t.Fatalf("env: %v", plan.Env)
	}
	joined := strings.Join(plan.PreArgs, " ")
	if !strings.Contains(joined, `model="openai/gpt-5.4"`) || !strings.Contains(joined, "model_catalog_json=") {
		t.Fatalf("preargs: %v", plan.PreArgs)
	}
	if len(plan.TempDirs) != 1 {
		t.Fatalf("tempdirs: %v", plan.TempDirs)
	}
	if _, err := os.Stat(plan.TempDirs[0].HostPath); err != nil {
		t.Fatalf("catalog dir missing: %v", err)
	}
}

func TestCodexResolveCatalogFailureWarns(t *testing.T) {
	plan, err := resolveCodex(codexCtx(nil, GatewayFlags{},
		func(_ string, _ ...string) (string, error) { return "", errors.New("codex missing") }))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "could not build codex model catalog") {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
	if strings.Contains(strings.Join(plan.PreArgs, " "), "model_catalog_json") {
		t.Fatal("catalog arg must be absent on failure")
	}
}

func TestCodexCleanupRemovesTempDir(t *testing.T) {
	plan, err := resolveCodex(codexCtx(nil, GatewayFlags{}, okProbe))
	if err != nil {
		t.Fatal(err)
	}
	dir := plan.TempDirs[0].HostPath
	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("tempdir not removed: %v", err)
	}
}
