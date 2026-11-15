//go:build webpanel && !android

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const webUtilityReleaseRepository = "chen08209/FlClash"
const webUtilityReleaseMetadataURL = "https://api.github.com/repos/chen08209/FlClash/releases/latest"
const webUtilityReleaseMetadataLimit = 1 << 20
const webUtilityReleaseBodyLimit = 256 << 10

var webUtilityReleaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
var webUtilityReleaseAssetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)

type WebAPIUtilityReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type WebAPIUtilityRelease struct {
	Provider     string                      `json:"provider"`
	Repository   string                      `json:"repository"`
	TagName      string                      `json:"tagName"`
	Version      string                      `json:"version"`
	Name         string                      `json:"name"`
	Body         string                      `json:"body"`
	PublishedAt  string                      `json:"publishedAt"`
	ReleaseURL   string                      `json:"releaseUrl"`
	Assets       []WebAPIUtilityReleaseAsset `json:"assets"`
	CheckedAt    string                      `json:"checkedAt"`
	UpdatePolicy string                      `json:"updatePolicy"`
}

func webUtilityJSON(data []byte, target any, limit int) error {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return webAPIError(502, "invalid_source_body", "source returned an invalid or oversized JSON document")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := webAPIJSONTokens(decoder, 0); err != nil {
		return webAPIError(502, "invalid_source_body", "source returned malformed or duplicate JSON members")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return webAPIError(502, "invalid_source_body", "source returned more than one JSON document")
	}
	if bytes.TrimSpace(data)[0] != '{' || json.Unmarshal(data, target) != nil {
		return webAPIError(502, "invalid_source_body", "source returned an unexpected JSON field type")
	}
	return nil
}

func webUtilityParseRelease(data []byte, now time.Time) (WebAPIUtilityRelease, error) {
	var result WebAPIUtilityRelease
	var incoming struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		Assets      []struct {
			Name  string `json:"name"`
			Size  int64  `json:"size"`
			State string `json:"state"`
		} `json:"assets"`
	}
	if err := webUtilityJSON(data, &incoming, webUtilityReleaseMetadataLimit); err != nil {
		return result, err
	}
	published, err := time.Parse(time.RFC3339, incoming.PublishedAt)
	if err != nil || !webUtilityReleaseTag.MatchString(incoming.TagName) || incoming.Draft || incoming.Prerelease || len(incoming.Assets) > 128 || len(incoming.Body) > webUtilityReleaseBodyLimit || len(incoming.Name) > 1024 || strings.ContainsRune(incoming.Body+incoming.Name, '\x00') {
		return result, webAPIError(502, "invalid_release", "provider returned invalid stable release metadata")
	}
	base := "https://github.com/" + webUtilityReleaseRepository + "/releases/"
	result = WebAPIUtilityRelease{
		Provider: "github", Repository: webUtilityReleaseRepository,
		TagName: incoming.TagName, Version: strings.TrimPrefix(incoming.TagName, "v"),
		Name: incoming.Name, Body: incoming.Body,
		PublishedAt: published.UTC().Format(time.RFC3339),
		ReleaseURL:  base + "tag/" + incoming.TagName,
		Assets:      []WebAPIUtilityReleaseAsset{}, CheckedAt: now.UTC().Format(time.RFC3339Nano),
		UpdatePolicy: "server-image",
	}
	if result.Name == "" {
		result.Name = result.TagName
	}
	seen := map[string]bool{}
	for _, asset := range incoming.Assets {
		if !webUtilityReleaseAssetName.MatchString(asset.Name) || strings.Contains(asset.Name, "..") || asset.Size < 0 || asset.Size > 1<<40 || seen[asset.Name] || asset.State != "" && asset.State != "uploaded" {
			return WebAPIUtilityRelease{}, webAPIError(502, "invalid_release", "provider returned an invalid release asset")
		}
		seen[asset.Name] = true
		result.Assets = append(result.Assets, WebAPIUtilityReleaseAsset{Name: asset.Name, Size: asset.Size, URL: base + "download/" + incoming.TagName + "/" + url.PathEscape(asset.Name)})
	}
	return result, nil
}
