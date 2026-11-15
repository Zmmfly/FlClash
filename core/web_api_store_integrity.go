//go:build webpanel && !android

package core

import (
	"encoding/json"
	"strings"
)

func (tx *WebAPIReadTx) validateIntegrity() error {
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	owners := map[WebAPIID]WebAPIProfile{}
	labels := map[string]bool{}
	for _, p := range profiles {
		if _, err = webAPIInteger(string(p.ID), true); err != nil {
			return webAPIError(409, "invalid_id", "stored profile has an invalid identity")
		}
		if p.Type != "file" && p.Type != "url" && p.Type != "custom" {
			return webAPIError(409, "invalid_profile_type", "stored profile has an invalid type")
		}
		if p.Label != "" && labels[p.Label] {
			return webAPIError(409, "duplicate_label", "profile labels must be unique")
		}
		labels[p.Label] = true
		if p.OverridesRawDocument != nil && p.Overrides != nil {
			return webAPIError(409, "invalid_overrides", "raw overrides and decoded overrides are mutually exclusive")
		}
		owners[p.ID] = p
	}
	scripts, err := tx.Scripts()
	if err != nil {
		return err
	}
	scriptIDs := map[WebAPIID]bool{}
	for _, v := range scripts {
		scriptIDs[v.ID] = true
	}
	for _, p := range profiles {
		if p.ScriptID != nil && !scriptIDs[*p.ScriptID] {
			return webAPIError(409, "invalid_reference", "profile script does not exist")
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return err
	}
	ruleIDs := map[WebAPIID]WebAPIRule{}
	ruleKeys := map[string]bool{}
	for _, v := range rules {
		if _, err = webAPIInteger(string(v.ID), true); err != nil {
			return webAPIError(409, "invalid_id", "stored rule has an invalid identity")
		}
		if v.ProfileID != nil {
			if _, ok := owners[*v.ProfileID]; !ok {
				return webAPIError(409, "invalid_reference", "rule owner does not exist")
			}
		}
		if !webAPIContains(webAPIRuleActions, v.RuleAction) {
			return webAPIError(409, "invalid_rule_action", "stored rule action is unknown")
		}
		key, err := json.Marshal(v.WebAPIRuleInput)
		if err != nil {
			return err
		}
		if ruleKeys[string(key)] {
			return webAPIError(409, "duplicate_rule", "identical rules for the same owner are not allowed")
		}
		ruleKeys[string(key)] = true
		ruleIDs[v.ID] = v
	}
	disabled, err := tx.DisabledRules()
	if err != nil {
		return err
	}
	for _, v := range disabled {
		if _, ok := owners[v.ProfileID]; !ok {
			return webAPIError(409, "invalid_reference", "disabled-rule owner does not exist")
		}
		rule, ok := ruleIDs[v.RuleID]
		if !ok {
			return webAPIError(409, "invalid_reference", "disabled rule does not exist")
		}
		if rule.ProfileID != nil {
			return webAPIError(409, "owner_conflict", "only global rules can be disabled")
		}
	}
	proxies, err := tx.CustomProxies()
	if err != nil {
		return err
	}
	proxyIDs := map[WebAPIID]bool{}
	for _, v := range proxies {
		proxyIDs[v.ID] = true
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return err
	}
	groupNames := map[WebAPIID]map[string]bool{}
	for _, v := range groups {
		owner, ok := owners[v.ProfileID]
		if !ok || owner.Type != "custom" {
			return webAPIError(409, "owner_conflict", "proxy groups require a custom profile owner")
		}
		if groupNames[v.ProfileID] == nil {
			groupNames[v.ProfileID] = map[string]bool{}
		}
		if groupNames[v.ProfileID][v.Definition.Name] {
			return webAPIError(409, "duplicate_name", "proxy group names must be unique within a profile")
		}
		groupNames[v.ProfileID][v.Definition.Name] = true
	}
	dialers, err := tx.ProxyDialers()
	if err != nil {
		return err
	}
	for _, v := range dialers {
		owner, ok := owners[v.ProfileID]
		if !ok || owner.Type != "custom" || !proxyIDs[v.ProxyID] {
			return webAPIError(409, "invalid_reference", "proxy dialer has an invalid profile or proxy owner")
		}
	}
	settings, err := tx.Settings()
	if err != nil {
		return err
	}
	if settings.CurrentProfileID != nil {
		if _, ok := owners[*settings.CurrentProfileID]; !ok {
			return webAPIError(409, "invalid_reference", "selected profile does not exist")
		}
	}
	return tx.tx.Bucket([]byte("content")).ForEach(func(key, _ []byte) error {
		parts := strings.SplitN(string(key), ":", 3)
		if len(parts) != 3 {
			return webAPIError(409, "invalid_content_key", "invalid stored content identity")
		}
		_, bucket, err := webAPIContentKey(WebAPIContentKey{Kind: WebAPIContentKind(parts[0]), ID: WebAPIID(parts[1]), Name: parts[2]})
		if err != nil {
			return err
		}
		if tx.tx.Bucket([]byte(bucket)).Get([]byte(parts[1])) == nil {
			return webAPIError(409, "invalid_reference", "content owner does not exist")
		}
		return nil
	})
}
