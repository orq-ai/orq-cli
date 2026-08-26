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
