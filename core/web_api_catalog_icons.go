//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

func webAPIIconFileName(source string) string {
	parsed, err := url.Parse(source)
	if err != nil {
		return ""
	}
	parts := strings.Split(parsed.EscapedPath(), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			name, err := url.PathUnescape(parts[i])
			if err == nil {
				return name
			}
			return parts[i]
		}
	}
	return ""
}

func webAPIParseIconSet(data []byte) (webAPIIconSetDocument, error) {
	var result webAPIIconSetDocument
	fail := func() (webAPIIconSetDocument, error) {
		return result, webAPIError(400, "invalid_icon_set_document", "source is not an icon gallery with usable HTTP(S) icons")
	}
	if !utf8.Valid(data) {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := webAPIJSONTokens(decoder, 0); err != nil {
		return fail()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fail()
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return fail()
	}
	var icons []json.RawMessage
	if err := json.Unmarshal(document["icons"], &icons); err != nil {
		return fail()
	}
	_ = json.Unmarshal(document["name"], &result.Name)
	result.Name = strings.TrimSpace(result.Name)
	result.Icons = []WebAPIIconSetIcon{}
	seen := map[string]bool{}
	for _, raw := range icons {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var source string
		if json.Unmarshal(item["url"], &source) != nil || !webAPICatalogURL(source, false) || seen[source] {
			continue
		}
		seen[source] = true
		var name string
		_ = json.Unmarshal(item["name"], &name)
		name = strings.TrimSpace(name)
		if name == "" {
			name = webAPIIconFileName(source)
		}
		result.Icons = append(result.Icons, WebAPIIconSetIcon{Name: name, URL: source})
	}
	if len(result.Icons) == 0 {
		return fail()
	}
	return result, nil
}
