//go:build webpanel && !android

package core

import (
	"errors"
	"strings"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/rules"
	RC "github.com/metacubex/mihomo/rules/common"
	"github.com/metacubex/mihomo/rules/logic"
)

type webAPIRuleSyntax struct {
	providers    map[string]bool
	count, depth int
}

func (state *webAPIRuleSyntax) parse(kind, payload, target string, params []string, subRules map[string][]C.Rule) (C.Rule, error) {
	state.count++
	state.depth++
	defer func() { state.depth-- }()
	if state.count > 2048 || state.depth > 64 {
		return nil, errors.New("nested rule exceeds validation limits")
	}
	if kind != "MATCH" && payload == "" {
		return nil, errors.New("rule payload is required")
	}
	for _, parameter := range params {
		if !webAPIContains([]string{"src", "no-resolve"}, parameter) || !webAPIContains([]string{"GEOIP", "IP-ASN", "IP-CIDR", "IP-CIDR6", "IP-SUFFIX", "RULE-SET"}, kind) {
			return nil, errors.New("invalid nested rule parameter")
		}
	}
	if webAPIContains([]string{"DST-PORT", "SRC-PORT", "IN-PORT", "UID", "DSCP"}, kind) {
		maximum := uint64(65535)
		if kind == "UID" {
			maximum = 4294967295
		}
		if kind == "DSCP" {
			maximum = 63
		}
		if !webAPIRanges(payload, maximum) {
			return nil, errors.New("invalid nested numeric range")
		}
	}
	switch kind {
	case "AND":
		return logic.NewAND(payload, target, state.parse)
	case "OR":
		return logic.NewOR(payload, target, state.parse)
	case "NOT":
		return logic.NewNOT(payload, target, state.parse)
	case "SUB-RULE":
		return logic.NewSubRule(payload, target, subRules, state.parse)
	case "RULE-SET":
		if !webAPIName(payload) || strings.ContainsAny(payload, "()") {
			return nil, errors.New("invalid rule provider name")
		}
		state.providers[payload] = true
	case "GEOSITE", "GEOIP", "SRC-GEOIP":
		if !webAPICatalogName(payload) || strings.ContainsAny(payload, ",()") {
			return nil, errors.New("invalid geographic rule payload")
		}
		// Geographic constructors open/download host datasets; only grammar belongs to this transaction.
		return RC.NewMatch(target), nil
	}
	return rules.ParseRule(kind, payload, target, params, subRules)
}

func webAPIParseComplexRule(input WebAPIRuleInput) (*webAPIRuleSyntax, error) {
	state := &webAPIRuleSyntax{providers: map[string]bool{}}
	if !webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, input.RuleAction) {
		return nil, webAPIError(400, "invalid_rule_action", "expected a complex rule action")
	}
	if input.Content == nil || *input.Content == "" || len(*input.Content) > 65536 || strings.TrimSpace(*input.Content) != *input.Content || strings.ContainsAny(*input.Content, "\x00\r\n") || input.NoResolve || input.Src || input.RuleProvider != nil {
		return nil, webAPIError(400, "invalid_rule", "complex rule has invalid payload or parameters")
	}
	depth := 0
	for _, character := range *input.Content {
		if character == '(' {
			depth++
			if depth > 128 {
				return nil, webAPIError(400, "invalid_rule", "nested rule exceeds validation limits")
			}
		} else if character == ')' {
			depth--
			if depth < 0 {
				return nil, webAPIError(400, "invalid_rule", "unbalanced rule payload")
			}
		}
	}
	if depth != 0 {
		return nil, webAPIError(400, "invalid_rule", "unbalanced rule payload")
	}
	var target string
	if input.RuleAction == "SUB_RULE" {
		if input.SubRule == nil || !webAPIName(*input.SubRule) || input.RuleTarget != nil {
			return nil, webAPIError(400, "invalid_rule", "SUB_RULE requires subRule and no ruleTarget")
		}
		target = *input.SubRule
	} else {
		if input.RuleTarget == nil || !webAPIName(*input.RuleTarget) || input.SubRule != nil || strings.EqualFold(*input.RuleTarget, "MATCH") {
			return nil, webAPIError(400, "invalid_rule", "complex rule requires a routing target")
		}
		target = *input.RuleTarget
	}
	line := webAPIRuleLine(WebAPIRule{WebAPIRuleInput: input})
	kind, payload, parsedTarget, parameters := RC.ParseRulePayload(line, true)
	if parsedTarget != target || len(parameters) != 0 {
		return nil, webAPIError(400, "invalid_rule", "rule fields cannot round-trip through the Core grammar")
	}
	if _, err := state.parse(kind, payload, target, parameters, map[string][]C.Rule{}); err != nil {
		return nil, webAPIError(400, "invalid_rule", "nested rule syntax or leaf payload is invalid")
	}
	return state, nil
}

func ValidateWebAPIComplexRule(tx *WebAPIReadTx, input WebAPIRuleInput) error {
	state, err := webAPIParseComplexRule(input)
	if err != nil {
		return err
	}
	if input.RuleAction != "SUB_RULE" {
		if err := tx.validateTarget(input.ProfileID, *input.RuleTarget); err != nil {
			return err
		}
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	matched := false
	for _, profile := range profiles {
		if input.ProfileID != nil && profile.ID != *input.ProfileID {
			continue
		}
		matched = true
		var catalog WebAPIProfileCatalog
		if profile.Type == "custom" {
			catalog.RuleProviderNames = []string{}
			providers, err := tx.ClashProviders()
			if err != nil {
				return err
			}
			for _, provider := range providers {
				catalog.RuleProviderNames = append(catalog.RuleProviderNames, provider.Label)
			}
		} else {
			catalog, err = tx.ProfileCatalog(profile.ID)
			if err != nil && (len(state.providers) > 0 || input.RuleAction == "SUB_RULE") {
				return webAPIError(409, "catalog_unavailable", "profile provider/sub-rule catalog is not prepared")
			}
		}
		if input.RuleAction == "SUB_RULE" && !webAPIContains(catalog.SubRuleNames, *input.SubRule) {
			return webAPIError(409, "invalid_reference", "sub-rule is absent from the owning profile")
		}
		for name := range state.providers {
			if !webAPIContains(catalog.RuleProviderNames, name) {
				return webAPIError(409, "invalid_reference", "nested rule provider is absent from the owning profile")
			}
		}
	}
	if !matched && (input.ProfileID != nil || len(state.providers) > 0 || input.RuleAction == "SUB_RULE") {
		return webAPIError(409, "invalid_reference", "complex rule owner or provider context does not exist")
	}
	return nil
}

func webAPIValidateStoredRuleSyntax(input WebAPIRuleInput) error {
	if !webAPIContains(webAPIRuleActions, input.RuleAction) {
		return webAPIError(400, "invalid_rule_action", "unknown stored rule action")
	}
	if webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, input.RuleAction) {
		_, err := webAPIParseComplexRule(input)
		return err
	}
	if input.RuleTarget == nil || !webAPIName(*input.RuleTarget) || strings.EqualFold(*input.RuleTarget, "MATCH") || input.SubRule != nil {
		return webAPIError(400, "invalid_target", "stored routing target cannot round-trip through rule text")
	}
	if input.RuleAction == "RULE_SET" {
		if input.RuleProvider == nil || !webAPIName(*input.RuleProvider) || input.Content != nil {
			return webAPIError(400, "invalid_rule", "stored rule provider cannot round-trip through rule text")
		}
	} else if input.RuleProvider != nil {
		return webAPIError(400, "invalid_rule", "ruleProvider is only valid for RULE_SET")
	}
	if input.RuleAction == "MATCH" {
		if input.Content != nil || input.NoResolve || input.Src {
			return webAPIError(400, "invalid_rule", "MATCH has no content or parameters")
		}
		return nil
	}
	if input.RuleAction != "RULE_SET" && (input.Content == nil || *input.Content == "" || strings.ContainsAny(*input.Content, "\x00\r\n")) {
		return webAPIError(400, "invalid_rule", "stored rule content is required")
	}
	kind, payload, target, params := RC.ParseRulePayload(webAPIRuleLine(WebAPIRule{WebAPIRuleInput: input}), true)
	if target != *input.RuleTarget {
		return webAPIError(400, "invalid_rule", "stored rule fields cannot round-trip through Core grammar")
	}
	syntax := &webAPIRuleSyntax{providers: map[string]bool{}}
	if _, err := syntax.parse(kind, payload, target, params, nil); err != nil {
		return webAPIError(400, "invalid_rule", "stored rule syntax or leaf payload is invalid")
	}
	return nil
}
