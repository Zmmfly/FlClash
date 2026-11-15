//go:build webpanel && !android

package core

import (
	"net/url"
	"strings"
)

func ValidateWebAPIUtilitySource(purpose WebAPISourcePurpose, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.ForceQuery || len(raw) > 8192 {
		return webAPIError(400, "invalid_utility_source", "utility source must use a fixed provider endpoint")
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || strings.ToLower(u.Hostname()) != u.Hostname() {
		return webAPIError(400, "invalid_utility_source", "utility source must use a fixed provider endpoint")
	}
	if u.Port() != "" && !(u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80") {
		return webAPIError(400, "invalid_utility_source", "utility source has an unexpected port")
	}
	u.Host = u.Hostname()
	normalized := u.String()
	if purpose == WebAPISourcePurpose("app-release") && normalized == webUtilityReleaseMetadataURL {
		return nil
	}
	if purpose == WebAPISourcePurpose("ip-quality-lookup") {
		if normalized == "https://ident.me/json" {
			return nil
		}
		var source, ip string
		switch u.Scheme + "://" + u.Host {
		case "http://ip-api.com":
			source, ip = "ipApiCom", strings.TrimPrefix(u.Path, "/json/")
		case "https://api.ipquery.io":
			source, ip = "ipQuery", strings.TrimPrefix(u.Path, "/")
		case "https://iplocate.io":
			source, ip = "ipLocate", strings.TrimPrefix(u.Path, "/api/lookup/")
		case "https://proxycheck.io":
			source, ip = "proxyCheck", strings.TrimPrefix(u.Path, "/v3/")
		case "https://api.ipapi.is":
			source, ip = "ipApiIs", u.Query().Get("q")
		}
		if expected, err := webUtilityIPURL(source, ip); err == nil && expected == normalized {
			return nil
		}
	}
	return webAPIError(400, "invalid_utility_source", "utility source is not an approved provider endpoint")
}
