package auth

import (
	"net/url"
	"os"
	"strings"
)

const DefaultAPIBaseURL = "https://api.orq.ai"

type URLs struct {
	APIBaseURL     string `json:"api_base_url"`
	V1BaseURL      string `json:"v1_base_url"`
	AuthBaseURL    string `json:"auth_base_url"`
	ProfileBaseURL string `json:"profile_base_url"`
}

func trimTrailingSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func envDefaultAPIBase() string {
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
		profile = v1 + "/me"
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
