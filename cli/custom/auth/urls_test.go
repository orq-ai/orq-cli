package auth

import "testing"

// Both SaaS hostnames must read as "hosted": sessions written before 4.15 hold
// api.orq.ai, and treating those as a self-hosted override would rewrite every
// agent's gateway URL and drop the dashboard links.
func TestIsHostedAPIBase(t *testing.T) {
	for _, in := range []string{DefaultAPIBaseURL, LegacyDefaultAPIBaseURL, "https://api.orq.ai/", " https://my.orq.ai "} {
		if !IsHostedAPIBase(in) {
			t.Errorf("%q should be hosted", in)
		}
	}
	for _, in := range []string{"", "https://orq.acme.internal", "http://localhost:3000"} {
		if IsHostedAPIBase(in) {
			t.Errorf("%q should not be hosted", in)
		}
	}
}

// One env ladder: ORQ_SERVER wins, the deprecated spelling still resolves and
// names itself so the caller can warn, and whitespace is not a value.
func TestServerFromEnv(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantURL    string
		wantEnvVar string
	}{
		{name: "nothing set"},
		{name: "ORQ_SERVER", env: map[string]string{"ORQ_SERVER": "https://env.example"}, wantURL: "https://env.example", wantEnvVar: "ORQ_SERVER"},
		{
			name:    "deprecated spelling names itself",
			env:     map[string]string{"ORQ_API_BASE_URL": "https://legacy.example"},
			wantURL: "https://legacy.example", wantEnvVar: DeprecatedServerEnvVar,
		},
		{
			name:    "ORQ_SERVER beats the deprecated spelling",
			env:     map[string]string{"ORQ_SERVER": "https://env.example", "ORQ_API_BASE_URL": "https://legacy.example"},
			wantURL: "https://env.example", wantEnvVar: "ORQ_SERVER",
		},
		{name: "whitespace is not a value", env: map[string]string{"ORQ_SERVER": "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, envVar := ServerFromEnv(func(k string) string { return tc.env[k] })
			if url != tc.wantURL || envVar != tc.wantEnvVar {
				t.Errorf("got (%q, %q), want (%q, %q)", url, envVar, tc.wantURL, tc.wantEnvVar)
			}
		})
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"aim.orq.ai":         "https://aim.orq.ai",
		"https://aim.orq.ai": "https://aim.orq.ai",
		"http://aim.orq.ai":  "http://aim.orq.ai",
		"aim.orq.ai/base":    "https://aim.orq.ai/base",
		"localhost:3000":     "http://localhost:3000",
		"127.0.0.1:4200":     "http://127.0.0.1:4200",
		"  aim.orq.ai  ":     "https://aim.orq.ai",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizeServer(in); got != want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}
