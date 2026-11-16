//go:build webpanel && !android

package core

import (
	"encoding/json"
	"reflect"
	"strings"
)

type webAPICatalogReference struct {
	Resource  string   `json:"resource"`
	ID        WebAPIID `json:"id,omitempty"`
	ProfileID WebAPIID `json:"profileId,omitempty"`
	Path      string   `json:"path"`
	Name      string   `json:"name"`
}

func webAPICatalogReferenced(references []webAPICatalogReference) error {
	if len(references) == 0 {
		return nil
	}
	err := webAPIError(409, "resource_in_use", "surviving resources reference the removed resource")
	err.Details = map[string]any{"references": references}
	return err
}

func RenameWebAPICatalogReferences(tx *WebAPIWriteTx, kind string, ownerID WebAPIID, oldName, newName string) error {
	if kind == "proxy-provider" && oldName == "" && ownerID != "" {
		profile, err := tx.Profile(ownerID)
		if err != nil {
			return err
		}
		oldName = webAPIEffectiveProfileLabel(profile)
	}
	if !webAPICatalogName(oldName) || !webAPICatalogName(newName) {
		return webAPIError(400, "invalid_name", "invalid catalog rename")
	}
	return webAPIRenameCatalogReferences(tx, kind, ownerID, map[string]string{oldName: newName})
}

func webAPIRenameNames(values []string, names map[string]string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value
		if next, exists := names[value]; exists {
			result[i] = next
		}
	}
	return result
}

func webAPIRenamePointer(value *string, names map[string]string) *string {
	if value != nil {
		if next, exists := names[*value]; exists {
			return &next
		}
	}
	return value
}

func webAPIRenameMap[V any](values map[string]V, names map[string]string) (map[string]V, error) {
	if values == nil {
		return nil, nil
	}
	result := make(map[string]V, len(values))
	for key, value := range values {
		if next, exists := names[key]; exists {
			key = next
		}
		if _, exists := result[key]; exists {
			return nil, webAPIError(409, "rename_conflict", "renamed reference keys would collide")
		}
		result[key] = value
	}
	return result, nil
}

func webAPIRenameCatalogReferences(tx *WebAPIWriteTx, kind string, ownerID WebAPIID, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	if !webAPIContains([]string{"group", "custom-proxy", "rule-provider", "proxy-provider"}, kind) {
		return webAPIError(400, "invalid_reference_kind", "unknown catalog reference namespace")
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return err
	}
	members := map[WebAPIID]map[string][]string{}
	for _, group := range groups {
		if members[group.ProfileID] == nil {
			members[group.ProfileID] = map[string][]string{}
		}
		name := group.Definition.Name
		if kind == "group" && group.ProfileID == ownerID {
			for old, next := range names {
				if next == name {
					name = old
					break
				}
			}
		}
		members[group.ProfileID][name] = append([]string{}, group.Definition.Proxies...)
	}
	for _, group := range groups {
		if kind == "group" && group.ProfileID != ownerID {
			continue
		}
		before := webAPIClone(group)
		switch kind {
		case "group", "custom-proxy":
			group.Definition.Proxies = webAPIRenameNames(group.Definition.Proxies, names)
			group.Definition.DefaultSelected = webAPIRenamePointer(group.Definition.DefaultSelected, names)
			group.Definition.EmptyFallback = webAPIRenamePointer(group.Definition.EmptyFallback, names)
		case "proxy-provider":
			group.Definition.Use = webAPIRenameNames(group.Definition.Use, names)
			group.Definition.UsePosition, err = webAPIRenameMap(group.Definition.UsePosition, names)
			if err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(before, group) {
			if err = tx.SaveProxyGroup(group); err != nil {
				return err
			}
		}
	}
	if kind == "custom-proxy" {
		proxies, err := tx.CustomProxies()
		if err != nil {
			return err
		}
		for _, proxy := range proxies {
			if next, exists := names[webAPIProxyString(proxy.Definition, "dialer-proxy")]; exists {
				proxy.Definition["dialer-proxy"], _ = json.Marshal(next)
				if err = tx.SaveCustomProxy(proxy); err != nil {
					return err
				}
			}
		}
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	custom := map[WebAPIID]bool{}
	for _, profile := range profiles {
		if profile.Type == "custom" {
			custom[profile.ID] = true
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.ProfileID == nil || !custom[*rule.ProfileID] || kind == "group" && *rule.ProfileID != ownerID {
			continue
		}
		before := webAPIClone(rule)
		if kind == "group" || kind == "custom-proxy" {
			rule.RuleTarget = webAPIRenamePointer(rule.RuleTarget, names)
			if rule.RuleTarget != nil && !webAPIName(*rule.RuleTarget) {
				return webAPIError(409, "unrepresentable_reference", "renamed rule target cannot round-trip through rule text")
			}
		}
		if kind == "rule-provider" {
			rule.RuleProvider = webAPIRenamePointer(rule.RuleProvider, names)
			if rule.RuleProvider != nil && !webAPIName(*rule.RuleProvider) {
				return webAPIError(409, "unrepresentable_reference", "renamed rule provider cannot round-trip through rule text")
			}
			if rule.Content != nil && webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
				text, e := webAPIRenameRuleSetText(*rule.Content, names)
				if e != nil {
					return webAPIError(409, "unrepresentable_reference", e.Error())
				}
				rule.Content = &text
			}
		}
		if !reflect.DeepEqual(before, rule) {
			if err = tx.SaveRule(rule); err != nil {
				return err
			}
		}
	}
	if kind == "group" || kind == "custom-proxy" {
		dialers, err := tx.ProxyDialers()
		if err != nil {
			return err
		}
		for _, dialer := range dialers {
			if kind == "group" && dialer.ProfileID != ownerID {
				continue
			}
			if next, exists := names[dialer.Target]; exists {
				dialer.Target = next
				if err = tx.SaveProxyDialer(dialer); err != nil {
					return err
				}
			}
		}
	}
	for _, profile := range profiles {
		if profile.Type != "custom" || kind == "group" && profile.ID != ownerID {
			continue
		}
		before := webAPIClone(profile)
		if err = webAPIRenameProfileCatalogReferences(&profile, kind, names, members[profile.ID]); err != nil {
			return err
		}
		if !reflect.DeepEqual(before, profile) {
			if err = tx.SaveProfile(profile); err != nil {
				return err
			}
		}
	}
	return nil
}

// An undeclared GLOBAL selects among every group of the profile and every proxy those groups list.
func webAPIRenameSelects(members map[string][]string, group, value string) bool {
	if explicit, declared := members[group]; declared || group != "GLOBAL" {
		return webAPIContains(explicit, value)
	}
	if _, isGroup := members[value]; isGroup {
		return true
	}
	for _, proxies := range members {
		if webAPIContains(proxies, value) {
			return true
		}
	}
	return false
}

func webAPIRenameProfileCatalogReferences(profile *WebAPIProfile, kind string, names map[string]string, members map[string][]string) error {
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return webAPIError(409, "unreadable_overrides", "cannot safely rewrite references through unreadable profile overrides")
	}
	if kind == "group" || kind == "custom-proxy" {
		picks := make(map[string]string, len(profile.SelectedMap))
		for group, value := range profile.SelectedMap {
			if webAPIRenameSelects(members, group, value) {
				if next, exists := names[value]; exists {
					value = next
				}
			}
			if kind == "group" {
				if next, exists := names[group]; exists {
					group = next
				}
			}
			if _, exists := picks[group]; exists {
				return webAPIError(409, "rename_conflict", "renamed selection keys would collide")
			}
			picks[group] = value
		}
		if profile.SelectedMap != nil {
			profile.SelectedMap = picks
		}
		profile.MatchTarget = webAPIRenamePointer(profile.MatchTarget, names)
		if profile.MatchTarget != nil && !webAPIName(*profile.MatchTarget) {
			return webAPIError(409, "unrepresentable_reference", "renamed MATCH target cannot round-trip through rule text")
		}
		if kind == "group" {
			profile.CurrentGroupName = webAPIRenamePointer(profile.CurrentGroupName, names)
			profile.UnfoldSet = webAPIRenameNames(profile.UnfoldSet, names)
		}
	}
	if profile.Overrides == nil {
		return nil
	}
	overrides := profile.Overrides
	var err error
	if kind == "proxy-provider" {
		overrides.ProxyProviders, err = webAPIRenameMap(overrides.ProxyProviders, names)
		return err
	}
	if kind == "group" || kind == "custom-proxy" {
		dialers, _ := webAPIDialers(overrides)
		if kind == "custom-proxy" {
			dialers, err = webAPIRenameMap(dialers, names)
			if err != nil {
				return err
			}
		}
		for source, target := range dialers {
			if next, exists := names[target]; exists {
				dialers[source] = next
			}
		}
		if len(overrides.DialerProxies) > 0 {
			overrides.DialerProxies, _ = json.Marshal(dialers)
		}
		if overrides.NTP != nil {
			if next, exists := names[overrides.NTP.DialerProxy]; exists {
				overrides.NTP.DialerProxy = next
			}
		}
		if overrides.DNS != nil {
			dns := overrides.DNS
			for _, list := range []*[]string{&dns.DefaultNameserver, &dns.Nameserver, &dns.Fallback, &dns.ProxyServerNameserver, &dns.DirectNameserver} {
				for i, server := range *list {
					(*list)[i], err = webAPIRenameDNSServer(server, names)
					if err != nil {
						return webAPIError(409, "unrepresentable_reference", err.Error())
					}
				}
			}
			for _, policy := range []map[string]string{dns.NameserverPolicy, dns.ProxyServerNameserverPolicy} {
				for key, value := range policy {
					servers := strings.FieldsFunc(value, func(c rune) bool { return c == ',' || c == ';' || c == '\n' })
					changed := false
					for i, server := range servers {
						server = strings.TrimSpace(server)
						next, e := webAPIRenameDNSServer(server, names)
						if e != nil {
							return webAPIError(409, "unrepresentable_reference", e.Error())
						}
						changed = changed || next != server
						servers[i] = next
					}
					if changed {
						policy[key] = strings.Join(servers, ", ")
					}
				}
			}
		}
	}
	if kind == "rule-provider" {
		if overrides.DNS != nil {
			dns := overrides.DNS
			for _, policy := range []*map[string]string{&dns.NameserverPolicy, &dns.ProxyServerNameserverPolicy} {
				if *policy == nil {
					continue
				}
				renamed := make(map[string]string, len(*policy))
				for key, value := range *policy {
					next, e := webAPIRenameRuleSetEntry(key, names, true)
					if e != nil {
						return webAPIError(409, "unrepresentable_reference", e.Error())
					}
					if _, exists := renamed[next]; exists {
						return webAPIError(409, "rename_conflict", "renamed DNS policy keys would collide")
					}
					renamed[next] = value
				}
				*policy = renamed
			}
			for i, entry := range dns.FakeIPFilter {
				next, e := webAPIRenameRuleSetEntry(entry, names, false)
				if e == nil {
					next, e = webAPIRenameRuleSetText(next, names)
				}
				if e != nil {
					return webAPIError(409, "unrepresentable_reference", e.Error())
				}
				dns.FakeIPFilter[i] = next
			}
		}
		if overrides.Sniffer != nil {
			sniffer := overrides.Sniffer
			for _, list := range []*[]string{&sniffer.ForceDomain, &sniffer.SkipDomain, &sniffer.SkipSrcAddress, &sniffer.SkipDstAddress} {
				for i, entry := range *list {
					next, e := webAPIRenameRuleSetEntry(entry, names, false)
					if e != nil {
						return webAPIError(409, "unrepresentable_reference", e.Error())
					}
					(*list)[i] = next
				}
			}
		}
	}
	return nil
}

func webAPIRejectCatalogReferences(tx *WebAPIWriteTx, kind string, ownerID WebAPIID, removed []string) error {
	if len(removed) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, name := range removed {
		names[name] = true
	}
	var references []webAPICatalogReference
	add := func(resource string, id, owner WebAPIID, path, name string) {
		if names[name] {
			references = append(references, webAPICatalogReference{Resource: resource, ID: id, ProfileID: owner, Path: path, Name: name})
		}
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if kind == "group" && group.ProfileID != ownerID {
			continue
		}
		if kind == "group" || kind == "custom-proxy" {
			for _, name := range group.Definition.Proxies {
				add("proxy-groups", group.ID, group.ProfileID, "definition.proxies", name)
			}
			if group.Definition.DefaultSelected != nil {
				add("proxy-groups", group.ID, group.ProfileID, "definition.default-selected", *group.Definition.DefaultSelected)
			}
			if group.Definition.EmptyFallback != nil {
				add("proxy-groups", group.ID, group.ProfileID, "definition.empty-fallback", *group.Definition.EmptyFallback)
			}
		}
		if kind == "proxy-provider" {
			for _, name := range group.Definition.Use {
				add("proxy-groups", group.ID, group.ProfileID, "definition.use", name)
			}
		}
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	custom := map[WebAPIID]bool{}
	for _, profile := range profiles {
		if profile.Type == "custom" {
			custom[profile.ID] = true
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.ProfileID == nil || !custom[*rule.ProfileID] || kind == "group" && *rule.ProfileID != ownerID {
			continue
		}
		if (kind == "group" || kind == "custom-proxy") && rule.RuleTarget != nil {
			add("rules", rule.ID, *rule.ProfileID, "ruleTarget", *rule.RuleTarget)
		}
		if kind == "rule-provider" {
			if rule.RuleProvider != nil {
				add("rules", rule.ID, *rule.ProfileID, "ruleProvider", *rule.RuleProvider)
			}
			if rule.Content != nil && webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
				for _, name := range webAPIRuleSetTextNames(*rule.Content) {
					add("rules", rule.ID, *rule.ProfileID, "content", name)
				}
			}
		}
	}
	if kind == "group" || kind == "custom-proxy" {
		dialers, err := tx.ProxyDialers()
		if err != nil {
			return err
		}
		for _, dialer := range dialers {
			if kind != "group" || dialer.ProfileID == ownerID {
				add("proxy-dialers", dialer.ProxyID, dialer.ProfileID, "target", dialer.Target)
			}
		}
		proxies, err := tx.CustomProxies()
		if err != nil {
			return err
		}
		for _, proxy := range proxies {
			add("custom-proxies", proxy.ID, "", "definition.dialer-proxy", webAPIProxyString(proxy.Definition, "dialer-proxy"))
		}
	}
	for _, profile := range profiles {
		if profile.Type != "custom" || kind == "group" && profile.ID != ownerID {
			continue
		}
		if kind == "group" || kind == "custom-proxy" {
			for key, value := range profile.SelectedMap {
				if kind == "group" {
					add("profiles", profile.ID, profile.ID, "selectedMap.key", key)
				}
				add("profiles", profile.ID, profile.ID, "selectedMap.value", value)
			}
			if profile.MatchTarget != nil {
				add("profiles", profile.ID, profile.ID, "matchTarget", *profile.MatchTarget)
			}
			if kind == "group" {
				if profile.CurrentGroupName != nil {
					add("profiles", profile.ID, profile.ID, "currentGroupName", *profile.CurrentGroupName)
				}
				for _, name := range profile.UnfoldSet {
					add("profiles", profile.ID, profile.ID, "unfoldSet", name)
				}
			}
		}
		for _, name := range removed {
			probe := webAPIClone(profile)
			if err = webAPIRenameProfileCatalogReferences(&probe, kind, map[string]string{name: "__removed_catalog_reference__"}, nil); err != nil {
				return err
			}
			if !reflect.DeepEqual(probe.Overrides, profile.Overrides) {
				references = append(references, webAPICatalogReference{Resource: "profiles", ID: profile.ID, ProfileID: profile.ID, Path: "overrides", Name: name})
			}
		}
	}
	return webAPICatalogReferenced(references)
}
