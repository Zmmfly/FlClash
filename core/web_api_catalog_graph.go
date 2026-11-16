//go:build webpanel && !android

package core

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/metacubex/mihomo/config"
)

func webAPIRequireCustomProfile(tx *WebAPIReadTx, id WebAPIID) (WebAPIProfile, error) {
	if _, err := webAPIInteger(string(id), true); err != nil {
		return WebAPIProfile{}, webAPIError(400, "invalid_id", "profileId is required")
	}
	profile, err := tx.Profile(id)
	if err != nil {
		return profile, err
	}
	if profile.Type != "custom" {
		return profile, webAPIError(409, "owner_conflict", "groups and dialers require a custom profile owner")
	}
	return profile, nil
}

func webAPIValidateGroup(definition WebAPIProxyGroupDefinition) error {
	if !webAPICatalogName(definition.Name) {
		return webAPIError(400, "invalid_name", "proxy group name is required")
	}
	if webAPIContains([]string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE"}, definition.Name) {
		return webAPIError(409, "reserved_name", "proxy group name is reserved")
	}
	if !webAPIContains([]string{"select", "url-test", "fallback", "load-balance"}, definition.Type) {
		return webAPIError(400, "invalid_group_type", "unknown or removed proxy group type")
	}
	for _, values := range [][]string{definition.Proxies, definition.Use} {
		seen := map[string]bool{}
		for _, name := range values {
			if !webAPICatalogName(name) || seen[name] {
				return webAPIError(400, "invalid_group_members", "group members must be valid unique names")
			}
			seen[name] = true
		}
	}
	for _, value := range []*int32{definition.Interval, definition.Timeout, definition.MaxFailedTimes, definition.Tolerance} {
		if value != nil && *value < 0 {
			return webAPIError(400, "invalid_group_option", "group numeric options cannot be negative")
		}
	}
	if len(definition.Proxies) == 0 && len(definition.Use) == 0 && !webAPIBool(definition.IncludeAll) && !webAPIBool(definition.IncludeAllProxies) && !webAPIBool(definition.IncludeAllProviders) {
		return webAPIError(400, "missing_group_source", "proxy group requires members or an include-all source")
	}
	if definition.URL != nil && *definition.URL != "" && !webAPICatalogURL(*definition.URL, false) {
		return webAPIError(400, "invalid_group_url", "expected an HTTP(S) group test URL")
	}
	if definition.Strategy != nil && !webAPIContains([]string{"consistent-hashing", "round-robin", "sticky-sessions"}, *definition.Strategy) {
		return webAPIError(400, "invalid_group_strategy", "unknown load-balance strategy")
	}
	if definition.Strategy != nil && *definition.Strategy == "round-robin" && definition.HashKey != nil && *definition.HashKey != "" {
		return webAPIError(400, "invalid_group_strategy", "round-robin groups cannot set hash-key")
	}
	for name, position := range definition.UsePosition {
		if !webAPIContains(definition.Use, name) || position < 0 || int(position) > len(definition.Proxies) {
			return webAPIError(400, "invalid_use_position", "use-position must name a selected provider at a valid member index")
		}
	}
	if definition.ExpectedStatus != nil && *definition.ExpectedStatus != "" && *definition.ExpectedStatus != "*" && !webAPIRanges(*definition.ExpectedStatus, 65535) {
		return webAPIError(400, "invalid_expected_status", "invalid HTTP status range")
	}
	return nil
}

func webAPIBool(value *bool) bool { return value != nil && *value }

func webAPIGroupResource(owner WebAPIID) webAPICatalogResource[WebAPIProxyGroup, webAPIGroupInput] {
	var resource webAPICatalogResource[WebAPIProxyGroup, webAPIGroupInput]
	resource = webAPICatalogResource[WebAPIProxyGroup, webAPIGroupInput]{
		list: func(tx *WebAPIReadTx) ([]WebAPIProxyGroup, error) {
			if _, err := webAPIRequireCustomProfile(tx, owner); err != nil {
				return nil, err
			}
			all, err := tx.ProxyGroups()
			if err != nil {
				return nil, err
			}
			items := make([]WebAPIProxyGroup, 0)
			for _, item := range all {
				if item.ProfileID == owner {
					items = append(items, item)
				}
			}
			return webAPISortStringResources(items, func(v WebAPIProxyGroup) WebAPIID { return v.ID }, func(v WebAPIProxyGroup) *string { return v.Order }), nil
		},
		load: func(tx *WebAPIReadTx, id WebAPIID) (WebAPIProxyGroup, error) {
			item, err := tx.ProxyGroup(id)
			if err != nil {
				return item, err
			}
			if item.ProfileID != owner {
				return item, webAPIError(409, "owner_conflict", "proxy group identity belongs to a different owner")
			}
			_, err = webAPIRequireCustomProfile(tx, owner)
			return item, err
		},
		insert: (*WebAPIWriteTx).InsertProxyGroup, save: (*WebAPIWriteTx).SaveProxyGroup, remove: (*WebAPIWriteTx).DeleteProxyGroup,
		id: func(v WebAPIProxyGroup) WebAPIID { return v.ID }, name: func(v WebAPIProxyGroup) string { return v.Definition.Name },
		order: func(v WebAPIProxyGroup, i int) WebAPIProxyGroup { order := webAPIOrder(i); v.Order = &order; return v },
		build: func(id WebAPIID, input webAPIGroupInput, old *WebAPIProxyGroup) (WebAPIProxyGroup, error) {
			v := WebAPIProxyGroup{ID: id, ProfileID: owner}
			if old != nil {
				v = *old
			}
			if err := webAPIValidateGroup(input.Definition); err != nil {
				return v, err
			}
			v.Definition, v.MemberLayout = input.Definition, input.MemberLayout
			return v, nil
		},
		changed: func(tx *WebAPIWriteTx, before, after []WebAPIProxyGroup) error {
			renames, removed := webAPICatalogChanges(resource, before, after)
			if err := webAPIRenameCatalogReferences(tx, "group", owner, renames); err != nil {
				return err
			}
			if err := webAPIRejectCatalogReferences(tx, "group", owner, removed); err != nil {
				return err
			}
			return RefreshWebAPICatalog(tx)
		},
	}
	return resource
}

func webAPIProxyResource() webAPICatalogResource[WebAPICustomProxy, webAPIProxyInput] {
	var resource webAPICatalogResource[WebAPICustomProxy, webAPIProxyInput]
	resource = webAPICatalogResource[WebAPICustomProxy, webAPIProxyInput]{
		list: func(tx *WebAPIReadTx) ([]WebAPICustomProxy, error) {
			items, err := tx.CustomProxies()
			return webAPISortStringResources(items, func(v WebAPICustomProxy) WebAPIID { return v.ID }, func(v WebAPICustomProxy) *string { return v.Order }), err
		},
		load: (*WebAPIReadTx).CustomProxy, insert: (*WebAPIWriteTx).InsertCustomProxy, save: (*WebAPIWriteTx).SaveCustomProxy,
		remove: func(tx *WebAPIWriteTx, id WebAPIID) error {
			dialers, err := tx.ProxyDialers()
			if err != nil {
				return err
			}
			for _, dialer := range dialers {
				if dialer.ProxyID == id {
					if err := tx.DeleteProxyDialer(dialer.ProfileID, id); err != nil {
						return err
					}
				}
			}
			return tx.DeleteCustomProxy(id)
		},
		id: func(v WebAPICustomProxy) WebAPIID { return v.ID }, name: func(v WebAPICustomProxy) string { return webAPIProxyString(v.Definition, "name") },
		order: func(v WebAPICustomProxy, i int) WebAPICustomProxy {
			order := webAPIOrder(i)
			v.Order = &order
			return v
		},
		build: func(id WebAPIID, input webAPIProxyInput, old *WebAPICustomProxy) (WebAPICustomProxy, error) {
			v := WebAPICustomProxy{ID: id}
			if old != nil {
				v = *old
			}
			if err := webAPIValidateProxy(input.Definition, input.EmptyOptions); err != nil {
				return v, err
			}
			v.Definition, v.EmptyOptions = input.Definition, input.EmptyOptions
			if v.EmptyOptions == nil {
				v.EmptyOptions = []string{}
			}
			return v, nil
		},
		changed: func(tx *WebAPIWriteTx, before, after []WebAPICustomProxy) error {
			renames, removed := webAPICatalogChanges(resource, before, after)
			if err := webAPIRenameCatalogReferences(tx, "custom-proxy", "", renames); err != nil {
				return err
			}
			if err := webAPIRejectCatalogReferences(tx, "custom-proxy", "", removed); err != nil {
				return err
			}
			return RefreshWebAPICatalog(tx)
		},
	}
	return resource
}

type webAPIDialerInput struct {
	ProxyID WebAPIID `json:"proxyId"`
	Target  string   `json:"target"`
}
type webAPIDialerSelection struct {
	ProfileID WebAPIID            `json:"profileId"`
	Dialers   []webAPIDialerInput `json:"dialers"`
}

func webAPIReadDialers(tx *WebAPIReadTx, owner WebAPIID) (webAPIDialerSelection, error) {
	result := webAPIDialerSelection{ProfileID: owner, Dialers: []webAPIDialerInput{}}
	if _, err := webAPIRequireCustomProfile(tx, owner); err != nil {
		return result, err
	}
	items, err := tx.ProxyDialers()
	if err != nil {
		return result, err
	}
	for _, item := range items {
		if item.ProfileID == owner {
			result.Dialers = append(result.Dialers, webAPIDialerInput{ProxyID: item.ProxyID, Target: item.Target})
		}
	}
	sort.Slice(result.Dialers, func(i, j int) bool { return webAPIIDLess(result.Dialers[i].ProxyID, result.Dialers[j].ProxyID) })
	return result, nil
}

func webAPIReplaceDialers(tx *WebAPIWriteTx, owner WebAPIID, items []webAPIDialerInput) (webAPIDialerSelection, error) {
	previous, err := webAPIReadDialers(tx.WebAPIReadTx, owner)
	if err != nil {
		return previous, err
	}
	seen := map[WebAPIID]bool{}
	for _, item := range items {
		if seen[item.ProxyID] {
			return previous, webAPIError(409, "duplicate_id", "duplicate dialer source")
		}
		seen[item.ProxyID] = true
		proxy, e := tx.CustomProxy(item.ProxyID)
		if e != nil {
			return previous, e
		}
		if item.Target != "" && (!webAPICatalogName(item.Target) || !dependencySupportsDialer(webAPIProxyString(proxy.Definition, "type"))) {
			return previous, webAPIError(400, "invalid_dialer", "unsupported dialer source or invalid target")
		}
	}
	for _, item := range previous.Dialers {
		if err := tx.DeleteProxyDialer(owner, item.ProxyID); err != nil {
			return previous, err
		}
	}
	for _, item := range items {
		if err := tx.SaveProxyDialer(WebAPIProxyDialer{ProfileID: owner, ProxyID: item.ProxyID, Target: item.Target}); err != nil {
			return previous, err
		}
	}
	if err := RefreshWebAPICatalog(tx); err != nil {
		return previous, err
	}
	return webAPIReadDialers(tx.WebAPIReadTx, owner)
}

func webAPIEffectiveProfileLabel(profile WebAPIProfile) string {
	if profile.Label != "" {
		return profile.Label
	}
	if parsed, err := url.Parse(profile.URL); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return string(profile.ID)
}

func webAPIRuleLine(rule WebAPIRule) string {
	action := strings.ReplaceAll(rule.RuleAction, "_", "-")
	parts := []string{action}
	if rule.RuleAction == "RULE_SET" {
		if rule.RuleProvider != nil {
			parts = append(parts, *rule.RuleProvider)
		}
	} else if rule.Content != nil {
		parts = append(parts, *rule.Content)
	}
	if rule.RuleAction == "SUB_RULE" {
		if rule.SubRule != nil {
			parts = append(parts, *rule.SubRule)
		}
	} else if rule.RuleTarget != nil {
		parts = append(parts, *rule.RuleTarget)
	}
	if rule.NoResolve {
		parts = append(parts, "no-resolve")
	}
	if rule.Src {
		parts = append(parts, "src")
	}
	return strings.Join(parts, ",")
}

func webAPICatalogRawCandidate(tx *WebAPIReadTx, profile WebAPIProfile) (*config.RawConfig, WebAPIProfileCatalog, error) {
	return webAPICatalogRawCandidateMode(tx, profile, false)
}

func webAPICatalogRawCandidateMode(tx *WebAPIReadTx, profile WebAPIProfile, allowQuarantine bool) (*config.RawConfig, WebAPIProfileCatalog, error) {
	return webAPICatalogRawCandidateWithBudget(tx, profile, allowQuarantine, NewWebAPICatalogReadBudget(webAPICatalogScopedReadBytes))
}

func webAPICatalogRawCandidateWithBudget(tx *WebAPIReadTx, profile WebAPIProfile, allowQuarantine bool, budget *WebAPICatalogReadBudget) (*config.RawConfig, WebAPIProfileCatalog, error) {
	rows, err := webAPICatalogReadCandidateRows(tx, profile, budget)
	if err != nil {
		return nil, WebAPIProfileCatalog{}, err
	}
	return webAPICatalogRawCandidateRows(profile, rows, allowQuarantine)
}

func webAPICatalogRawCandidateRows(profile WebAPIProfile, rows webAPICatalogCandidateRows, allowQuarantine bool) (*config.RawConfig, WebAPIProfileCatalog, error) {
	raw := config.DefaultRawConfig()
	raw.Proxy, raw.ProxyGroup, raw.Rule = nil, nil, nil
	raw.ProxyProvider = map[string]map[string]any{}
	catalog := WebAPIProfileCatalog{OutboundNames: []string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL"}, RuleProviderNames: []string{}, SubRuleNames: []string{}}
	groups := rows.groups
	declared := map[string]bool{}
	providerNames := map[string]bool{}
	for _, group := range groups {
		if group.ProfileID != profile.ID {
			continue
		}
		if err := webAPIValidateGroup(group.Definition); err != nil {
			return nil, catalog, err
		}
		data, _ := json.Marshal(group.Definition)
		var mapping map[string]any
		if err := json.Unmarshal(data, &mapping); err != nil {
			return nil, catalog, err
		}
		raw.ProxyGroup = append(raw.ProxyGroup, mapping)
		if group.Definition.Name != "GLOBAL" {
			catalog.OutboundNames = append(catalog.OutboundNames, group.Definition.Name)
		}
		for _, name := range group.Definition.Proxies {
			declared[name] = true
		}
		for _, name := range group.Definition.Use {
			providerNames[name] = true
		}
	}
	allProfiles := rows.providerProfiles
	providerCounts := map[string]int{}
	for _, p := range allProfiles {
		if p.Type != "custom" {
			providerCounts[webAPIEffectiveProfileLabel(p)]++
		}
	}
	for name := range providerNames {
		if providerCounts[name] > 1 {
			return nil, catalog, webAPIError(409, "duplicate_name", "proxy provider profile identity is ambiguous")
		}
	}
	for _, provider := range allProfiles {
		name := webAPIEffectiveProfileLabel(provider)
		if provider.Type != "custom" && providerNames[name] {
			if webAPIUnreadable(provider.WebAPIProfileInput) && !allowQuarantine {
				return nil, catalog, webAPIError(409, "unreadable_overrides", "proxy provider profile is quarantined")
			}
			if state, exists := rows.providerStates[provider.ID]; exists && state.UnavailableReason != "" && !allowQuarantine {
				return nil, catalog, webAPICatalogUnavailable(provider.ID, state.UnavailableReason)
			}
			definition := map[string]any{"type": "file"}
			if content, exists := rows.providerContents[provider.ID]; exists {
				parsed, _, e := webContentProfile(content.Bytes, provider.AgeSecretKey)
				if e != nil {
					return nil, catalog, webAPIError(409, "invalid_provider_content", "stored proxy provider content cannot be validated")
				}
				definition = map[string]any{"type": "inline", "payload": parsed.Proxy}
			}
			raw.ProxyProvider[name] = definition
			delete(providerNames, name)
		}
	}
	if len(providerNames) > 0 {
		return nil, catalog, webAPIError(409, "invalid_reference", "proxy group names an absent proxy provider profile")
	}
	proxies := rows.proxies
	dialers := rows.dialers
	bySource := map[WebAPIID]string{}
	for _, dialer := range dialers {
		if dialer.ProfileID == profile.ID {
			if _, exists := bySource[dialer.ProxyID]; exists {
				return nil, catalog, webAPIError(409, "duplicate_dialer", "duplicate stored dialer source")
			}
			bySource[dialer.ProxyID] = dialer.Target
		}
	}
	for _, proxy := range proxies {
		name := webAPIProxyString(proxy.Definition, "name")
		if !declared[name] {
			continue
		}
		data, _ := json.Marshal(proxy.Definition)
		var mapping map[string]any
		if err := json.Unmarshal(data, &mapping); err != nil {
			return nil, catalog, err
		}
		delete(mapping, "dialer-proxy")
		if target, exists := bySource[proxy.ID]; exists {
			mapping["dialer-proxy"] = target
		}
		raw.Proxy = append(raw.Proxy, mapping)
		catalog.OutboundNames = append(catalog.OutboundNames, name)
	}
	proxyIDs := map[WebAPIID]bool{}
	for _, proxy := range proxies {
		proxyIDs[proxy.ID] = true
	}
	rootNames := map[string]bool{}
	for _, name := range catalog.OutboundNames {
		rootNames[name] = true
	}
	for _, dialer := range dialers {
		if dialer.ProfileID != profile.ID {
			continue
		}
		if !proxyIDs[dialer.ProxyID] {
			return nil, catalog, webAPIError(409, "invalid_reference", "dialer source does not exist")
		}
		if dialer.Target != "" && !rootNames[dialer.Target] {
			return nil, catalog, webAPIError(409, "invalid_reference", "dialer target is absent from the owning profile")
		}
	}
	providers := rows.ruleProviders
	validProviders := map[string]bool{}
	for _, provider := range providers {
		validProviders[provider.Label] = true
		catalog.RuleProviderNames = append(catalog.RuleProviderNames, provider.Label)
	}
	rules := rows.rules
	for _, rule := range rules {
		if rule.ProfileID == nil || *rule.ProfileID != profile.ID {
			continue
		}
		if rule.RuleAction == "RULE_SET" && (rule.RuleProvider == nil || !validProviders[*rule.RuleProvider]) {
			return nil, catalog, webAPIError(409, "invalid_reference", "rule provider does not exist")
		}
		if rule.Content != nil && webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
			for _, name := range webAPIRuleSetTextNames(*rule.Content) {
				if !validProviders[name] {
					return nil, catalog, webAPIError(409, "invalid_reference", "nested rule provider does not exist")
				}
			}
		}
		raw.Rule = append(raw.Rule, webAPIRuleLine(rule))
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return nil, catalog, webAPIError(409, "unreadable_overrides", "cannot validate a candidate through unreadable profile overrides")
	}
	if profile.Overrides != nil && len(profile.Overrides.ProxyProviders) > 0 {
		for name, options := range profile.Overrides.ProxyProviders {
			if provider, exists := raw.ProxyProvider[name]; exists {
				data, _ := json.Marshal(options)
				var mapping map[string]any
				_ = json.Unmarshal(data, &mapping)
				for key, value := range mapping {
					if value != nil {
						provider[key] = value
					}
				}
			}
		}
	}
	return raw, catalog, nil
}

func webAPICatalogGroupMembers(graph *dependencyGraph, group string) ([]string, bool, error) {
	root, exists := graph.roots[group]
	if !exists || graph.nodes[root].ref.Kind != "group" {
		return nil, false, webAPIError(404, "not_found", "proxy group does not exist")
	}
	seenNodes := map[int]bool{}
	names := map[string]bool{}
	ready := true
	pending := append([]int{}, graph.nodes[root].edges...)
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seenNodes[node] {
			continue
		}
		seenNodes[node] = true
		info := graph.nodes[node]
		if info.unknown || info.ref.Kind == "provider" && !graph.providers[info.ref.Name].known {
			ready = false
		}
		if info.ref.Kind == "aggregate" || info.ref.Kind == "provider" {
			pending = append(pending, info.edges...)
			continue
		}
		names[info.ref.Name] = true
	}
	if !ready {
		fallback := graph.groups[group].EmptyFallback
		if fallback == "" {
			fallback = "COMPATIBLE"
		}
		if !webAPIContains(graph.groups[group].Proxies, fallback) {
			delete(names, fallback)
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, ready, nil
}

func WebAPICatalogGroupMemberNames(tx *WebAPIReadTx, owner WebAPIID, group string) ([]string, bool, error) {
	profile, err := tx.Profile(owner)
	if err != nil {
		return nil, false, err
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return nil, false, webAPIError(409, "catalog_unavailable", "profile overrides are quarantined")
	}
	var raw *config.RawConfig
	if profile.Type == "custom" {
		raw, _, err = webAPICatalogRawCandidate(tx, profile)
	} else {
		source, e := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: owner})
		if e != nil {
			return nil, false, webAPIError(409, "catalog_unavailable", "profile source is not prepared")
		}
		raw, _, err = webContentProfile(source.Bytes, profile.AgeSecretKey)
	}
	if err != nil {
		return nil, false, err
	}
	graph, err := webAPICatalogMemberGraph(raw)
	if err != nil {
		return nil, false, err
	}
	return webAPICatalogGroupMembers(graph, group)
}

func webAPICatalogMemberGraph(raw *config.RawConfig) (*dependencyGraph, error) {
	candidate := webAPIClone(*raw)
	for _, proxy := range candidate.Proxy {
		delete(proxy, "dialer-proxy")
	}
	for _, provider := range candidate.ProxyProvider {
		delete(provider, "dialer-proxy")
		if override, ok := provider["override"].(map[string]any); ok {
			delete(override, "dialer-proxy")
		}
		if payload, ok := provider["payload"].([]any); ok {
			for _, value := range payload {
				if proxy, ok := value.(map[string]any); ok {
					delete(proxy, "dialer-proxy")
				}
			}
		}
	}
	return buildDependencyGraph(&candidate)
}

func webAPIValidateCatalogProfileReferences(profile WebAPIProfile, raw *config.RawConfig, catalog WebAPIProfileCatalog, graph *dependencyGraph) error {
	return webAPIValidateCatalogProfileReferencesMode(profile, raw, catalog, graph, false)
}

func webAPIValidateWrittenSelections(input WebAPIProfileInput, graph *dependencyGraph) error {
	if input.CurrentGroupName != nil && *input.CurrentGroupName != "" {
		node, exists := graph.roots[*input.CurrentGroupName]
		if !exists || graph.nodes[node].ref.Kind != "group" {
			return webAPIError(409, "invalid_reference", "profile current group is absent from the candidate")
		}
	}
	for group, target := range input.SelectedMap {
		if target == "" {
			continue
		}
		members, ready, err := webAPICatalogGroupMembers(graph, group)
		if err != nil {
			return err
		}
		if !webAPIContains(members, target) {
			if !ready {
				return webAPIError(409, "catalog_unavailable", "provider members are not prepared; saved selection cannot be validated")
			}
			return webAPIError(409, "invalid_reference", "profile selection is absent from the group's selectable members")
		}
	}
	return nil
}

func webAPIValidateCatalogProfileReferencesMode(profile WebAPIProfile, raw *config.RawConfig, catalog WebAPIProfileCatalog, graph *dependencyGraph, allowUnknown bool) error {
	if err := webAPIValidateGroupProviderNamespace(raw, graph); err != nil {
		return err
	}
	validTarget := func(name string) bool { _, exists := graph.roots[name]; return exists }
	for _, mapping := range raw.ProxyGroup {
		group, _ := mapping["name"].(string)
		selected, _ := mapping["default-selected"].(string)
		if selected == "" {
			continue
		}
		members, ready, err := webAPICatalogGroupMembers(graph, group)
		if err != nil {
			return err
		}
		if !webAPIContains(members, selected) {
			if !ready {
				if allowUnknown {
					continue
				}
				return webAPIError(409, "catalog_unavailable", "provider members are not prepared; default selection cannot be validated")
			}
			return webAPIError(409, "invalid_reference", "default selection is absent from the group's selectable members")
		}
	}
	if profile.MatchTarget != nil && *profile.MatchTarget != "" && !validTarget(*profile.MatchTarget) {
		return webAPIError(409, "invalid_reference", "profile MATCH target is absent from the candidate")
	}
	if profile.Overrides == nil {
		return nil
	}
	overrides := profile.Overrides
	checkServer := func(server string) error {
		names, err := webAPIDNSProxyNames(server)
		if err != nil {
			return webAPIError(409, "invalid_reference", "stored DNS server cannot be interpreted")
		}
		for _, name := range names {
			if !validTarget(name) {
				return webAPIError(409, "invalid_reference", "DNS server outbound is absent from the candidate")
			}
		}
		return nil
	}
	checkRuleSet := func(name string) error {
		if !webAPIContains(catalog.RuleProviderNames, name) {
			return webAPIError(409, "invalid_reference", "override names an absent rule provider")
		}
		return nil
	}
	if overrides.NTP != nil && overrides.NTP.DialerProxy != "" && !validTarget(overrides.NTP.DialerProxy) {
		return webAPIError(409, "invalid_reference", "NTP outbound is absent from the candidate")
	}
	if overrides.DNS != nil {
		dns := overrides.DNS
		for _, list := range [][]string{dns.DefaultNameserver, dns.Nameserver, dns.Fallback, dns.ProxyServerNameserver, dns.DirectNameserver} {
			for _, server := range list {
				if err := checkServer(server); err != nil {
					return err
				}
			}
		}
		for _, policy := range []map[string]string{dns.NameserverPolicy, dns.ProxyServerNameserverPolicy} {
			for key, value := range policy {
				for _, name := range webAPIRuleSetEntryNames(key, true) {
					if err := checkRuleSet(name); err != nil {
						return err
					}
				}
				for _, server := range strings.FieldsFunc(value, func(c rune) bool { return c == ',' || c == ';' || c == '\n' }) {
					if err := checkServer(strings.TrimSpace(server)); err != nil {
						return err
					}
				}
			}
		}
		for _, entry := range dns.FakeIPFilter {
			names := webAPIRuleSetEntryNames(entry, false)
			names = append(names, webAPIRuleSetTextNames(entry)...)
			for _, name := range names {
				if err := checkRuleSet(name); err != nil {
					return err
				}
			}
		}
	}
	if overrides.Sniffer != nil {
		sniffer := overrides.Sniffer
		for _, list := range [][]string{sniffer.ForceDomain, sniffer.SkipDomain, sniffer.SkipSrcAddress, sniffer.SkipDstAddress} {
			for _, entry := range list {
				for _, name := range webAPIRuleSetEntryNames(entry, false) {
					if err := checkRuleSet(name); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func WebAPICatalogQuarantinedProfiles(tx *WebAPIReadTx) ([]WebAPIID, error) {
	profiles, err := tx.Profiles()
	if err != nil {
		return nil, err
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return nil, err
	}
	quarantined := map[WebAPIID]bool{}
	byLabel := map[string]WebAPIID{}
	for _, profile := range profiles {
		if webAPIUnreadable(profile.WebAPIProfileInput) {
			quarantined[profile.ID] = true
		}
		if profile.Type != "custom" {
			byLabel[webAPIEffectiveProfileLabel(profile)] = profile.ID
		}
	}
	for changed := true; changed; {
		changed = false
		for _, group := range groups {
			if quarantined[group.ProfileID] {
				continue
			}
			for _, name := range group.Definition.Use {
				if quarantined[byLabel[name]] {
					quarantined[group.ProfileID] = true
					changed = true
					break
				}
			}
		}
	}
	result := make([]WebAPIID, 0, len(quarantined))
	for id := range quarantined {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return webAPIIDLess(result[i], result[j]) })
	return result, nil
}

func webAPIValidatedCatalogs(tx *WebAPIReadTx, restore bool) (map[WebAPIID]WebAPIProfileCatalog, error) {
	profiles, err := tx.Profiles()
	if err != nil {
		return nil, err
	}
	proxies, err := tx.CustomProxies()
	if err != nil {
		return nil, err
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return nil, err
	}
	proxyNames := map[string]bool{}
	for _, proxy := range proxies {
		if err := webAPIValidateProxy(proxy.Definition, proxy.EmptyOptions); err != nil {
			return nil, err
		}
		name := webAPIProxyString(proxy.Definition, "name")
		if proxyNames[name] {
			return nil, webAPIError(409, "duplicate_name", "custom proxy name is already used")
		}
		proxyNames[name] = true
	}
	groupNames := map[WebAPIID]map[string]bool{}
	for _, group := range groups {
		if _, err := webAPIRequireCustomProfile(tx, group.ProfileID); err != nil {
			return nil, err
		}
		if err := webAPIValidateGroup(group.Definition); err != nil {
			return nil, err
		}
		if groupNames[group.ProfileID] == nil {
			groupNames[group.ProfileID] = map[string]bool{}
		}
		name := group.Definition.Name
		if proxyNames[name] || groupNames[group.ProfileID][name] {
			return nil, webAPIError(409, "duplicate_name", "proxy and group names share one outbound namespace")
		}
		groupNames[group.ProfileID][name] = true
	}
	dialers, err := tx.ProxyDialers()
	if err != nil {
		return nil, err
	}
	for _, dialer := range dialers {
		if _, err := webAPIRequireCustomProfile(tx, dialer.ProfileID); err != nil {
			return nil, err
		}
		source, err := tx.CustomProxy(dialer.ProxyID)
		if err != nil {
			return nil, webAPIError(409, "invalid_reference", "dialer source does not exist")
		}
		if dialer.Target != "" && (!webAPICatalogName(dialer.Target) || !dependencySupportsDialer(webAPIProxyString(source.Definition, "type"))) {
			return nil, webAPIError(400, "invalid_dialer", "unsupported dialer source or invalid target")
		}
	}
	providers, err := tx.ClashProviders()
	if err != nil {
		return nil, err
	}
	providerNames := map[string]bool{}
	for _, provider := range providers {
		if !webAPICatalogName(provider.Label) || providerNames[provider.Label] {
			return nil, webAPIError(409, "duplicate_name", "rule provider name is invalid or duplicated")
		}
		providerNames[provider.Label] = true
		if !webAPIContains([]string{"domain", "ipcidr", "classical"}, provider.Behavior) || !webAPIContains([]string{"yaml", "text", "mrs"}, provider.Format) {
			return nil, webAPIError(400, "invalid_provider", "unknown rule provider behavior or format")
		}
	}
	for _, profile := range profiles {
		if profile.ScriptID != nil {
			if _, err := tx.Script(*profile.ScriptID); err != nil {
				return nil, webAPIError(409, "invalid_reference", "profile script does not exist")
			}
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if rule.ProfileID != nil {
			if _, err := tx.Profile(*rule.ProfileID); err != nil {
				return nil, webAPIError(409, "invalid_reference", "rule owner does not exist")
			}
		}
		if err := webAPIValidateStoredRuleSyntax(rule.WebAPIRuleInput); err != nil {
			return nil, err
		}
	}
	quarantineIDs, err := WebAPICatalogQuarantinedProfiles(tx)
	if err != nil {
		return nil, err
	}
	quarantine := map[WebAPIID]bool{}
	for _, id := range quarantineIDs {
		quarantine[id] = true
	}
	catalogs := map[WebAPIID]WebAPIProfileCatalog{}
	for _, profile := range profiles {
		if profile.Type != "custom" {
			continue
		}
		if quarantine[profile.ID] {
			if !restore {
				return nil, webAPIError(409, "unreadable_overrides", "profile or proxy provider dependency is quarantined")
			}
			copy := webAPIClone(profile)
			copy.OverridesRawDocument = nil
			if copy.Overrides != nil {
				copy.Overrides.DialerProxies = nil
			}
			if _, _, err := webAPICatalogRawCandidateForRestore(tx, copy); err != nil {
				return nil, err
			}
			continue
		}
		raw, catalog, err := webAPICatalogRawCandidate(tx, profile)
		if err != nil {
			return nil, err
		}
		graph, err := buildDependencyGraph(raw)
		if err != nil {
			var policy *DependencyPolicyError
			failure := webAPIError(409, "outbound_policy_invalid", "candidate outbound graph is invalid")
			if errors.As(err, &policy) {
				failure.Details = map[string]any{"reason": policy.Reason, "source": policy.Source, "target": policy.Target, "path": policy.Path, "profileId": profile.ID}
			}
			return nil, failure
		}
		if err := webAPIValidateCatalogProfileReferences(profile, raw, catalog, graph); err != nil {
			return nil, err
		}
		catalogs[profile.ID] = catalog
	}
	return catalogs, nil
}

func webAPICatalogRawCandidateForRestore(tx *WebAPIReadTx, profile WebAPIProfile) (*config.RawConfig, WebAPIProfileCatalog, error) {
	raw, catalog, err := webAPICatalogRawCandidateMode(tx, profile, true)
	if err != nil {
		return nil, catalog, err
	}
	unknown := map[string]bool{}
	shadow := webAPIClone(*raw)
	for name, provider := range shadow.ProxyProvider {
		if provider["type"] != "inline" {
			unknown[name] = true
			override, _ := provider["override"].(map[string]any)
			if override == nil {
				override = map[string]any{}
			}
			if _, exists := override["dialer-proxy"]; !exists {
				if _, exists := provider["dialer-proxy"]; !exists {
					override["dialer-proxy"] = ""
				}
			}
			provider["override"] = override
		}
		if override, ok := provider["override"].(map[string]any); ok {
			if _, exists := override["override-expr"]; exists {
				unknown[name] = true
				delete(override, "override-expr")
			}
		}
	}
	graph, err := buildDependencyGraph(&shadow)
	if err != nil {
		return nil, catalog, webAPICatalogPolicyError(err, profile.ID)
	}
	for i := range graph.nodes {
		if graph.nodes[i].ref.Kind == "provider" && unknown[graph.nodes[i].ref.Name] {
			graph.nodes[i].unknown = true
		}
	}
	if err := webAPIValidateCatalogProfileReferencesMode(profile, raw, catalog, graph, true); err != nil {
		return nil, catalog, err
	}
	return raw, catalog, nil
}

func ValidateWebAPICatalog(tx *WebAPIReadTx) error {
	_, err := webAPIValidatedCatalogs(tx, false)
	return err
}

func ValidateWebAPICatalogForRestore(tx *WebAPIReadTx) error {
	_, err := webAPIValidatedCatalogs(tx, true)
	return err
}

func RefreshWebAPICatalog(tx *WebAPIWriteTx) error {
	catalogs, err := webAPIValidatedCatalogs(tx.WebAPIReadTx, false)
	if err != nil {
		return err
	}
	for id, catalog := range catalogs {
		sort.Strings(catalog.OutboundNames)
		sort.Strings(catalog.RuleProviderNames)
		if err := tx.SetProfileCatalog(id, catalog); err != nil {
			return err
		}
	}
	return nil
}

type webAPIRestoreCatalog struct {
	id                WebAPIID
	catalog           WebAPIProfileCatalog
	unavailableReason string
}

func webAPIPrepareRestoreCatalogs(tx *WebAPIReadTx) ([]webAPIRestoreCatalog, error) {
	known, err := webAPIValidatedCatalogs(tx, true)
	if err != nil {
		return nil, err
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return nil, err
	}
	quarantinedIDs, err := WebAPICatalogQuarantinedProfiles(tx)
	if err != nil {
		return nil, err
	}
	quarantined := map[WebAPIID]bool{}
	for _, id := range quarantinedIDs {
		quarantined[id] = true
	}
	result := make([]webAPIRestoreCatalog, 0, len(profiles))
	for _, profile := range profiles {
		item := webAPIRestoreCatalog{id: profile.ID, catalog: WebAPIProfileCatalog{OutboundNames: []string{}, RuleProviderNames: []string{}, SubRuleNames: []string{}, GroupMembers: map[string][]string{}}}
		if quarantined[profile.ID] {
			item.unavailableReason = WebAPICatalogDependencyQuarantined
			if webAPIUnreadable(profile.WebAPIProfileInput) {
				item.unavailableReason = WebAPICatalogUnreadableOverrides
			}
			result = append(result, item)
			continue
		}
		if profile.Type == "custom" {
			item.catalog = known[profile.ID]
			result = append(result, item)
			continue
		}
		content, e := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID})
		if e != nil {
			var problem *WebAPIError
			if errors.As(e, &problem) && problem.Status == 404 {
				item.unavailableReason = WebAPICatalogContentUnavailable
				result = append(result, item)
				continue
			}
			return nil, e
		}
		_, catalog, e := webContentProfile(content.Bytes, profile.AgeSecretKey)
		if e != nil {
			return nil, webAPIError(409, "catalog_unavailable", "restored profile source cannot be interpreted")
		}
		item.catalog = catalog
		result = append(result, item)
	}
	return result, nil
}

func RefreshWebAPICatalogForRestore(tx *WebAPIWriteTx) error {
	catalogs, err := webAPIPrepareRestoreCatalogs(tx.WebAPIReadTx)
	if err != nil {
		return err
	}
	for _, item := range catalogs {
		catalog := item.catalog
		catalog.UnavailableReason = item.unavailableReason
		sort.Strings(catalog.OutboundNames)
		sort.Strings(catalog.RuleProviderNames)
		if err := tx.SetProfileCatalog(item.id, catalog); err != nil {
			return err
		}
	}
	return nil
}

func webAPIValidateGroupProviderNamespace(raw *config.RawConfig, graph *dependencyGraph) error {
	for name, group := range graph.groups {
		if _, exists := raw.ProxyProvider[name]; !exists {
			continue
		}
		explicit := len(group.Proxies)
		providers := len(group.Use)
		if group.IncludeAll || group.IncludeAllProviders {
			providers = len(raw.ProxyProvider)
		}
		if group.IncludeAll || group.IncludeAllProxies {
			if group.Filter == "" {
				explicit += len(raw.Proxy)
			} else {
				patterns, err := dependencyRegex(group.Filter)
				if err != nil {
					return err
				}
				for _, proxy := range raw.Proxy {
					proxyName, _ := proxy["name"].(string)
					matched, err := dependencyRegexMatches(patterns, proxyName)
					if err != nil {
						return err
					}
					if matched {
						explicit++
					}
				}
			}
			if explicit == 0 && providers == 0 {
				explicit = 1
			}
		}
		if explicit > 0 {
			return webAPIError(409, "duplicate_name", "proxy group and its compatible provider would share a declared provider identity")
		}
	}
	return nil
}
