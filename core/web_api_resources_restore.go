//go:build webpanel && !android

package core

import "errors"

func (tx *WebAPIReadTx) ValidateRuleForRestore(rule WebAPIRuleInput) error {
	if err := webAPIValidateStoredRuleSyntax(rule); err != nil {
		return err
	}
	var owners []WebAPIProfile
	if rule.ProfileID != nil {
		profile, err := tx.Profile(*rule.ProfileID)
		if err != nil {
			return webAPIError(409, "invalid_reference", "restored rule owner does not exist")
		}
		owners = []WebAPIProfile{profile}
	} else {
		var err error
		owners, err = tx.Profiles()
		if err != nil {
			return err
		}
	}
	if rule.RuleAction == "MATCH" && (rule.ProfileID == nil || owners[0].Type != "custom") {
		return webAPIError(400, "invalid_rule", "MATCH belongs to a custom profile")
	}
	providers := map[string]bool{}
	if rule.RuleAction == "RULE_SET" {
		providers[*rule.RuleProvider] = true
	}
	if webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
		syntax, err := webAPIParseComplexRule(rule)
		if err != nil {
			return err
		}
		providers = syntax.providers
	}
	for _, owner := range owners {
		catalog, err := tx.ProfileCatalogState(owner.ID)
		known := err == nil && catalog.UnavailableReason == ""
		if err != nil {
			var problem *WebAPIError
			if !errors.As(err, &problem) || problem.Status != 404 {
				return err
			}
		}
		if owner.Type == "custom" {
			available, err := tx.ClashProviders()
			if err != nil {
				return err
			}
			names := map[string]bool{}
			for _, provider := range available {
				names[provider.Label] = true
			}
			for name := range providers {
				if !names[name] {
					return webAPIError(409, "invalid_reference", "restored rule provider is absent from custom profile resources")
				}
			}
			if rule.RuleAction == "SUB_RULE" {
				return webAPIError(409, "invalid_reference", "custom profile has no sub-rule source")
			}
		}
		if !known {
			continue
		}
		if rule.RuleAction == "SUB_RULE" {
			if !webAPIContains(catalog.SubRuleNames, *rule.SubRule) {
				return webAPIError(409, "invalid_reference", "restored sub-rule is absent from the owning source")
			}
		} else if !webAPIContains([]string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL"}, *rule.RuleTarget) && !webAPIContains(catalog.OutboundNames, *rule.RuleTarget) {
			return webAPIError(409, "invalid_reference", "restored routing target is absent from the known catalog")
		}
		for name := range providers {
			if owner.Type != "custom" && !webAPIContains(catalog.RuleProviderNames, name) {
				return webAPIError(409, "invalid_reference", "restored rule provider is absent from the known catalog")
			}
		}
	}
	return nil
}
