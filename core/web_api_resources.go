//go:build webpanel && !android

package core

import (
	"encoding/json"
	"errors"
	"github.com/dlclark/regexp2"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (tx *WebAPIReadTx) Profile(id WebAPIID) (WebAPIProfile, error) {
	var v WebAPIProfile
	err := webAPIGet(tx.tx, "profiles", string(id), &v)
	return v, err
}

func (tx *WebAPIReadTx) Profiles() ([]WebAPIProfile, error) {
	items, err := webAPIList[WebAPIProfile](tx.tx, "profiles")
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Order != nil && b.Order != nil && *a.Order != *b.Order {
			return *a.Order < *b.Order
		}
		if (a.Order == nil) != (b.Order == nil) {
			return a.Order != nil
		}
		return webAPIIDLess(a.ID, b.ID)
	})
	return items, err
}

func webAPIIDLess(a, b WebAPIID) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (tx *WebAPIReadTx) Rules() ([]WebAPIRule, error) {
	items, err := webAPIList[WebAPIRule](tx.tx, "rules")
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Order != nil && b.Order != nil && *a.Order != *b.Order {
			return *a.Order < *b.Order
		}
		if (a.Order == nil) != (b.Order == nil) {
			return a.Order != nil
		}
		return webAPIIDLess(a.ID, b.ID)
	})
	return items, err
}

func (tx *WebAPIReadTx) Rule(id WebAPIID) (WebAPIRule, error) {
	var v WebAPIRule
	err := webAPIGet(tx.tx, "rules", string(id), &v)
	return v, err
}

func (tx *WebAPIReadTx) DisabledRules() ([]WebAPIDisabledRule, error) {
	return webAPIList[WebAPIDisabledRule](tx.tx, "disabled-rules")
}

func (tx *WebAPIReadTx) ProfileCatalogState(id WebAPIID) (WebAPIProfileCatalog, error) {
	var v WebAPIProfileCatalog
	err := webAPIGet(tx.tx, "profile-catalogs", string(id), &v)
	return v, err
}

func (tx *WebAPIReadTx) ProfileCatalog(id WebAPIID) (WebAPIProfileCatalog, error) {
	catalog, err := tx.ProfileCatalogState(id)
	if err != nil {
		return catalog, err
	}
	if catalog.UnavailableReason != "" {
		return catalog, webAPICatalogUnavailable(id, catalog.UnavailableReason)
	}
	return catalog, nil
}
func webAPICatalogUnavailable(id WebAPIID, reason string) *WebAPIError {
	problem := webAPIError(409, "catalog_unavailable", "profile catalog is not available for routing")
	problem.Details = map[string]any{"profileId": id, "reason": reason}
	return problem
}
func (tx *WebAPIReadTx) RequireProfileCatalogAvailable(id WebAPIID) error {
	catalog, err := tx.ProfileCatalogState(id)
	if err != nil {
		var problem *WebAPIError
		if errors.As(err, &problem) && problem.Status == 404 {
			return nil
		}
		return err
	}
	if catalog.UnavailableReason != "" {
		return webAPICatalogUnavailable(id, catalog.UnavailableReason)
	}
	return nil
}
func (tx *WebAPIReadTx) requireRuleCatalogsAvailable(owner *WebAPIID) error {
	if owner != nil {
		return tx.RequireProfileCatalogAvailable(*owner)
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		if err := tx.RequireProfileCatalogAvailable(profile.ID); err != nil {
			return err
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) SetProfileCatalog(id WebAPIID, catalog WebAPIProfileCatalog) error {
	if _, err := tx.Profile(id); err != nil {
		return err
	}
	if catalog.UnavailableReason != "" {
		if !webAPIContains([]string{WebAPICatalogUnreadableOverrides, WebAPICatalogDependencyQuarantined, WebAPICatalogContentUnavailable}, catalog.UnavailableReason) {
			return webAPIError(400, "invalid_catalog_state", "unknown catalog unavailability reason")
		}
		if len(catalog.OutboundNames)+len(catalog.RuleProviderNames)+len(catalog.SubRuleNames)+len(catalog.GroupMembers)+len(catalog.IconURLs) > 0 {
			return webAPIError(409, "invalid_catalog_state", "unavailable catalog must not advertise routing entries")
		}
	}
	for _, names := range [][]string{catalog.OutboundNames, catalog.RuleProviderNames, catalog.SubRuleNames} {
		seen := map[string]bool{}
		for _, name := range names {
			if !webAPICatalogName(name) || seen[name] {
				return webAPIError(409, "invalid_catalog", "catalog names must be unique and unambiguous")
			}
			seen[name] = true
		}
	}
	for group, members := range catalog.GroupMembers {
		if !webAPIContains(catalog.OutboundNames, group) {
			return webAPIError(409, "invalid_catalog", "group name is absent from the outbound catalog")
		}
		seen := map[string]bool{}
		for _, name := range members {
			if !webAPICatalogName(name) || seen[name] {
				return webAPIError(409, "invalid_catalog", "group members must be unique names")
			}
			seen[name] = true
		}
	}
	icons := make([]string, 0, len(catalog.IconURLs))
	seenIcons := map[string]bool{}
	for _, raw := range catalog.IconURLs {
		if !webAPIProfileIconURL(raw) {
			return webAPIError(400, "invalid_catalog_icon", "catalog icon must be a bounded HTTP(S) URL")
		}
		if !seenIcons[raw] {
			icons = append(icons, raw)
			seenIcons[raw] = true
		}
	}
	catalog.IconURLs = icons
	return webAPIPut(tx.tx, "profile-catalogs", string(id), catalog)
}

func webAPIName(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, ",\r\n\x00")
}
func webAPIOwner(a, b *WebAPIID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func webAPIDialers(o *WebAPIProfileOverrides) (map[string]string, bool) {
	if o == nil || len(o.DialerProxies) == 0 {
		return map[string]string{}, true
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(o.DialerProxies, &raw) != nil || raw == nil {
		return nil, false
	}
	out := map[string]string{}
	for k, b := range raw {
		var s string
		if string(b) == "null" || json.Unmarshal(b, &s) != nil {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

func webAPIUnreadable(p WebAPIProfileInput) bool {
	_, valid := webAPIDialers(p.Overrides)
	return p.OverridesRawDocument != nil || !valid
}

func webAPINormalizeProfile(p WebAPIProfileInput) WebAPIProfileInput {
	if p.ExtendType == "" {
		p.ExtendType = "standard"
	}
	if p.SelectedMap == nil {
		p.SelectedMap = map[string]string{}
	}
	if p.UnfoldSet == nil {
		p.UnfoldSet = []string{}
	}
	if p.Overrides == nil && p.OverridesRawDocument == nil {
		p.Overrides = &WebAPIProfileOverrides{}
	}
	return p
}

func (tx *WebAPIReadTx) validateProfileFields(p WebAPIProfileInput) error {
	if p.Type != "file" && p.Type != "url" && p.Type != "custom" {
		return webAPIError(400, "invalid_profile_type", "invalid profile type")
	}
	if p.Label != "" && !webAPICatalogName(p.Label) {
		return webAPIError(400, "invalid_label", "invalid profile label")
	}
	if p.ExtendType != "standard" && p.ExtendType != "script" {
		return webAPIError(400, "invalid_extend_type", "invalid extension type")
	}
	if p.AutoUpdateDurationMillis < 0 || p.AutoUpdateDurationMillis > 9007199254740991 {
		return webAPIError(400, "invalid_duration", "auto update duration is outside the safe integer range")
	}
	if len(p.AgeSecretKey) > 4096 || !utf8.ValidString(p.AgeSecretKey) {
		return webAPIError(400, "invalid_secret", "invalid profile key")
	}
	if p.Type == "url" {
		u, err := url.Parse(p.URL)
		if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || len(p.URL) > 8192 {
			return webAPIError(400, "invalid_url", "expected a valid HTTP(S) subscription URL")
		}
	} else if p.URL != "" {
		return webAPIError(400, "invalid_url", "only URL profiles accept a subscription URL")
	}
	if p.ScriptID != nil {
		if _, err := tx.Script(*p.ScriptID); err != nil {
			return webAPIError(409, "invalid_reference", "profile script does not exist")
		}
	}
	if p.OverridesRawDocument != nil && len(*p.OverridesRawDocument) > WebAPIMaxContentBytes {
		return webAPIError(413, "overrides_too_large", "unreadable overrides exceed the resource limit")
	}
	if p.OverridesRawDocument != nil && p.Overrides != nil {
		return webAPIError(400, "invalid_overrides", "unreadable overrides cannot also contain a decoded object")
	}
	return webAPIValidateSelectionNames(p)
}

func webAPIValidateSelectionNames(p WebAPIProfileInput) error {
	if p.CurrentGroupName != nil && *p.CurrentGroupName != "" && !webAPICatalogName(*p.CurrentGroupName) {
		return webAPIError(400, "invalid_selection", "current group name is invalid")
	}
	for group, target := range p.SelectedMap {
		if !webAPICatalogName(group) || target != "" && !webAPICatalogName(target) {
			return webAPIError(400, "invalid_selection", "remembered selection has an invalid name")
		}
	}
	return nil
}

// A refresh may rename what a profile remembers; as in the native client, only a written selection is checked.
func webAPIChangedSelections(stored, input WebAPIProfileInput) WebAPIProfileInput {
	changed := map[string]string{}
	for group, target := range input.SelectedMap {
		if previous, exists := stored.SelectedMap[group]; !exists || previous != target {
			changed[group] = target
		}
	}
	input.SelectedMap = changed
	if input.CurrentGroupName != nil && stored.CurrentGroupName != nil && *input.CurrentGroupName == *stored.CurrentGroupName {
		input.CurrentGroupName = nil
	}
	return input
}

func webAPIWithoutSelections(input WebAPIProfileInput) WebAPIProfileInput {
	input.CurrentGroupName, input.SelectedMap = nil, nil
	return input
}

func (tx *WebAPIReadTx) validateProfile(p WebAPIProfileInput, id WebAPIID) error {
	if err := tx.validateProfileFields(p); err != nil {
		return err
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	for _, existing := range profiles {
		if existing.ID != id && p.Label != "" && existing.Label == p.Label {
			return webAPIError(409, "duplicate_label", "profile label is already used")
		}
	}
	if webAPIUnreadable(p) {
		return nil
	}
	if p.MatchTarget != nil && *p.MatchTarget != "" {
		if err := tx.validateTarget(&id, *p.MatchTarget); err != nil {
			return err
		}
	}
	if p.CurrentGroupName != nil && *p.CurrentGroupName != "" || len(p.SelectedMap) > 0 {
		if _, err := tx.ProfileCatalog(id); err != nil {
			return webAPIError(409, "catalog_unavailable", "profile selection requires a group catalog")
		}
	}
	seen := map[string]bool{}
	for _, name := range p.UnfoldSet {
		if !webAPICatalogName(name) || seen[name] {
			return webAPIError(400, "invalid_unfold_set", "unfoldSet contains an invalid or duplicate name")
		}
		seen[name] = true
	}
	return ValidateWebAPIProfileInput(tx, id, p)
}

func (tx *WebAPIWriteTx) CreateProfile(input WebAPIProfileInput) (WebAPIProfile, error) {
	input = webAPINormalizeProfile(input)
	id, err := tx.AllocateID()
	if err != nil {
		return WebAPIProfile{}, err
	}
	if err = tx.validateProfile(input, id); err != nil {
		return WebAPIProfile{}, err
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return WebAPIProfile{}, err
	}
	for i, p := range profiles {
		order := int32(i)
		p.Order = &order
		if err = tx.SaveProfile(p); err != nil {
			return WebAPIProfile{}, err
		}
	}
	order := int32(len(profiles))
	profile := WebAPIProfile{ID: id, WebAPIProfileInput: input, Order: &order}
	return profile, tx.insertResource("profiles", id, profile)
}

func (tx *WebAPIWriteTx) InsertProfile(profile WebAPIProfile) error {
	return tx.insertResource("profiles", profile.ID, profile)
}
func (tx *WebAPIWriteTx) SaveProfile(profile WebAPIProfile) error {
	return tx.saveResource("profiles", profile.ID, profile)
}
func (tx *WebAPIWriteTx) InsertRule(rule WebAPIRule) error {
	return tx.insertResource("rules", rule.ID, rule)
}
func (tx *WebAPIWriteTx) SaveRule(rule WebAPIRule) error {
	return tx.saveResource("rules", rule.ID, rule)
}
func (tx *WebAPIWriteTx) SaveDisabledRule(rule WebAPIDisabledRule) error {
	return webAPIPut(tx.tx, "disabled-rules", string(rule.ProfileID)+":"+string(rule.RuleID), rule)
}

func (tx *WebAPIWriteTx) UpdateProfile(id WebAPIID, input WebAPIProfileInput) (WebAPIProfile, error) {
	profile, err := tx.Profile(id)
	if err != nil {
		return profile, err
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return profile, webAPIError(409, "unreadable_overrides", "reset unreadable dialer overrides explicitly before editing")
	}
	if input.UnfoldSet == nil {
		input.UnfoldSet = profile.UnfoldSet
	}
	input = webAPINormalizeProfile(input)
	if profile.Type != input.Type {
		return profile, webAPIError(409, "immutable_profile_type", "profile type is immutable")
	}
	if profile.Label != input.Label {
		if profile, err = tx.RenameProfile(id, input.Label); err != nil {
			return profile, err
		}
	}
	if err = webAPIValidateSelectionNames(input); err != nil {
		return profile, err
	}
	if err = tx.validateProfile(webAPIChangedSelections(profile.WebAPIProfileInput, input), id); err != nil {
		return profile, err
	}
	profile.WebAPIProfileInput = input
	return profile, tx.SaveProfile(profile)
}

func (tx *WebAPIWriteTx) ResetProfileDialers(id WebAPIID) (WebAPIProfile, error) {
	profile, err := tx.CatalogProfile(id, NewWebAPICatalogReadBudget(128<<20))
	if err != nil {
		return profile, err
	}
	if profile.OverridesRawDocument != nil {
		profile.OverridesRawDocument = nil
		profile.Overrides = &WebAPIProfileOverrides{}
	}
	if profile.Overrides == nil {
		profile.Overrides = &WebAPIProfileOverrides{}
	}
	profile.Overrides.DialerProxies = json.RawMessage(`{}`)
	if err := tx.SaveProfile(profile); err != nil {
		return profile, err
	}
	if err := tx.validateProfileFields(profile.WebAPIProfileInput); err != nil {
		return profile, err
	}
	if err := RefreshWebAPIProfileCatalogAfterRepair(tx, id); err != nil {
		return profile, err
	}
	if tx.authority != nil && tx.authority.command == "profiles:reset-dialers" && tx.authority.repairTarget == id {
		tx.authority.repairedProfile = id
	}
	return profile, nil
}

func (tx *WebAPIWriteTx) RenameProfile(id WebAPIID, label string) (WebAPIProfile, error) {
	profile, err := tx.Profile(id)
	if err != nil {
		return profile, err
	}
	if label != "" && !webAPICatalogName(label) {
		return profile, webAPIError(400, "invalid_label", "invalid profile label")
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return profile, webAPIError(409, "unreadable_overrides", "reset unreadable overrides explicitly before editing")
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return profile, err
	}
	for _, p := range profiles {
		if label != "" && p.ID != id && p.Label == label {
			return profile, webAPIError(409, "duplicate_label", "profile label is already used")
		}
	}
	old := webAPIEffectiveProfileLabel(profile)
	nextProfile := profile
	nextProfile.Label = label
	providerName := webAPIEffectiveProfileLabel(nextProfile)
	if profile.Type != "custom" && old != providerName {
		groups, err := tx.ProxyGroups()
		if err != nil {
			return profile, err
		}
		for _, group := range groups {
			changed := false
			for i, name := range group.Definition.Use {
				if name == old {
					group.Definition.Use[i] = providerName
					changed = true
				}
			}
			if pos, ok := group.Definition.UsePosition[old]; ok {
				if _, exists := group.Definition.UsePosition[providerName]; exists {
					return profile, webAPIError(409, "rename_conflict", "renamed provider position would collide")
				}
				delete(group.Definition.UsePosition, old)
				group.Definition.UsePosition[providerName] = pos
				changed = true
			}
			if changed {
				if err = tx.SaveProxyGroup(group); err != nil {
					return profile, err
				}
			}
		}
		for _, p := range profiles {
			if p.ID == id || p.Type != "custom" {
				continue
			}
			if webAPIUnreadable(p.WebAPIProfileInput) {
				return profile, webAPIError(409, "unreadable_overrides", "cannot safely rename a provider through unreadable custom overrides")
			}
			if p.Overrides != nil {
				if value, ok := p.Overrides.ProxyProviders[old]; ok {
					if _, exists := p.Overrides.ProxyProviders[providerName]; exists {
						return profile, webAPIError(409, "rename_conflict", "renamed provider override would collide")
					}
					delete(p.Overrides.ProxyProviders, old)
					p.Overrides.ProxyProviders[providerName] = value
					if err = tx.SaveProfile(p); err != nil {
						return profile, err
					}
				}
			}
		}
	}
	profile.Label = label
	return profile, tx.SaveProfile(profile)
}

func (tx *WebAPIWriteTx) DeleteProfile(id WebAPIID) (WebAPIDeleted, error) {
	result := WebAPIDeleted{DeletedIDs: []WebAPIID{id}}
	profile, err := tx.Profile(id)
	if err != nil {
		return result, err
	}
	groups, err := tx.ProxyGroups()
	if err != nil {
		return result, err
	}
	if profile.Type != "custom" {
		for _, g := range groups {
			if g.ProfileID != id && webAPIContains(g.Definition.Use, webAPIEffectiveProfileLabel(profile)) {
				return result, webAPIError(409, "resource_in_use", "profile is used as a proxy provider")
			}
		}
		profiles, err := tx.Profiles()
		if err != nil {
			return result, err
		}
		for _, p := range profiles {
			if p.ID == id || p.Type != "custom" {
				continue
			}
			if webAPIUnreadable(p.WebAPIProfileInput) {
				return result, webAPIError(409, "unreadable_overrides", "cannot prove provider is unused in unreadable overrides")
			}
			if p.Overrides != nil {
				if _, ok := p.Overrides.ProxyProviders[webAPIEffectiveProfileLabel(profile)]; ok {
					return result, webAPIError(409, "resource_in_use", "profile has provider overrides")
				}
			}
		}
	}
	rules, err := tx.Rules()
	if err != nil {
		return result, err
	}
	for _, rule := range rules {
		if rule.ProfileID != nil && *rule.ProfileID == id {
			if err = tx.deleteRule(rule.ID); err != nil {
				return result, err
			}
		}
	}
	disabled, err := tx.DisabledRules()
	if err != nil {
		return result, err
	}
	for _, v := range disabled {
		if v.ProfileID == id {
			if err = tx.tx.Bucket([]byte("disabled-rules")).Delete([]byte(string(id) + ":" + string(v.RuleID))); err != nil {
				return result, err
			}
		}
	}
	for _, group := range groups {
		if group.ProfileID == id {
			if err = tx.DeleteProxyGroup(group.ID); err != nil {
				return result, err
			}
		}
	}
	dialers, err := tx.ProxyDialers()
	if err != nil {
		return result, err
	}
	for _, d := range dialers {
		if d.ProfileID == id {
			if err = tx.DeleteProxyDialer(d.ProfileID, d.ProxyID); err != nil {
				return result, err
			}
		}
	}
	if err = tx.DeleteContent(WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}); err != nil {
		return result, err
	}
	if err = tx.deleteProfileAssets(id); err != nil {
		return result, err
	}
	if err = tx.tx.Bucket([]byte("profile-catalogs")).Delete([]byte(id)); err != nil {
		return result, err
	}
	settings, err := tx.Settings()
	if err != nil {
		return result, err
	}
	if settings.CurrentProfileID != nil && *settings.CurrentProfileID == id {
		settings.CurrentProfileID = nil
		if err = tx.SaveSettings(settings); err != nil {
			return result, err
		}
	}
	return result, tx.deleteResource("profiles", id)
}

func webAPIExactIDs(ids, existing []WebAPIID) error {
	seen := map[WebAPIID]bool{}
	for _, id := range ids {
		if _, err := webAPIInteger(string(id), true); err != nil {
			return webAPIError(400, "invalid_id", "invalid resource identity")
		}
		if seen[id] {
			return webAPIError(409, "duplicate_id", "duplicate identity in command")
		}
		seen[id] = true
	}
	if len(ids) != len(existing) {
		return webAPIError(409, "incomplete_order", "reorder requires every owned resource exactly once")
	}
	for _, id := range existing {
		if !seen[id] {
			return webAPIError(409, "incomplete_order", "reorder does not match the owned resources")
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) ReorderProfiles(ids []WebAPIID) ([]WebAPIProfile, error) {
	profiles, err := tx.Profiles()
	if err != nil {
		return nil, err
	}
	existing := make([]WebAPIID, len(profiles))
	byID := map[WebAPIID]WebAPIProfile{}
	for i, p := range profiles {
		existing[i] = p.ID
		byID[p.ID] = p
	}
	if err = webAPIExactIDs(ids, existing); err != nil {
		return nil, err
	}
	for i, id := range ids {
		p := byID[id]
		order := int32(i)
		p.Order = &order
		if err = tx.SaveProfile(p); err != nil {
			return nil, err
		}
	}
	return tx.Profiles()
}

func webAPIContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (tx *WebAPIReadTx) validateTarget(owner *WebAPIID, target string) error {
	if !webAPIName(target) || strings.EqualFold(target, "MATCH") {
		return webAPIError(400, "invalid_target", "invalid routing target")
	}
	if err := tx.requireRuleCatalogsAvailable(owner); err != nil {
		return err
	}
	if webAPIContains([]string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"}, target) {
		return nil
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return err
	}
	found := false
	for _, profile := range profiles {
		if owner != nil && profile.ID != *owner {
			continue
		}
		found = true
		catalog, err := tx.ProfileCatalog(profile.ID)
		if err != nil {
			var e *WebAPIError
			if errors.As(err, &e) && e.Status == 404 {
				return webAPIError(409, "catalog_unavailable", "profile target catalog has not been prepared")
			}
			return err
		}
		if !webAPIContains(catalog.OutboundNames, target) {
			return webAPIError(409, "invalid_reference", "routing target is absent from the profile catalog")
		}
	}
	if !found {
		return webAPIError(409, "catalog_unavailable", "routing target cannot be validated without an owning profile catalog")
	}
	return nil
}

var webAPIRuleActions = strings.Fields("DOMAIN DOMAIN_SUFFIX DOMAIN_KEYWORD DOMAIN_REGEX DOMAIN_WILDCARD GEOSITE IP_CIDR IP_CIDR6 IP_SUFFIX IP_ASN GEOIP SRC_GEOIP SRC_IP_ASN SRC_IP_CIDR SRC_IP_SUFFIX DST_PORT SRC_PORT IN_PORT IN_TYPE IN_USER IN_NAME REMATCH_NAME PROCESS_PATH PROCESS_PATH_REGEX PROCESS_PATH_WILDCARD PROCESS_NAME PROCESS_NAME_REGEX PROCESS_NAME_WILDCARD UID NETWORK DSCP RULE_SET AND OR NOT SUB_RULE MATCH")

func (tx *WebAPIReadTx) validateRule(rule WebAPIRuleInput) error {
	var profile WebAPIProfile
	if rule.ProfileID != nil {
		var err error
		profile, err = tx.Profile(*rule.ProfileID)
		if err != nil {
			return webAPIError(409, "invalid_reference", "rule owner does not exist")
		}
	}
	if err := tx.requireRuleCatalogsAvailable(rule.ProfileID); err != nil {
		return err
	}
	if !webAPIContains(webAPIRuleActions, rule.RuleAction) {
		return webAPIError(400, "invalid_rule_action", "unknown rule action")
	}
	if webAPIContains([]string{"AND", "OR", "NOT", "SUB_RULE"}, rule.RuleAction) {
		return ValidateWebAPIComplexRule(tx, rule)
	}
	if rule.SubRule != nil {
		return webAPIError(400, "invalid_rule", "subRule is only valid for SUB_RULE")
	}
	if rule.RuleTarget == nil {
		return webAPIError(400, "invalid_target", "ruleTarget is required")
	}
	if err := tx.validateTarget(rule.ProfileID, *rule.RuleTarget); err != nil {
		return err
	}
	if (rule.NoResolve || rule.Src) && !webAPIContains([]string{"GEOIP", "IP_ASN", "IP_CIDR", "IP_CIDR6", "IP_SUFFIX", "RULE_SET"}, rule.RuleAction) {
		return webAPIError(400, "invalid_rule_parameters", "this rule action does not accept noResolve/src")
	}
	if rule.RuleAction == "MATCH" {
		if rule.ProfileID == nil || profile.Type != "custom" || rule.Content != nil || rule.RuleProvider != nil {
			return webAPIError(400, "invalid_rule", "MATCH is only valid in a custom profile and has no payload")
		}
		return nil
	}
	if rule.RuleAction == "RULE_SET" {
		if rule.RuleProvider == nil || !webAPIName(*rule.RuleProvider) || rule.Content != nil {
			return webAPIError(400, "invalid_rule", "RULE_SET requires ruleProvider and no content")
		}
		if rule.ProfileID == nil {
			return webAPIError(409, "invalid_reference", "RULE_SET requires a profile catalog")
		}
		if profile.Type == "custom" {
			providers, err := tx.CatalogClashProvidersByNames([]string{*rule.RuleProvider}, NewWebAPICatalogReadBudget(128<<20))
			if err != nil {
				return err
			}
			if len(providers) != 1 {
				return webAPIError(409, "invalid_reference", "rule provider is absent from app-level resources")
			}
			return nil
		}
		catalog, err := tx.ProfileCatalog(*rule.ProfileID)
		if err != nil {
			return webAPIError(409, "catalog_unavailable", "rule provider catalog is unavailable")
		}
		if !webAPIContains(catalog.RuleProviderNames, *rule.RuleProvider) {
			return webAPIError(409, "invalid_reference", "rule provider is absent from the catalog")
		}
		return nil
	}
	if rule.RuleProvider != nil {
		return webAPIError(400, "invalid_rule", "ruleProvider is only valid for RULE_SET")
	}
	if rule.Content == nil || *rule.Content == "" || len(*rule.Content) > 8192 || strings.TrimSpace(*rule.Content) != *rule.Content || strings.ContainsAny(*rule.Content, "\r\n\x00") {
		return webAPIError(400, "invalid_rule_content", "invalid rule content")
	}
	content := *rule.Content
	regex := webAPIContains([]string{"DOMAIN_REGEX", "PROCESS_NAME_REGEX", "PROCESS_PATH_REGEX"}, rule.RuleAction)
	if regex {
		if _, err := regexp2.Compile(content, regexp2.IgnoreCase); err != nil {
			return webAPIError(400, "invalid_rule_content", "invalid rule regular expression")
		}
	}
	if !regex && strings.Contains(content, ",") {
		return webAPIError(400, "invalid_rule_content", "commas are not valid for this rule payload")
	}
	switch rule.RuleAction {
	case "NETWORK":
		if content != "tcp" && content != "udp" {
			return webAPIError(400, "invalid_rule_content", "network must be tcp or udp")
		}
	case "IP_CIDR", "IP_CIDR6", "SRC_IP_CIDR":
		if _, err := netip.ParsePrefix(content); err != nil {
			return webAPIError(400, "invalid_rule_content", "expected an IP prefix")
		}
	case "DST_PORT", "SRC_PORT", "IN_PORT", "UID", "DSCP":
		maximum := uint64(65535)
		if rule.RuleAction == "UID" {
			maximum = 4294967295
		}
		if rule.RuleAction == "DSCP" {
			maximum = 63
		}
		if !webAPIRanges(content, maximum) {
			return webAPIError(400, "invalid_rule_content", "invalid numeric range")
		}
	}
	return nil
}

func webAPIRanges(s string, max uint64) bool {
	for _, item := range strings.Split(s, "/") {
		parts := strings.Split(item, "-")
		if len(parts) > 2 {
			return false
		}
		var prev uint64
		for i, part := range parts {
			n, err := strconv.ParseUint(part, 10, 64)
			if err != nil || n > max || (i > 0 && n < prev) {
				return false
			}
			prev = n
		}
	}
	return true
}

func (tx *WebAPIReadTx) OwnedRules(owner *WebAPIID) ([]WebAPIRule, error) {
	all, err := tx.Rules()
	if err != nil {
		return nil, err
	}
	out := make([]WebAPIRule, 0)
	for _, rule := range all {
		if webAPIOwner(rule.ProfileID, owner) {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (tx *WebAPIReadTx) duplicateRule(input WebAPIRuleInput, except WebAPIID) error {
	all, err := tx.OwnedRules(input.ProfileID)
	if err != nil {
		return err
	}
	for _, rule := range all {
		if rule.ID != except && reflect.DeepEqual(rule.WebAPIRuleInput, input) {
			return webAPIError(409, "duplicate_rule", "identical rule already exists for this owner")
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) CreateRule(input WebAPIRuleInput) (WebAPIRule, error) {
	if err := tx.validateRule(input); err != nil {
		return WebAPIRule{}, err
	}
	if err := tx.duplicateRule(input, ""); err != nil {
		return WebAPIRule{}, err
	}
	id, err := tx.AllocateID()
	if err != nil {
		return WebAPIRule{}, err
	}
	owned, err := tx.OwnedRules(input.ProfileID)
	if err != nil {
		return WebAPIRule{}, err
	}
	for i, r := range owned {
		rank := webAPIOrder(i)
		r.Order = &rank
		if err = tx.SaveRule(r); err != nil {
			return WebAPIRule{}, err
		}
	}
	order := webAPIOrder(len(owned))
	rule := WebAPIRule{ID: id, WebAPIRuleInput: input, Order: &order}
	return rule, tx.InsertRule(rule)
}

func (tx *WebAPIWriteTx) UpdateRule(id WebAPIID, input WebAPIRuleInput) (WebAPIRule, error) {
	rule, err := tx.Rule(id)
	if err != nil {
		return rule, err
	}
	if !webAPIOwner(rule.ProfileID, input.ProfileID) {
		return rule, webAPIError(409, "owner_conflict", "rule ownership cannot be changed")
	}
	if err = tx.validateRule(input); err != nil {
		return rule, err
	}
	if err = tx.duplicateRule(input, id); err != nil {
		return rule, err
	}
	rule.WebAPIRuleInput = input
	return rule, tx.SaveRule(rule)
}

func (tx *WebAPIWriteTx) deleteRule(id WebAPIID) error {
	if err := tx.deleteResource("rules", id); err != nil {
		return err
	}
	disabled, err := tx.DisabledRules()
	if err != nil {
		return err
	}
	for _, v := range disabled {
		if v.RuleID == id {
			if err = tx.tx.Bucket([]byte("disabled-rules")).Delete([]byte(string(v.ProfileID) + ":" + string(id))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) DeleteRules(ids []WebAPIID) (WebAPIDeleted, error) {
	result := WebAPIDeleted{DeletedIDs: append([]WebAPIID{}, ids...)}
	if len(ids) == 0 {
		return result, webAPIError(400, "empty_delete", "at least one rule is required")
	}
	seen := map[WebAPIID]bool{}
	for _, id := range ids {
		if seen[id] {
			return result, webAPIError(409, "duplicate_id", "duplicate identity in command")
		}
		seen[id] = true
		if _, err := tx.Rule(id); err != nil {
			return result, err
		}
	}
	for _, id := range ids {
		if err := tx.deleteRule(id); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (tx *WebAPIWriteTx) ReorderRules(owner *WebAPIID, ids []WebAPIID) ([]WebAPIRule, error) {
	if owner != nil {
		if _, err := tx.Profile(*owner); err != nil {
			return nil, err
		}
	}
	owned, err := tx.OwnedRules(owner)
	if err != nil {
		return nil, err
	}
	existing := make([]WebAPIID, len(owned))
	byID := map[WebAPIID]WebAPIRule{}
	for i, r := range owned {
		existing[i] = r.ID
		byID[r.ID] = r
	}
	if err = webAPIExactIDs(ids, existing); err != nil {
		return nil, err
	}
	for i, id := range ids {
		r := byID[id]
		order := webAPIOrder(i)
		r.Order = &order
		if err = tx.SaveRule(r); err != nil {
			return nil, err
		}
	}
	return tx.OwnedRules(owner)
}

func (tx *WebAPIWriteTx) ReplaceRules(owner *WebAPIID, items []WebAPIRuleReplacement) ([]WebAPIRule, error) {
	if owner != nil {
		if _, err := tx.Profile(*owner); err != nil {
			return nil, err
		}
	}
	seen := map[WebAPIID]bool{}
	seenRules := map[string]bool{}
	for _, v := range items {
		encoded, err := json.Marshal(v.WebAPIRuleInput)
		if err != nil {
			return nil, err
		}
		if seenRules[string(encoded)] {
			return nil, webAPIError(409, "duplicate_rule", "identical replacement rules are not allowed")
		}
		seenRules[string(encoded)] = true
		if !webAPIOwner(v.ProfileID, owner) {
			return nil, webAPIError(409, "owner_conflict", "replacement rule belongs to a different owner")
		}
		if err := tx.validateRule(v.WebAPIRuleInput); err != nil {
			return nil, err
		}
		if v.ID != nil {
			if seen[*v.ID] {
				return nil, webAPIError(409, "duplicate_id", "duplicate replacement identity")
			}
			seen[*v.ID] = true
			rule, err := tx.Rule(*v.ID)
			if err != nil {
				return nil, err
			}
			if !webAPIOwner(rule.ProfileID, owner) {
				return nil, webAPIError(409, "owner_conflict", "replacement identity belongs to a different owner")
			}
		}
	}
	owned, err := tx.OwnedRules(owner)
	if err != nil {
		return nil, err
	}
	for _, rule := range owned {
		if seen[rule.ID] {
			err = tx.tx.Bucket([]byte("rules")).Delete([]byte(rule.ID))
		} else {
			err = tx.deleteRule(rule.ID)
		}
		if err != nil {
			return nil, err
		}
	}
	for i, v := range items {
		var id WebAPIID
		if v.ID != nil {
			id = *v.ID
		} else {
			id, err = tx.AllocateID()
			if err != nil {
				return nil, err
			}
		}
		order := webAPIOrder(i)
		if err = tx.InsertRule(WebAPIRule{ID: id, WebAPIRuleInput: v.WebAPIRuleInput, Order: &order}); err != nil {
			return nil, err
		}
	}
	return tx.OwnedRules(owner)
}

func (tx *WebAPIReadTx) DisabledSelection(owner WebAPIID) (WebAPIDisabledSelection, error) {
	result := WebAPIDisabledSelection{ProfileID: owner, RuleIDs: []WebAPIID{}}
	if _, err := tx.Profile(owner); err != nil {
		return result, err
	}
	items, err := tx.DisabledRules()
	if err != nil {
		return result, err
	}
	for _, v := range items {
		if v.ProfileID == owner {
			result.RuleIDs = append(result.RuleIDs, v.RuleID)
		}
	}
	sort.Slice(result.RuleIDs, func(i, j int) bool { return webAPIIDLess(result.RuleIDs[i], result.RuleIDs[j]) })
	return result, nil
}

func (tx *WebAPIWriteTx) ReplaceDisabledRules(owner WebAPIID, ids []WebAPIID) (WebAPIDisabledSelection, error) {
	result := WebAPIDisabledSelection{ProfileID: owner, RuleIDs: []WebAPIID{}}
	if _, err := tx.Profile(owner); err != nil {
		return result, err
	}
	seen := map[WebAPIID]bool{}
	for _, id := range ids {
		if seen[id] {
			return result, webAPIError(409, "duplicate_id", "duplicate disabled rule")
		}
		seen[id] = true
		rule, err := tx.Rule(id)
		if err != nil {
			return result, webAPIError(409, "invalid_reference", "disabled rule does not exist")
		}
		if rule.ProfileID != nil {
			return result, webAPIError(409, "owner_conflict", "only global rules can be disabled")
		}
	}
	items, err := tx.DisabledRules()
	if err != nil {
		return result, err
	}
	for _, v := range items {
		if v.ProfileID == owner {
			if err = tx.tx.Bucket([]byte("disabled-rules")).Delete([]byte(string(owner) + ":" + string(v.RuleID))); err != nil {
				return result, err
			}
		}
	}
	for _, id := range ids {
		if err = tx.SaveDisabledRule(WebAPIDisabledRule{ProfileID: owner, RuleID: id}); err != nil {
			return result, err
		}
	}
	return tx.DisabledSelection(owner)
}
