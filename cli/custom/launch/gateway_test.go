package launch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var noPrefix = MakeNormalizeModel(nil)

func TestParseModelList(t *testing.T) {
	got, err := ParseModelList("openai/gpt-5-mini, anthropic/claude-sonnet-4-6")
	if err != nil || !reflect.DeepEqual(got, []string{"openai/gpt-5-mini", "anthropic/claude-sonnet-4-6"}) {
		t.Fatalf("comma list: got %v, %v", got, err)
	}

	got, err = ParseModelList(`["openai/gpt-5-mini","anthropic/claude-sonnet-4-6"]`)
	if err != nil || !reflect.DeepEqual(got, []string{"openai/gpt-5-mini", "anthropic/claude-sonnet-4-6"}) {
		t.Fatalf("json list: got %v, %v", got, err)
	}

	if _, err = ParseModelList(`["ok",1]`); err == nil {
		t.Fatal("mixed-type json should error")
	}
	if _, err = ParseModelList(`[not json`); err == nil {
		t.Fatal("bad json should error")
	}

	got, err = ParseModelList("  ")
	if err != nil || len(got) != 0 {
		t.Fatalf("blank: got %v, %v", got, err)
	}
}

func TestNormalizeModel(t *testing.T) {
	normalize := MakeNormalizeModel([]string{"orq", "orq-openai"})
	if normalize("orq/openai/gpt-5-mini") != "openai/gpt-5-mini" {
		t.Fatal("orq/ prefix not stripped")
	}
	if normalize("orq-openai/openai/gpt-5-mini") != "openai/gpt-5-mini" {
		t.Fatal("orq-openai/ prefix not stripped")
	}
	if normalize("openai/gpt-5-mini") != "openai/gpt-5-mini" {
		t.Fatal("bare id changed")
	}
}

func TestIsResponsesModel(t *testing.T) {
	if !IsResponsesModel("openai/gpt-5-mini") || IsResponsesModel("anthropic/claude-sonnet-4-6") {
		t.Fatal("responses detection wrong")
	}
}

func TestShouldWarnMissingProviderPrefix(t *testing.T) {
	if !ShouldWarnMissingProviderPrefix("gpt-5-mini", noPrefix) {
		t.Fatal("bare model should warn")
	}
	if ShouldWarnMissingProviderPrefix("openai/gpt-5-mini", noPrefix) {
		t.Fatal("prefixed model should not warn")
	}
}

func TestDedupeModels(t *testing.T) {
	normalize := MakeNormalizeModel([]string{"orq"})
	got := DedupeModels([]string{"a/x", "orq/a/x", "", "b/y"}, normalize)
	if !reflect.DeepEqual(got, []string{"a/x", "b/y"}) {
		t.Fatalf("got %v", got)
	}
}

func fetcherOf(ids ...string) ModelFetcher {
	return func(_, _ string) ([]ModelInfo, error) {
		var out []ModelInfo
		for _, id := range ids {
			out = append(out, ModelInfo{ID: id})
		}
		return out, nil
	}
}

func resolveOpts(overrides func(*ResolveInput)) ResolveInput {
	in := ResolveInput{
		AuthToken:     "key",
		APIBaseURL:    DefaultGatewayAPIBaseURL,
		Getenv:        env(nil),
		Normalize:     noPrefix,
		DefaultModel:  "openai/gpt-5-mini",
		BaseURLEnvKey: "ORQ_OPENCODE_BASE_URL",
		ModelEnvKey:   "OPENCODE_MODEL",
		ModelsEnvKey:  "OPENCODE_MODELS",
		Fetch:         fetcherOf("openai/gpt-5-mini"),
	}
	if overrides != nil {
		overrides(&in)
	}
	return in
}

func TestResolveNoAuth(t *testing.T) {
	_, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) { in.AuthToken = "" }))
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not-logged-in error, got %v", err)
	}
}

func TestResolveBaseURLPriority(t *testing.T) {
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Getenv = env(map[string]string{"ORQ_OPENCODE_BASE_URL": "https://env.example/v3/router"})
		in.Flags.BaseURL = "https://flag.example/v3/router"
	}))
	if err != nil || got.BaseURL != "https://flag.example/v3/router" {
		t.Fatalf("flag should win: %v, %v", got, err)
	}

	got, _ = ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Getenv = env(map[string]string{"ORQ_OPENCODE_BASE_URL": "https://env.example/v3/router"})
	}))
	if got.BaseURL != "https://env.example/v3/router" {
		t.Fatalf("agent env should win over default: %v", got.BaseURL)
	}

	got, _ = ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Getenv = env(map[string]string{"ORQ_GATEWAY_URL": "https://gw.example/v3/router"})
	}))
	if got.BaseURL != "https://gw.example/v3/router" {
		t.Fatalf("ORQ_GATEWAY_URL should win over default: %v", got.BaseURL)
	}

	got, _ = ResolveGatewayConfig(resolveOpts(nil))
	if got.BaseURL != DefaultGatewayBaseURL {
		t.Fatalf("default: %v", got.BaseURL)
	}
}

func TestResolveModelFlagBeforeEnv(t *testing.T) {
	normalize := MakeNormalizeModel([]string{"orq", "orq-openai"})
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Normalize = normalize
		in.Getenv = env(map[string]string{"OPENCODE_MODEL": "openai/env-model"})
		in.Flags.Model = "orq/openai/flag-model"
	}))
	if err != nil || got.GatewayModel != "openai/flag-model" {
		t.Fatalf("flag model: %v, %v", got, err)
	}
}

func TestResolvePrefersDefaultWhenFetched(t *testing.T) {
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Fetch = fetcherOf("anthropic/claude-sonnet-4-6", "openai/gpt-5-mini")
	}))
	if err != nil || got.GatewayModel != "openai/gpt-5-mini" {
		t.Fatalf("default-if-fetched: %v, %v", got, err)
	}
	if !reflect.DeepEqual(got.GatewayModels, []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5-mini"}) {
		t.Fatalf("models: %v", got.GatewayModels)
	}
}

func TestResolveFirstFetchedWhenDefaultAbsent(t *testing.T) {
	got, _ := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Fetch = fetcherOf("anthropic/claude-sonnet-4-6")
	}))
	if got.GatewayModel != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("first fetched: %v", got.GatewayModel)
	}
}

func TestResolveMergesExplicitModels(t *testing.T) {
	got, _ := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Flags.Models = "anthropic/claude-sonnet-4-6"
	}))
	if !reflect.DeepEqual(got.GatewayModels, []string{"openai/gpt-5-mini", "anthropic/claude-sonnet-4-6"}) {
		t.Fatalf("merge: %v", got.GatewayModels)
	}
}

func TestResolveSkipsFetch(t *testing.T) {
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Flags.NoFetchModels = true
		in.Fetch = func(_, _ string) ([]ModelInfo, error) {
			t.Fatal("fetcher should not be called")
			return nil, nil
		}
	}))
	if err != nil || got.ModelFetchWarning != "" {
		t.Fatalf("skip fetch: %v, %v", got, err)
	}
	if !reflect.DeepEqual(got.GatewayModels, []string{"openai/gpt-5-mini"}) {
		t.Fatalf("models: %v", got.GatewayModels)
	}
}

func TestResolveFetchFailureWarns(t *testing.T) {
	got, err := ResolveGatewayConfig(resolveOpts(func(in *ResolveInput) {
		in.Flags.Model = "openai/manual-model"
		in.Fetch = func(_, _ string) ([]ModelInfo, error) { return nil, errors.New("network failed") }
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayModel != "openai/manual-model" {
		t.Fatalf("model: %v", got.GatewayModel)
	}
	if !strings.Contains(got.ModelFetchWarning, "Could not fetch enabled models") {
		t.Fatalf("warning: %q", got.ModelFetchWarning)
	}
}

func TestFetchEnabledModelsHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/models" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("auth header: %s", r.Header.Get("Authorization"))
		}
		// Top-level array, matching the real endpoint shape.
		w.Write([]byte(`[
			{"provider":"openai","model_id":"gpt-5-mini","model_type":"chat","enabled":true,
			 "metadata":{"context_window":400000,"max_output_tokens":128000}},
			{"provider":"openai","model_id":"dall-e","model_type":"image","enabled":true},
			{"provider":"anthropic","model_id":"claude-sonnet-4-6","model_type":"chat","enabled":false},
			{"provider":"anthropic","model_id":"claude-haiku-4-5","model_type":"chat","enabled":true}
		]`))
	}))
	defer srv.Close()

	got, err := FetchEnabledModels("test-key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelInfo{
		{ID: "anthropic/claude-haiku-4-5"},
		{ID: "openai/gpt-5-mini", ContextWindow: 400000, MaxOutputTokens: 128000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchEnabledModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := FetchEnabledModels("k", srv.URL); err == nil {
		t.Fatal("want error on 500")
	}
}
