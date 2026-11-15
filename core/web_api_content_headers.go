//go:build webpanel && !android

package core

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func WebAPIValidateCommittedSourceHeaders(input map[string][]string) (map[string][]string, error) {
	if len(input) > 32 {
		return nil, webAPIError(400, "invalid_source_headers", "too many source header declarations")
	}
	output := map[string][]string{}
	seen := map[string]bool{}
	total := 0
	for name, values := range input {
		lower := strings.ToLower(name)
		if seen[lower] || len(values) != 1 {
			return nil, webAPIError(400, "invalid_source_headers", "duplicate source header declarations are not accepted")
		}
		seen[lower] = true
		if !webSourceHeaderName(name) {
			return nil, webAPIError(400, "invalid_source_headers", "invalid source header name")
		}
		limit := 8192
		switch lower {
		case "host", "origin", "referer", "proxy-authorization", "proxy-authenticate", "connection", "proxy-connection", "keep-alive", "content-length", "transfer-encoding", "upgrade", "te", "trailer", "expect":
			return nil, webAPIError(400, "invalid_source_headers", "transport header is not permitted in a source declaration")
		case "user-agent", "accept":
			limit = 1024
		}
		value := values[0]
		total += len(name) + len(value)
		if strings.TrimSpace(name) != name || len(value) > limit || total > 16<<10 {
			return nil, webAPIError(400, "invalid_source_headers", "invalid source header value")
		}
		for _, b := range []byte(value) {
			if b < 0x20 && b != '\t' || b == 0x7f {
				return nil, webAPIError(400, "invalid_source_headers", "invalid source header value")
			}
		}
		if lower == "authorization" {
			scheme, token, ok := strings.Cut(value, " ")
			if !ok || !strings.EqualFold(scheme, "Basic") && !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
				return nil, webAPIError(400, "invalid_source_headers", "source authorization must use Basic or Bearer")
			}
		}
		output[http.CanonicalHeaderKey(name)] = []string{value}
	}
	return output, nil
}

func webSourceHeaderName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, b := range []byte(name) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			continue
		}
		return false
	}
	return true
}

func webSourceIdentity(raw string, headers map[string][]string) (*url.URL, string, map[string][]string, error) {
	u, digest, err := webSourceCanonical(raw)
	if err != nil {
		return nil, "", nil, err
	}
	normalized, err := WebAPIValidateCommittedSourceHeaders(headers)
	if err != nil {
		return nil, "", nil, err
	}
	if u.User != nil && len(normalized["Authorization"]) != 0 {
		return nil, "", nil, webAPIError(400, "invalid_source_headers", "source credentials have conflicting declarations")
	}
	if len(normalized) > 0 {
		body, err := json.Marshal(struct {
			URL     string              `json:"url"`
			Headers map[string][]string `json:"headers"`
		}{u.String(), normalized})
		if err != nil {
			return nil, "", nil, err
		}
		digest = webContentDigest(body)
	}
	return u, digest, normalized, nil
}

func webSourceIdentityDigest(raw string, headers map[string][]string) (string, error) {
	_, digest, _, err := webSourceIdentity(raw, headers)
	return digest, err
}
