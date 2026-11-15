//go:build webpanel && !android

package core

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
)

const webDAVChallengeLimit = 8 << 10

type webDAVDigest struct {
	realm, nonce, opaque, algorithm, qop string
	count                                uint32
	stale                                bool
}

func webDAVHeaderText(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func webDAVToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func webDAVChallengeParts(header string) ([]string, bool) {
	if !webDAVHeaderText(header, webDAVChallengeLimit) {
		return nil, false
	}
	var parts []string
	start, quoted, escaped := 0, false, false
	for i, c := range header {
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == ',' && !quoted {
			parts = append(parts, strings.TrimSpace(header[start:i]))
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, false
	}
	parts = append(parts, strings.TrimSpace(header[start:]))
	return parts, len(parts) <= 64
}

func webDAVUnquote(raw string) (string, bool) {
	if !strings.HasPrefix(raw, `"`) {
		return raw, webDAVToken(raw)
	}
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", false
	}
	var out strings.Builder
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] == '"' {
			return "", false
		}
		if raw[i] == '\\' {
			i++
			if i >= len(raw)-1 {
				return "", false
			}
		}
		out.WriteByte(raw[i])
	}
	return out.String(), true
}

func webDAVParseDigest(values map[string]string) *webDAVDigest {
	if len(values) > 16 || values["nonce"] == "" || !webDAVHeaderText(values["nonce"], 1024) || !webDAVHeaderText(values["realm"], 1024) || !webDAVHeaderText(values["opaque"], 1024) {
		return nil
	}
	algorithm := strings.ToUpper(values["algorithm"])
	if algorithm == "" {
		algorithm = "MD5"
	}
	switch algorithm {
	case "MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS":
	default:
		return nil
	}
	qop := ""
	if raw, ok := values["qop"]; ok {
		for _, item := range strings.Split(raw, ",") {
			if strings.EqualFold(strings.TrimSpace(item), "auth") {
				qop = "auth"
			}
		}
		if qop == "" {
			return nil
		}
	}
	if value := values["charset"]; value != "" && !strings.EqualFold(value, "UTF-8") {
		return nil
	}
	if value := values["userhash"]; value != "" && !strings.EqualFold(value, "false") {
		return nil
	}
	return &webDAVDigest{realm: values["realm"], nonce: values["nonce"], opaque: values["opaque"], algorithm: algorithm, qop: qop, stale: strings.EqualFold(values["stale"], "true")}
}

func webDAVDigestChallenge(headers []string) *webDAVDigest {
	total := 0
	var candidates []*webDAVDigest
	for _, header := range headers {
		total += len(header)
		if total > webDAVChallengeLimit || len(headers) > 8 {
			return nil
		}
		parts, ok := webDAVChallengeParts(header)
		if !ok {
			return nil
		}
		active := false
		values := map[string]string{}
		finish := func() {
			if active {
				if d := webDAVParseDigest(values); d != nil {
					candidates = append(candidates, d)
				}
			}
			active = false
			values = map[string]string{}
		}
		for _, part := range parts {
			space := strings.IndexByte(part, ' ')
			equal := strings.IndexByte(part, '=')
			if space > 0 && (equal < 0 || space < equal) && webDAVToken(part[:space]) && !strings.HasPrefix(strings.TrimSpace(part[space:]), "=") {
				finish()
				active = strings.EqualFold(part[:space], "Digest")
				part = strings.TrimSpace(part[space+1:])
			}
			if !active {
				continue
			}
			key, raw, ok := strings.Cut(part, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			value, valid := webDAVUnquote(strings.TrimSpace(raw))
			_, duplicate := values[key]
			if !ok || !webDAVToken(key) || !valid || duplicate {
				active = false
				continue
			}
			values[key] = value
		}
		finish()
	}
	for _, d := range candidates {
		if strings.HasPrefix(d.algorithm, "SHA-256") {
			return d
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

func webDAVQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func (d *webDAVDigest) authorization(user, password, method string, u *url.URL) (string, error) {
	if d.count == ^uint32(0) {
		return "", webAPIError(502, "dav_auth_exhausted", "DAV authentication nonce count exhausted")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", webAPIError(500, "dav_auth_unavailable", "DAV authentication could not be generated")
	}
	cnonce := hex.EncodeToString(random[:])
	hash := func(value string) string {
		if strings.HasPrefix(d.algorithm, "SHA-256") {
			sum := sha256.Sum256([]byte(value))
			return hex.EncodeToString(sum[:])
		}
		sum := md5.Sum([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	d.count++
	nc := strconv.FormatUint(uint64(d.count), 16)
	nc = strings.Repeat("0", 8-len(nc)) + nc
	uri := u.RequestURI()
	ha1 := hash(user + ":" + d.realm + ":" + password)
	if strings.HasSuffix(d.algorithm, "-SESS") {
		ha1 = hash(ha1 + ":" + d.nonce + ":" + cnonce)
	}
	ha2 := hash(method + ":" + uri)
	response := hash(ha1 + ":" + d.nonce + ":" + ha2)
	if d.qop == "auth" {
		response = hash(ha1 + ":" + d.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	}
	parts := []string{"username=" + webDAVQuote(user), "realm=" + webDAVQuote(d.realm), "nonce=" + webDAVQuote(d.nonce), "uri=" + webDAVQuote(uri), "response=" + webDAVQuote(response), "algorithm=" + d.algorithm}
	if d.opaque != "" {
		parts = append(parts, "opaque="+webDAVQuote(d.opaque))
	}
	if d.qop == "auth" {
		parts = append(parts, "qop=auth", "nc="+nc)
	}
	if d.qop == "auth" || strings.HasSuffix(d.algorithm, "-SESS") {
		parts = append(parts, "cnonce="+webDAVQuote(cnonce))
	}
	return "Digest " + strings.Join(parts, ", "), nil
}
