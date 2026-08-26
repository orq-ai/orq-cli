package auth

import (
	"net/url"
	"os"
	"strings"
)

const DefaultAPIBaseURL = "https://api.orq.ai"

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
// "session"). Called once per invocation, before any command runs.
func SetServer(url, source string) {
	server = strings.TrimSpace(url)
	serverSource = source
}

// Server is the resolved host, or "" when nothing overrode the default.
func Server() string { return server }

// ServerSource names where Server() came from, for `orq doctor`.
func ServerSource() string { return serverSource }

func envDefaultAPIBase() string {
	if server != "" {
		return server
	}
	// Direct env reads remain for callers that run without the PreRun — tests,
	// and `orq launch` resolving credentials on its own.
	if v := strings.TrimSpace(os.Getenv("ORQ_SERVER")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("ORQ_API_BASE_URL")); v != "" {
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
