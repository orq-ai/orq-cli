package auth

import (
	"net/url"
	"os"
	"strings"
)

// DefaultAPIBaseURL is the host every URL hangs off when nothing overrides it.
// It is the `servers[0]` entry of openapi.yaml, which is also what the
// generated commands fall back to — one binary, one default. api.orq.ai
// answers the same routes from the same origin and stays valid; it simply is
// not the spec's own value, and two defaults meant one unflagged run could
// straddle both.
const DefaultAPIBaseURL = "https://my.orq.ai"

// LegacyDefaultAPIBaseURL is what the CLI defaulted to before 4.15 and what
// every session written by an older build still stores. Same origin, same
// routes, different name — so it is not an override, and code deciding "is
// this the hosted service or a self-hosted deployment?" must accept both.
const LegacyDefaultAPIBaseURL = "https://api.orq.ai"

// IsHostedAPIBase reports whether apiBase names orq's own SaaS under either of
// its two interchangeable hostnames.
func IsHostedAPIBase(apiBase string) bool {
	switch trimTrailingSlash(strings.TrimSpace(apiBase)) {
	case DefaultAPIBaseURL, LegacyDefaultAPIBaseURL:
		return true
	}
	return false
}

// ProfileRPCPath is the Connect RPC that replaced the REST profile endpoint.
// The old GET /v2/api/me was deleted when profiles moved to identity-api, so
// the profile fetch is a POST to this path instead.
const ProfileRPCPath = "/v3/rpc/identity/orq.identity.v1.ProfileService/GetProfile"

type URLs struct {
	APIBaseURL     string `json:"api_base_url"`
	V1BaseURL      string `json:"v1_base_url"`
	AuthBaseURL    string `json:"auth_base_url"`
	ProfileBaseURL string `json:"profile_base_url"`
}

func trimTrailingSlash(s string) string {
	return strings.TrimRight(s, "/")
}

// The active server and where it came from, decided once by the root PreRun
// (see custom.resolveServer) and read by everything downstream. Provenance is
// recorded at the moment of the decision rather than re-derived later: the
// resolved URL alone cannot say whether it came from --server, an env var or
// the session, because those values are usually identical.
var (
	server       string
	serverSource = "default"
)

// SetServer records the resolved host and its origin ("flag", "env", "config",
// "session"). Called by the root PreRun before any command runs: once for the
// explicit sources, again for the session host when none of them applied.
func SetServer(url, source string) {
	server = strings.TrimSpace(url)
	serverSource = source
}

// Server is the resolved host, or "" when nothing overrode the default.
func Server() string { return server }

// ServerSource names where Server() came from, for `orq doctor`.
func ServerSource() string { return serverSource }

// ServerFromEnv reports the host the environment names and which spelling
// named it, or "" for both when neither is set. It is the one env ladder in
// the binary: the root PreRun calls it to resolve --server's env layer (and
// warns on the deprecated spelling), and the fallbacks below call it so a run
// that misses the PreRun cannot resolve a different host than one that does
// not. getenv is injected so callers can drive it in tests.
func ServerFromEnv(getenv func(string) string) (url, envVar string) {
	for _, name := range []string{"ORQ_SERVER", "ORQ_API_BASE_URL"} {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v, name
		}
	}
	return "", ""
}

// DeprecatedServerEnvVar is the pre-4.15 spelling of ORQ_SERVER, honored for
// one release. Callers that can print compare ServerFromEnv's second return
// against it and warn.
const DeprecatedServerEnvVar = "ORQ_API_BASE_URL"

func envDefaultAPIBase() string {
	if server != "" {
		return server
	}
	// Reached only by callers that run without the PreRun, which in practice
	// means tests: no warning is printed here, and the persisted
	// `orq server set` layer lives with viper in the PreRun.
	if v, _ := ServerFromEnv(os.Getenv); v != "" {
		return v
	}
	return DefaultAPIBaseURL
}

func deriveV1BaseURL(apiBase string) string {
	if v := strings.TrimSpace(os.Getenv("ORQ_V1_BASE_URL")); v != "" {
		return trimTrailingSlash(v)
	}
	if u, err := url.Parse(apiBase); err == nil {
		host := u.Hostname()
		port := u.Port()
		if (host == "localhost" || host == "127.0.0.1" || host == "::1") &&
			(port == "4200" || port == "3500" || port == "3000") {
			u.Host = "127.0.0.1:3000"
			u.Path = ""
			u.RawQuery = ""
			u.Fragment = ""
			return trimTrailingSlash(u.String())
		}
	}
	return trimTrailingSlash(apiBase) + "/v2/api"
}

func ResolveURLs(apiBase string) URLs {
	if apiBase == "" {
		apiBase = envDefaultAPIBase()
	}
	apiBase = trimTrailingSlash(apiBase)
	v1 := deriveV1BaseURL(apiBase)
	profile := strings.TrimSpace(os.Getenv("ORQ_PROFILE_BASE_URL"))
	if profile == "" {
		profile = apiBase + ProfileRPCPath
	} else {
		profile = trimTrailingSlash(profile)
	}
	return URLs{
		APIBaseURL:     apiBase,
		V1BaseURL:      v1,
		AuthBaseURL:    apiBase + "/v2/auth",
		ProfileBaseURL: profile,
	}
}
