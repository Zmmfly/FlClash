//go:build webpanel && !android

package core

import (
	"encoding/json"
	"net/url"
	"strings"
)

func webAPIProfileIconURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func (tx *WebAPIReadTx) ProfileIconSource(id string) (string, error) {
	if !webAPISourceUint256Decimal(id, true) {
		return "", webAPIError(400, "invalid_source_resource", "invalid profile icon identity")
	}
	remaining := int64(128 << 20)
	rows := 0
	source := ""
	cursor := tx.tx.Bucket([]byte("profile-catalogs")).Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		rows++
		if rows > 100000 || int64(len(raw)) > remaining {
			return "", webAPIError(413, "icon_source_limit", "validated profile icon inventory exceeds the lookup budget")
		}
		remaining -= int64(len(raw))
		if tx.tx.Bucket([]byte("profiles")).Get(key) == nil {
			continue
		}
		var catalog struct {
			UnavailableReason string   `json:"unavailableReason"`
			IconURLs          []string `json:"iconUrls"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return "", webAPIError(409, "invalid_catalog_state", "profile icon catalog cannot be interpreted")
		}
		if catalog.UnavailableReason != "" {
			continue
		}
		for _, candidate := range catalog.IconURLs {
			if !webAPIProfileIconURL(candidate) {
				return "", webAPIError(409, "invalid_catalog_icon", "stored catalog icon is invalid")
			}
			if WebAPIUtilityIconSourceID(candidate) == id {
				source = candidate
				break
			}
		}
		if source != "" {
			return source, nil
		}
	}
	return "", webAPIError(404, "icon_image_unregistered", "icon URL is absent from validated profile catalogs")
}
