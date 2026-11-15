//go:build webpanel && !android

package core

import "sort"

func RefreshWebAPIProfileCatalogAfterRepair(tx *WebAPIWriteTx, id WebAPIID) error {
	budget := NewWebAPICatalogReadBudget(webAPICatalogScopedReadBytes)
	profile, err := tx.CatalogProfile(id, budget)
	if err != nil {
		return err
	}
	if !webAPIContains([]string{"custom", "file", "url"}, profile.Type) {
		return webAPIError(400, "invalid_profile_type", "catalog owner has an unknown profile type")
	}
	if profile.ExtendType != "" && !webAPIContains([]string{"standard", "script"}, profile.ExtendType) {
		return webAPIError(400, "invalid_extend_type", "catalog owner has an unknown extension type")
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return webAPIError(409, "unreadable_overrides", "explicitly repair the owner's unreadable overrides before refreshing its catalog")
	}
	candidate, err := webAPIReadProfileCandidate(tx.WebAPIReadTx, id, profile.WebAPIProfileInput, budget)
	if err != nil {
		return err
	}
	catalog := candidate.catalog
	sort.Strings(catalog.OutboundNames)
	sort.Strings(catalog.RuleProviderNames)
	sort.Strings(catalog.SubRuleNames)
	return tx.SetProfileCatalog(id, catalog)
}
