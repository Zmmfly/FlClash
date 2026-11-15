//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"time"
)

type WebAPICustomImportedProvider struct {
	Label           string `json:"label"`
	URL             string `json:"url"`
	AgeSecretKey    string `json:"ageSecretKey"`
	Content         string `json:"content"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}

type WebAPICustomProfileImportDTO struct {
	ProxyProviders []WebAPICustomImportedProvider `json:"proxyProviders"`
	RuleProviders  []WebAPICustomImportedProvider `json:"ruleProviders"`
	Proxies        []WebAPIProxyDefinition        `json:"proxies"`
	Dialers        map[string]string              `json:"dialers"`
	ProxyGroups    []WebAPIProxyGroupInput        `json:"proxyGroups"`
	Rules          []WebAPIRuleInput              `json:"rules"`
	Overrides      *WebAPIProfileOverrides        `json:"overrides"`
}

type WebAPICustomProfileImportRequest struct {
	WebAPIMutation
	WebAPIContentJobSelection
	ID     WebAPIID                     `json:"id"`
	Custom WebAPICustomProfileImportDTO `json:"custom"`
}

type WebAPICustomProfileImportResult struct {
	Profile              WebAPIProfile `json:"profile"`
	AddedProfileIDs      []WebAPIID    `json:"addedProfileIds"`
	AddedRuleProviderIDs []WebAPIID    `json:"addedRuleProviderIds"`
	AddedProxyIDs        []WebAPIID    `json:"addedProxyIds"`
	GroupIDs             []WebAPIID    `json:"groupIds"`
	RuleIDs              []WebAPIID    `json:"ruleIds"`
}

type webContentCustomProvider struct {
	source   WebAPICustomImportedProvider
	content  []byte
	catalog  *WebAPIProfileCatalog
	ruleInfo *RuleSetInfo
}

func (s *WebAPIContentService) prepareCustomProviders(ctx context.Context, sources []WebAPICustomImportedProvider, profiles bool) ([]webContentCustomProvider, error) {
	if prepared, ok, err := webCustomSourceBoundProviders(ctx, sources, profiles); ok || err != nil {
		return prepared, err
	}
	prepared := make([]webContentCustomProvider, 0, len(sources))
	var total int
	for _, source := range sources {
		if !webAPICatalogName(source.Label) {
			return nil, webAPIError(400, "invalid_label", "imported provider needs an unambiguous label")
		}
		data := []byte(source.Content)
		if source.URL != "" {
			if len(data) != 0 {
				return nil, webAPIError(400, "invalid_import_source", "remote provider cannot also supply inline content")
			}
			if source.AuthorizationID != "" {
				return nil, webAPIError(409, "source_import_job_required", "custom import dependencies require a prepared import job")
			}
			purpose := WebAPISourceRuleProviderImport
			if profiles {
				purpose = WebAPISourceProfileImport
			}
			fetched, err := webFetchPurposeSource(ctx, WebAPISourceRequest{Purpose: purpose, URL: source.URL}, nil, s.sourceOptions)
			if err != nil {
				return nil, err
			}
			data = fetched.Bytes
		}
		if len(data) > WebAPIMaxContentBytes {
			return nil, webAPIError(413, "content_too_large", "provider source exceeds 32 MiB")
		}
		total += len(data)
		if total > WebAPIBackupExpandedLimit {
			return nil, webAPIError(413, "content_too_large", "custom import sources exceed the aggregate limit")
		}
		item := webContentCustomProvider{source: source, content: data}
		if profiles {
			_, catalog, err := webContentProfile(data, source.AgeSecretKey)
			if err != nil {
				return nil, err
			}
			item.catalog = &catalog
		} else {
			if source.AgeSecretKey != "" {
				return nil, webAPIError(400, "invalid_import_source", "rule providers do not accept profile age identities")
			}
			info, _, err := webContentRuleSet(data, true)
			if err != nil {
				return nil, err
			}
			item.ruleInfo = info
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func (s *WebAPIContentService) ImportCustom(ctx context.Context, request WebAPICustomProfileImportRequest) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "profiles:import-custom", request); done || err != nil {
		return receipt, err
	}
	effective := request
	finish := func(bool) {}
	if request.PreparedJobID != "" {
		if s.CustomSources == nil {
			return WebAPIReceipt{}, webAPIError(503, "custom_import_unavailable", "custom source preparations are unavailable")
		}
		var err error
		ctx, effective.Custom, finish, err = s.CustomSources.Bind(ctx, request, request.WebAPIContentJobSelection)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		defer finish(false)
	} else if request.RequestDigest != "" || len(request.AuthorizationIDs) > 0 {
		return WebAPIReceipt{}, webAPIError(400, "invalid_content_preparation", "custom source grants require an immutable preparation")
	}
	if len(effective.Custom.ProxyProviders)+len(effective.Custom.RuleProviders) > 1000 || len(effective.Custom.Proxies)+len(effective.Custom.ProxyGroups)+len(effective.Custom.Rules) > 100000 {
		return WebAPIReceipt{}, webAPIError(413, "import_complexity_limit", "custom import has too many resources")
	}
	_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		profile, err := tx.Profile(request.ID)
		if err != nil {
			return err
		}
		if profile.Type != "custom" {
			return webAPIError(409, "invalid_profile_type", "custom import requires a custom profile")
		}
		if webAPIUnreadable(profile.WebAPIProfileInput) {
			return webAPIError(409, "unreadable_overrides", "repair unreadable overrides before replacing the custom graph")
		}
		return nil
	})
	if err != nil {
		return WebAPIReceipt{}, err
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	profiles, err := s.prepareCustomProviders(ctx, effective.Custom.ProxyProviders, true)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	providers, err := s.prepareCustomProviders(ctx, effective.Custom.RuleProviders, false)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	for _, definition := range effective.Custom.Proxies {
		if value, ok := definition["dialer-proxy"]; ok && string(value) != `""` {
			return WebAPIReceipt{}, webAPIError(400, "profile_dialers_required", "custom proxy dialers belong to the imported profile")
		}
		if err := webAPIValidateProxy(definition, nil); err != nil {
			return WebAPIReceipt{}, err
		}
	}
	for _, group := range effective.Custom.ProxyGroups {
		if err := webAPIValidateGroup(group.Definition); err != nil {
			return WebAPIReceipt{}, err
		}
	}
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, "profiles:import-custom", request, func(tx *WebAPIWriteTx) (any, error) {
		if err := webCustomSourceValidateCommit(ctx, tx.WebAPIReadTx); err != nil {
			return nil, err
		}
		result := WebAPICustomProfileImportResult{AddedProfileIDs: []WebAPIID{}, AddedRuleProviderIDs: []WebAPIID{}, AddedProxyIDs: []WebAPIID{}, GroupIDs: []WebAPIID{}, RuleIDs: []WebAPIID{}}
		profile, err := tx.Profile(request.ID)
		if err != nil {
			return nil, err
		}
		if profile.Type != "custom" || webAPIUnreadable(profile.WebAPIProfileInput) {
			return nil, webAPIError(409, "invalid_profile_state", "custom profile state changed")
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		for _, item := range profiles {
			typeName := "file"
			if item.source.URL != "" {
				typeName = "url"
			}
			p, err := tx.CreateProfile(WebAPIProfileInput{Type: typeName, Label: item.source.Label, URL: item.source.URL, AgeSecretKey: item.source.AgeSecretKey, ExtendType: "standard", AutoUpdate: true, AutoUpdateDurationMillis: 86400000, SelectedMap: map[string]string{}, UnfoldSet: []string{}})
			if err != nil {
				return nil, err
			}
			p.LastUpdateDate = &now
			if err = tx.SaveProfile(p); err != nil {
				return nil, err
			}
			if err = tx.SetProfileCatalog(p.ID, *item.catalog); err != nil {
				return nil, err
			}
			if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: p.ID}, MediaType: "application/yaml", Bytes: item.content}); err != nil {
				return nil, err
			}
			result.AddedProfileIDs = append(result.AddedProfileIDs, p.ID)
		}
		existingProviders, err := tx.ClashProviders()
		if err != nil {
			return nil, err
		}
		labels := map[string]bool{}
		for _, p := range existingProviders {
			labels[p.Label] = true
		}
		for _, item := range providers {
			if labels[item.source.Label] {
				return nil, webAPIError(409, "duplicate_label", "rule provider label already exists")
			}
			labels[item.source.Label] = true
			id, err := tx.AllocateID()
			if err != nil {
				return nil, err
			}
			provider := WebAPIClashProvider{ID: id, Label: item.source.Label, URL: item.source.URL, Behavior: item.ruleInfo.Behavior, Format: item.ruleInfo.Format}
			if err = tx.InsertClashProvider(provider); err != nil {
				return nil, err
			}
			if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: id}, MediaType: "application/octet-stream", Bytes: item.content}); err != nil {
				return nil, err
			}
			result.AddedRuleProviderIDs = append(result.AddedRuleProviderIDs, id)
		}
		for _, definition := range effective.Custom.Proxies {
			id, err := tx.AllocateID()
			if err != nil {
				return nil, err
			}
			if err = tx.InsertCustomProxy(WebAPICustomProxy{ID: id, Definition: definition, EmptyOptions: []string{}}); err != nil {
				return nil, err
			}
			result.AddedProxyIDs = append(result.AddedProxyIDs, id)
		}
		groups, err := tx.ProxyGroups()
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if group.ProfileID == request.ID {
				if err = tx.DeleteProxyGroup(group.ID); err != nil {
					return nil, err
				}
			}
		}
		owned, err := tx.OwnedRules(&request.ID)
		if err != nil {
			return nil, err
		}
		if len(owned) > 0 {
			ids := make([]WebAPIID, len(owned))
			for i, rule := range owned {
				ids[i] = rule.ID
			}
			if _, err = tx.DeleteRules(ids); err != nil {
				return nil, err
			}
		}
		dialers, err := tx.ProxyDialers()
		if err != nil {
			return nil, err
		}
		for _, dialer := range dialers {
			if dialer.ProfileID == request.ID {
				if err = tx.DeleteProxyDialer(dialer.ProfileID, dialer.ProxyID); err != nil {
					return nil, err
				}
			}
		}
		for i, input := range effective.Custom.ProxyGroups {
			id, err := tx.AllocateID()
			if err != nil {
				return nil, err
			}
			order := webAPIOrder(i)
			if err = tx.InsertProxyGroup(WebAPIProxyGroup{ID: id, ProfileID: request.ID, Definition: input.Definition, MemberLayout: input.MemberLayout, Order: &order}); err != nil {
				return nil, err
			}
			result.GroupIDs = append(result.GroupIDs, id)
		}
		for i, input := range effective.Custom.Rules {
			if input.ProfileID != nil && *input.ProfileID != request.ID {
				return nil, webAPIError(409, "owner_conflict", "imported rule belongs to another profile")
			}
			input.ProfileID = &request.ID
			id, err := tx.AllocateID()
			if err != nil {
				return nil, err
			}
			order := webAPIOrder(i)
			if err = tx.InsertRule(WebAPIRule{ID: id, WebAPIRuleInput: input, Order: &order}); err != nil {
				return nil, err
			}
			result.RuleIDs = append(result.RuleIDs, id)
		}
		customProxies, err := tx.CustomProxies()
		if err != nil {
			return nil, err
		}
		byName := map[string]WebAPIID{}
		for _, proxy := range customProxies {
			byName[webAPIProxyString(proxy.Definition, "name")] = proxy.ID
		}
		for source, target := range effective.Custom.Dialers {
			id, ok := byName[source]
			if !ok {
				return nil, webAPIError(409, "invalid_reference", "dialer source is absent")
			}
			if err = tx.SaveProxyDialer(WebAPIProxyDialer{ProfileID: request.ID, ProxyID: id, Target: target}); err != nil {
				return nil, err
			}
		}
		profile.Overrides = effective.Custom.Overrides
		profile.LastUpdateDate = &now
		if err = tx.SaveProfile(profile); err != nil {
			return nil, err
		}
		if err = RefreshWebAPICatalog(tx); err != nil {
			return nil, err
		}
		if err = tx.validateProfile(profile.WebAPIProfileInput, profile.ID); err != nil {
			return nil, err
		}
		rules, err := tx.OwnedRules(&request.ID)
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			if err = tx.validateRule(rule.WebAPIRuleInput); err != nil {
				return nil, err
			}
		}
		result.Profile = profile
		return result, nil
	})
	finish(err == nil)
	return receipt, err
}

func (s *WebAPIContentService) registerCustomImport(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/profiles:import-custom", webContentCommand(WebAPIMaxContentJSONBytes, s.ImportCustom))
}
