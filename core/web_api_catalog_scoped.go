//go:build webpanel && !android

package core

import (
	"errors"
	"sort"
)

const webAPICatalogScopedReadBytes int64 = 128 << 20

type webAPICatalogCandidateRows struct {
	groups           []WebAPIProxyGroup
	rules            []WebAPIRule
	dialers          []WebAPIProxyDialer
	proxies          []WebAPICustomProxy
	providerProfiles []WebAPIProfile
	ruleProviders    []WebAPIClashProvider
	providerContents map[WebAPIID]WebAPIContent
	providerStates   map[WebAPIID]WebAPIProfileCatalog
}

func webAPICatalogSortedNames(names map[string]bool) []string {
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func webAPICatalogRuleProviderReferences(profile WebAPIProfile, rules []WebAPIRule) ([]string, error) {
	names := map[string]bool{}
	for _, rule := range rules {
		if err := webAPIValidateStoredRuleSyntax(rule.WebAPIRuleInput); err != nil {
			return nil, err
		}
		if rule.RuleAction == "RULE_SET" && rule.RuleProvider != nil {
			names[*rule.RuleProvider] = true
		}
		if webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
			syntax, err := webAPIParseComplexRule(rule.WebAPIRuleInput)
			if err != nil {
				return nil, err
			}
			for name := range syntax.providers {
				names[name] = true
			}
		}
	}
	if profile.Overrides == nil {
		return webAPICatalogSortedNames(names), nil
	}
	if dns := profile.Overrides.DNS; dns != nil {
		for _, policy := range []map[string]string{dns.NameserverPolicy, dns.ProxyServerNameserverPolicy} {
			for key := range policy {
				for _, name := range webAPIRuleSetEntryNames(key, true) {
					names[name] = true
				}
			}
		}
		for _, entry := range dns.FakeIPFilter {
			for _, name := range webAPIRuleSetEntryNames(entry, false) {
				names[name] = true
			}
			for _, name := range webAPIRuleSetTextNames(entry) {
				names[name] = true
			}
		}
	}
	if sniffer := profile.Overrides.Sniffer; sniffer != nil {
		for _, list := range [][]string{sniffer.ForceDomain, sniffer.SkipDomain, sniffer.SkipSrcAddress, sniffer.SkipDstAddress} {
			for _, entry := range list {
				for _, name := range webAPIRuleSetEntryNames(entry, false) {
					names[name] = true
				}
			}
		}
	}
	return webAPICatalogSortedNames(names), nil
}

func webAPICatalogReadCandidateRows(tx *WebAPIReadTx, profile WebAPIProfile, budget *WebAPICatalogReadBudget) (webAPICatalogCandidateRows, error) {
	rows := webAPICatalogCandidateRows{providerContents: map[WebAPIID]WebAPIContent{}, providerStates: map[WebAPIID]WebAPIProfileCatalog{}}
	var err error
	if _, err = tx.CatalogProfile(profile.ID, budget); err != nil {
		var problem *WebAPIError
		if !errors.As(err, &problem) || problem.Status != 404 {
			return rows, err
		}
	}
	rows.groups, err = tx.CatalogProxyGroups(profile.ID, budget)
	if err != nil {
		return rows, err
	}
	rows.rules, err = tx.CatalogRules(profile.ID, budget)
	if err != nil {
		return rows, err
	}
	rows.dialers, err = tx.CatalogProxyDialers(profile.ID, budget)
	if err != nil {
		return rows, err
	}
	groupNames, proxyNames := map[string]bool{}, map[string]bool{}
	usedProviders, providerNames := map[string]bool{}, map[string]bool{}
	for _, group := range rows.groups {
		if group.ProfileID != profile.ID {
			return rows, webAPIError(409, "owner_conflict", "catalog group belongs to a different profile")
		}
		if groupNames[group.Definition.Name] {
			return rows, webAPIError(409, "duplicate_name", "proxy group name is duplicated in its owner")
		}
		groupNames[group.Definition.Name] = true
		for _, name := range group.Definition.Proxies {
			proxyNames[name] = true
		}
		for _, name := range group.Definition.Use {
			usedProviders[name], providerNames[name] = true, true
		}
	}
	for name := range groupNames {
		proxyNames[name] = true
	}
	for _, builtin := range []string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL"} {
		if !groupNames[builtin] {
			delete(proxyNames, builtin)
		}
	}
	if profile.Overrides != nil {
		for name := range profile.Overrides.ProxyProviders {
			providerNames[name] = true
		}
	}
	for _, rule := range rows.rules {
		if rule.ProfileID == nil || *rule.ProfileID != profile.ID {
			return rows, webAPIError(409, "owner_conflict", "catalog rule belongs to a different profile")
		}
	}
	rows.proxies, err = tx.CatalogCustomProxiesByNames(webAPICatalogSortedNames(proxyNames), budget)
	if err != nil {
		return rows, err
	}
	byID := map[WebAPIID]bool{}
	for _, proxy := range rows.proxies {
		if groupNames[webAPIProxyString(proxy.Definition, "name")] {
			return rows, webAPIError(409, "duplicate_name", "proxy and group share one outbound identity")
		}
		byID[proxy.ID] = true
	}
	for _, dialer := range rows.dialers {
		if dialer.ProfileID != profile.ID {
			return rows, webAPIError(409, "owner_conflict", "catalog dialer belongs to a different profile")
		}
		if byID[dialer.ProxyID] {
			continue
		}
		proxy, e := tx.CatalogCustomProxy(dialer.ProxyID, budget)
		if e != nil {
			var problem *WebAPIError
			if errors.As(e, &problem) && problem.Status == 404 {
				return rows, webAPIError(409, "invalid_reference", "catalog dialer source does not exist")
			}
			return rows, e
		}
		rows.proxies = append(rows.proxies, proxy)
		byID[proxy.ID] = true
	}
	for _, proxy := range rows.proxies {
		if err := webAPIValidateProxy(proxy.Definition, proxy.EmptyOptions); err != nil {
			return rows, err
		}
	}
	rows.providerProfiles, err = tx.CatalogProviderProfilesByNames(webAPICatalogSortedNames(providerNames), budget)
	if err != nil {
		return rows, err
	}
	for _, provider := range rows.providerProfiles {
		name := webAPIEffectiveProfileLabel(provider)
		delete(providerNames, name)
		if !usedProviders[name] {
			continue
		}
		state, e := tx.CatalogProfileCatalogState(provider.ID, budget)
		if e == nil {
			rows.providerStates[provider.ID] = state
		} else {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return rows, e
			}
		}
		content, e := tx.CatalogContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: provider.ID}, budget)
		if e == nil {
			rows.providerContents[provider.ID] = content
		} else {
			var problem *WebAPIError
			if !errors.As(e, &problem) || problem.Status != 404 {
				return rows, e
			}
		}
	}
	if len(providerNames) > 0 {
		return rows, webAPIError(409, "invalid_reference", "catalog proxy provider profile does not exist")
	}
	ruleNames, err := webAPICatalogRuleProviderReferences(profile, rows.rules)
	if err != nil {
		return rows, err
	}
	rows.ruleProviders, err = tx.CatalogClashProvidersByNames(ruleNames, budget)
	if err != nil {
		return rows, err
	}
	present := map[string]bool{}
	for _, provider := range rows.ruleProviders {
		if !webAPICatalogName(provider.Label) || !webAPIContains([]string{"domain", "ipcidr", "classical"}, provider.Behavior) || !webAPIContains([]string{"yaml", "text", "mrs"}, provider.Format) {
			return rows, webAPIError(400, "invalid_provider", "referenced rule provider metadata is invalid")
		}
		present[provider.Label] = true
	}
	for _, name := range ruleNames {
		if !present[name] {
			return rows, webAPIError(409, "invalid_reference", "catalog rule provider does not exist")
		}
	}
	return rows, nil
}
