//go:build webpanel && !android

package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type webBackupData struct {
	Profiles      []WebAPIProfile         `json:"profiles"`
	Scripts       []WebAPIScript          `json:"scripts"`
	Rules         []WebAPIRule            `json:"rules"`
	DisabledRules []WebAPIDisabledRule    `json:"disabledRules"`
	ProxyGroups   []WebAPIProxyGroup      `json:"proxyGroups"`
	CustomProxies []WebAPICustomProxy     `json:"customProxies"`
	ProxyDialers  []WebAPIProxyDialer     `json:"proxyDialers"`
	RuleProviders []WebAPIClashProvider   `json:"ruleProviders"`
	MitmModules   []WebAPIMitmModule      `json:"mitmModules"`
	IconSets      []WebAPIIconSet         `json:"iconSets"`
	Settings      *WebAPISettingsSnapshot `json:"settings,omitempty"`
	Contents      []webBackupContent      `json:"contents"`
}

type webBackupContent struct {
	Key       WebAPIContentKey `json:"key"`
	Entry     string           `json:"entry"`
	MediaType string           `json:"mediaType"`
	SHA256    string           `json:"sha256"`
	Size      int64            `json:"size"`
}

type webBackupCandidate struct {
	Data              webBackupData
	Contents          []WebAPIContent
	NativeConfig      json.RawMessage
	NativeSchema      int
	ConfigurationOnly bool
}

func webBackupValidateData(d webBackupData, blobs []WebAPIContent) error {
	count := len(d.Profiles) + len(d.Scripts) + len(d.Rules) + len(d.DisabledRules) + len(d.ProxyGroups) + len(d.CustomProxies) + len(d.ProxyDialers) + len(d.RuleProviders) + len(d.MitmModules) + len(d.IconSets)
	if count > 100000 {
		return webBackupInvalid("backup_row_limit")
	}
	profiles := map[WebAPIID]WebAPIProfile{}
	scripts := map[WebAPIID]bool{}
	rules := map[WebAPIID]WebAPIRule{}
	groups := map[WebAPIID]bool{}
	proxies := map[WebAPIID]bool{}
	providers := map[WebAPIID]bool{}
	modules := map[WebAPIID]WebAPIMitmModule{}
	icons := map[WebAPIID]bool{}
	add := func(id WebAPIID, exists bool) error {
		if _, err := webAPIInteger(string(id), true); err != nil || exists {
			return webBackupInvalid("backup_duplicate_or_invalid_id")
		}
		return nil
	}
	for _, v := range d.Profiles {
		_, exists := profiles[v.ID]
		if err := add(v.ID, exists); err != nil {
			return err
		}
		profiles[v.ID] = v
		if v.OverridesRawDocument != nil && v.Overrides != nil {
			return webBackupInvalid("backup_overrides_state")
		}
		if v.Type != "file" && v.Type != "url" && v.Type != "custom" {
			return webBackupInvalid("backup_invalid_profile")
		}
	}
	for _, v := range d.Scripts {
		if err := add(v.ID, scripts[v.ID]); err != nil {
			return err
		}
		scripts[v.ID] = true
	}
	for _, v := range d.Rules {
		_, exists := rules[v.ID]
		if err := add(v.ID, exists); err != nil {
			return err
		}
		rules[v.ID] = v
		if v.ProfileID != nil {
			if _, ok := profiles[*v.ProfileID]; !ok {
				return webBackupInvalid("backup_invalid_reference")
			}
		}
	}
	for _, v := range d.ProxyGroups {
		if err := add(v.ID, groups[v.ID]); err != nil {
			return err
		}
		groups[v.ID] = true
		if p, ok := profiles[v.ProfileID]; !ok || p.Type != "custom" {
			return webBackupInvalid("backup_invalid_reference")
		}
	}
	for _, v := range d.CustomProxies {
		if err := add(v.ID, proxies[v.ID]); err != nil {
			return err
		}
		proxies[v.ID] = true
	}
	for _, v := range d.RuleProviders {
		if err := add(v.ID, providers[v.ID]); err != nil {
			return err
		}
		providers[v.ID] = true
	}
	for _, v := range d.MitmModules {
		_, exists := modules[v.ID]
		if err := add(v.ID, exists); err != nil {
			return err
		}
		modules[v.ID] = v
	}
	for _, v := range d.IconSets {
		if err := add(v.ID, icons[v.ID]); err != nil {
			return err
		}
		icons[v.ID] = true
	}
	for _, p := range d.Profiles {
		if p.ScriptID != nil && !scripts[*p.ScriptID] {
			return webBackupInvalid("backup_invalid_reference")
		}
	}
	pairs := map[string]bool{}
	for _, v := range d.DisabledRules {
		key := string(v.ProfileID) + ":" + string(v.RuleID)
		_, exists := profiles[v.ProfileID]
		r, found := rules[v.RuleID]
		if !exists || !found || r.ProfileID != nil || pairs[key] {
			return webBackupInvalid("backup_invalid_reference")
		}
		pairs[key] = true
	}
	pairs = map[string]bool{}
	for _, v := range d.ProxyDialers {
		key := string(v.ProfileID) + ":" + string(v.ProxyID)
		p, exists := profiles[v.ProfileID]
		if !exists || p.Type != "custom" || !proxies[v.ProxyID] || pairs[key] {
			return webBackupInvalid("backup_invalid_reference")
		}
		pairs[key] = true
	}
	keys := map[string]bool{}
	assets := map[WebAPIID][]WebAPIContent{}
	var total int64
	for _, blob := range blobs {
		key, _, err := webAPIContentKey(blob.Key)
		if err != nil || keys[key] {
			return webBackupInvalid("backup_invalid_content")
		}
		keys[key] = true
		owner := false
		switch blob.Key.Kind {
		case WebAPIContentProfile:
			p, ok := profiles[blob.Key.ID]
			owner = ok && p.Type != "custom"
		case WebAPIContentProfileAsset:
			_, owner = profiles[blob.Key.ID]
			assets[blob.Key.ID] = append(assets[blob.Key.ID], blob)
		case WebAPIContentScript:
			owner = scripts[blob.Key.ID]
		case WebAPIContentRuleProvider:
			owner = providers[blob.Key.ID]
		case WebAPIContentMitmModule:
			_, owner = modules[blob.Key.ID]
		case WebAPIContentMitmResource:
			m, ok := modules[blob.Key.ID]
			owner = ok && webContentModuleHasKey(m, blob.Key.Name)
			if len(blob.Bytes) > 16<<20 {
				return webBackupInvalid("backup_expanded_limit")
			}
		}
		total += int64(len(blob.Bytes))
		if !owner || blob.SHA256 != webContentDigest(blob.Bytes) || len(blob.Bytes) > WebAPIMaxContentBytes || total > WebAPIBackupExpandedLimit {
			return webBackupInvalid("backup_invalid_content")
		}
	}
	for _, contents := range assets {
		if _, err := webContentPlanetAssets(contents); err != nil {
			return err
		}
	}
	return nil
}

func webBackupTypedCandidate(archive webBackupArchive) (webBackupCandidate, error) {
	var candidate webBackupCandidate
	if err := webContentStrictJSON(archive.Entries["data.json"], &candidate.Data); err != nil {
		return candidate, webBackupInvalid("backup_invalid_data")
	}
	expected := map[string]bool{"manifest.json": true, "data.json": true}
	for _, ref := range candidate.Data.Contents {
		data, exists := archive.Entries[ref.Entry]
		if !exists || expected[ref.Entry] || ref.Size != int64(len(data)) || ref.SHA256 != webContentDigest(data) || ref.Entry != webBackupBlobEntry(ref.Key) {
			return candidate, webBackupInvalid("backup_content_mismatch")
		}
		expected[ref.Entry] = true
		candidate.Contents = append(candidate.Contents, WebAPIContent{Key: ref.Key, MediaType: ref.MediaType, SHA256: ref.SHA256, Bytes: data})
	}
	if err := webBackupNoExtraEntries(archive, expected); err != nil {
		return candidate, err
	}
	return candidate, webBackupValidateData(candidate.Data, candidate.Contents)
}

func webBackupBlobEntry(key WebAPIContentKey) string {
	name := string(key.Kind) + "/" + string(key.ID)
	if key.Kind == WebAPIContentMitmResource || key.Kind == WebAPIContentProfileAsset {
		name += "/" + webContentDigest([]byte(key.Name))
	}
	return "blobs/" + name
}

func webBackupEncodeTyped(candidate webBackupCandidate) ([]byte, error) {
	if err := webBackupValidateData(candidate.Data, candidate.Contents); err != nil {
		return nil, err
	}
	entries := map[string][]byte{}
	candidate.Data.Contents = []webBackupContent{}
	for _, content := range candidate.Contents {
		entry := webBackupBlobEntry(content.Key)
		entries[entry] = content.Bytes
		candidate.Data.Contents = append(candidate.Data.Contents, webBackupContent{Key: content.Key, Entry: entry, MediaType: content.MediaType, SHA256: webContentDigest(content.Bytes), Size: int64(len(content.Bytes))})
	}
	data, err := webContentMarshal(candidate.Data)
	if err != nil {
		return nil, err
	}
	entries["data.json"] = data
	return webWriteBackupArchive("flclash-v1", entries)
}

func webBackupDecimal(v any) (string, error) {
	n, ok := v.(int64)
	if !ok || n <= 0 {
		return "", webBackupInvalid("backup_invalid_id")
	}
	return strconv.FormatInt(n, 10), nil
}

func webBackupTimestamp(v any) (string, error) {
	if n, ok := v.(int64); ok {
		return webNativeTime(n), nil
	}
	return "", webBackupInvalid("backup_invalid_timestamp")
}

func webBackupLegacyTransform(detail string) error {
	return &WebAPIError{Status: 400, Code: "backup_legacy_transform_required", Message: "legacy backup needs an explicit non-lossless compatibility decision", Details: map[string]any{"reason": detail}}
}

func webBackupJSON(value any) (json.RawMessage, error) { b, e := json.Marshal(value); return b, e }
func webBackupString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func webBackupName(id WebAPIID, extension string) string { return fmt.Sprintf("%s.%s", id, extension) }
func webBackupRemote(v *string) bool                     { return v != nil && strings.TrimSpace(*v) != "" }
