//go:build webpanel && !android

package core

import (
	"sort"
	"strconv"
)

type WebAPIBackupChange struct {
	Kind           string                         `json:"kind"`
	ID             string                         `json:"id,omitempty"`
	Name           string                         `json:"name,omitempty"`
	Count          int                            `json:"count"`
	Action         string                         `json:"action"`
	SettingsChange *WebAPISettingsMigrationChange `json:"settingsChange,omitempty"`
}

func webNativeCompatibilityTransform(tables map[string][]webNativeRow, changes *[]WebAPIBackupChange) error {
	profiles := map[int64]webNativeRow{}
	custom := map[int64]bool{}
	for _, p := range tables["profiles"] {
		id, ok := p["id"].(int64)
		if !ok || id <= 0 {
			return webBackupInvalid("backup_invalid_id")
		}
		profiles[id] = p
		switch p["overwrite_type"] {
		case "standard", "script":
		case "custom":
			custom[id] = true
			*changes = append(*changes, WebAPIBackupChange{Kind: "profile", ID: strconv.FormatInt(id, 10), Name: webBackupString(p["label"]), Count: 1, Action: "replace-custom-overwrite-with-standard"})
			p["overwrite_type"] = "standard"
			p["match_target"] = nil
		default:
			return webBackupLegacyTransform("unknown_overwrite_type")
		}
	}
	for _, table := range []string{"proxy_groups", "proxy_dialers"} {
		for _, row := range tables[table] {
			id, ok := row["profile_id"].(int64)
			if !ok || profiles[id] == nil {
				return webBackupInvalid("backup_invalid_reference")
			}
			*changes = append(*changes, WebAPIBackupChange{Kind: table, ID: strconv.FormatInt(id, 10), Name: webBackupString(row["name"]), Count: 1, Action: "remove-legacy-custom-overwrite-record"})
		}
		tables[table] = nil
	}
	keptProxies := []webNativeRow{}
	for _, p := range tables["custom_proxies"] {
		if owner := p["profile_id"]; owner != nil {
			id, ok := owner.(int64)
			if !ok || profiles[id] == nil {
				return webBackupInvalid("backup_invalid_reference")
			}
			proxy, err := webBackupDecimal(p["id"])
			if err != nil {
				return err
			}
			*changes = append(*changes, WebAPIBackupChange{Kind: "custom-proxy", ID: proxy, Name: webBackupString(profiles[id]["label"]), Count: 1, Action: "remove-profile-owned-proxy"})
			continue
		}
		delete(p, "profile_id")
		keptProxies = append(keptProxies, p)
	}
	tables["custom_proxies"] = keptProxies
	rules := map[int64]webNativeRow{}
	for _, r := range tables["rules"] {
		id, ok := r["id"].(int64)
		if !ok || id <= 0 {
			return webBackupInvalid("backup_invalid_id")
		}
		rules[id] = r
	}
	links := tables["profile_rule_mapping"]
	sort.SliceStable(links, func(i, j int) bool {
		a, b := links[i], links[j]
		if (a["profile_id"] == nil) != (b["profile_id"] == nil) {
			return a["profile_id"] == nil
		}
		return webBackupString(a["order"]) < webBackupString(b["order"])
	})
	owners := map[int64]webNativeRow{}
	disabled := map[string]webNativeRow{}
	for _, link := range links {
		ruleID, ok := link["rule_id"].(int64)
		if !ok || rules[ruleID] == nil {
			return webBackupInvalid("backup_invalid_reference")
		}
		profileID := int64(0)
		if link["profile_id"] != nil {
			profileID, ok = link["profile_id"].(int64)
			if !ok || profiles[profileID] == nil {
				return webBackupInvalid("backup_invalid_reference")
			}
		}
		scene := link["scene"]
		switch {
		case profileID == 0 && scene == nil, profileID != 0 && scene == "added" && !custom[profileID]:
			if _, exists := owners[ruleID]; exists {
				*changes = append(*changes, WebAPIBackupChange{Kind: "rule-link", ID: strconv.FormatInt(ruleID, 10), Name: webBackupString(link["id"]), Count: 1, Action: "retain-first-owner-remove-additional-link"})
			} else {
				owners[ruleID] = link
			}
		case profileID != 0 && scene == "disabled":
			disabled[strconv.FormatInt(profileID, 10)+":"+strconv.FormatInt(ruleID, 10)] = webNativeRow{"profile_id": profileID, "rule_id": ruleID}
		case profileID != 0 && custom[profileID] && (scene == "added" || scene == "custom"):
			*changes = append(*changes, WebAPIBackupChange{Kind: "rule-link", ID: strconv.FormatInt(ruleID, 10), Name: webBackupString(link["id"]), Count: 1, Action: "remove-custom-overwrite-rule-link"})
		default:
			return webBackupLegacyTransform("unknown_legacy_rule_scene")
		}
	}
	keptRules := []webNativeRow{}
	for _, r := range tables["rules"] {
		id := r["id"].(int64)
		link, ok := owners[id]
		if !ok {
			*changes = append(*changes, WebAPIBackupChange{Kind: "rule", ID: strconv.FormatInt(id, 10), Count: 1, Action: "remove-rule-without-retained-owner"})
			continue
		}
		r["profile_id"] = link["profile_id"]
		r["order"] = link["order"]
		keptRules = append(keptRules, r)
		if link["profile_id"] == nil {
			for owner := range custom {
				key := strconv.FormatInt(owner, 10) + ":" + strconv.FormatInt(id, 10)
				disabled[key] = webNativeRow{"profile_id": owner, "rule_id": id}
				*changes = append(*changes, WebAPIBackupChange{Kind: "disabled-rule", ID: key, Count: 1, Action: "disable-global-rule-for-converted-profile"})
			}
		}
	}
	tables["rules"] = keptRules
	tables["disabled_rules"] = nil
	for _, row := range disabled {
		owner := row["rule_id"].(int64)
		if link := owners[owner]; link != nil && link["profile_id"] == nil {
			tables["disabled_rules"] = append(tables["disabled_rules"], row)
		} else {
			*changes = append(*changes, WebAPIBackupChange{Kind: "disabled-rule", ID: strconv.FormatInt(owner, 10), Count: 1, Action: "remove-nonglobal-disabled-link"})
		}
	}
	return nil
}
