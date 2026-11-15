//go:build webpanel && !android

package core

import "context"

func (s *WebAPIContentService) Clone(ctx context.Context, request WebAPIProfileClone) (WebAPIReceipt, error) {
	return s.store.Transaction(ctx, request.WebAPIMutation, "profiles:clone", request, func(tx *WebAPIWriteTx) (any, error) {
		original, err := tx.Profile(request.ID)
		if err != nil {
			return nil, err
		}
		profile, err := webContentCloneValue(original)
		if err != nil {
			return nil, err
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		profile.ID = id
		profiles, err := tx.Profiles()
		if err != nil {
			return nil, err
		}
		taken := map[string]bool{}
		for _, p := range profiles {
			taken[p.Label] = true
		}
		label := request.Label
		if label == "" {
			label = profile.Label
			if label == "" {
				label = "Profile"
			}
			label += " copy"
		}
		if !webAPICatalogName(label) {
			return nil, webAPIError(400, "invalid_label", "invalid clone label")
		}
		profile.Label = webContentUniqueName(label, taken)
		order := int32(len(profiles))
		profile.Order = &order
		if err = tx.InsertProfile(profile); err != nil {
			return nil, err
		}
		if catalog, err := tx.ProfileCatalog(original.ID); err == nil {
			if err = tx.SetProfileCatalog(profile.ID, catalog); err != nil {
				return nil, err
			}
		}
		if content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: original.ID}); err == nil {
			content.Key.ID = profile.ID
			if err = tx.PutContent(content); err != nil {
				return nil, err
			}
		}
		assets, err := tx.ProfileAssets(original.ID)
		if err != nil {
			return nil, err
		}
		if _, err = webContentPlanetAssets(assets); err != nil {
			return nil, err
		}
		for _, asset := range assets {
			asset.Key.ID = profile.ID
			if err = tx.PutContent(asset); err != nil {
				return nil, err
			}
		}
		groups, err := tx.ProxyGroups()
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if group.ProfileID != original.ID {
				continue
			}
			group.ID, err = tx.AllocateID()
			if err != nil {
				return nil, err
			}
			group.ProfileID = profile.ID
			if err = tx.InsertProxyGroup(group); err != nil {
				return nil, err
			}
		}
		rules, err := tx.Rules()
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			if rule.ProfileID == nil || *rule.ProfileID != original.ID {
				continue
			}
			rule.ID, err = tx.AllocateID()
			if err != nil {
				return nil, err
			}
			rule.ProfileID = &profile.ID
			if err = tx.InsertRule(rule); err != nil {
				return nil, err
			}
		}
		disabled, err := tx.DisabledRules()
		if err != nil {
			return nil, err
		}
		for _, rule := range disabled {
			if rule.ProfileID != original.ID {
				continue
			}
			rule.ProfileID = profile.ID
			if err = tx.SaveDisabledRule(rule); err != nil {
				return nil, err
			}
		}
		dialers, err := tx.ProxyDialers()
		if err != nil {
			return nil, err
		}
		for _, dialer := range dialers {
			if dialer.ProfileID != original.ID {
				continue
			}
			dialer.ProfileID = profile.ID
			if err = tx.SaveProxyDialer(dialer); err != nil {
				return nil, err
			}
		}
		return profile, nil
	})
}

func (s *WebAPIContentService) Attach(ctx context.Context, request WebAPIMitmAttachment) (WebAPIReceipt, error) {
	if len(request.Bytes) > 16<<20 {
		return WebAPIReceipt{}, webAPIError(413, "content_too_large", "module resource exceeds 16 MiB")
	}
	if request.SourceReference == "" || len(request.SourceReference) > 256 {
		return WebAPIReceipt{}, webAPIError(400, "invalid_resource_reference", "attachment requires a logical source reference")
	}
	key := webContentModuleResourceKey(request.Bytes)
	content := WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: request.ID, Name: key}, Bytes: request.Bytes, MediaType: "application/octet-stream", SHA256: webContentDigest(request.Bytes)}
	return s.store.Transaction(ctx, request.WebAPIMutation, "mitm-modules:attach", request, func(tx *WebAPIWriteTx) (any, error) {
		module, err := tx.MitmModule(request.ID)
		if err != nil {
			return nil, err
		}
		if webBackupRemote(module.URL) {
			return nil, webAPIError(409, "remote_module_attachment", "attachments belong to local modules")
		}
		if module.Resources == nil {
			module.Resources = map[string]string{}
		}
		previousName := module.Resources[request.SourceReference]
		module.Resources[request.SourceReference] = key
		module.Enabled = false
		if err = tx.SaveMitmModule(module); err != nil {
			return nil, err
		}
		if err = tx.PutContent(content); err != nil {
			return nil, err
		}
		if err = webContentPruneModuleResources(tx, module, previousName); err != nil {
			return nil, err
		}
		return WebAPIContentMutationResult{Content: webContentSummary(content), MitmModule: &module}, nil
	})
}

type WebAPIMitmDetachResult struct {
	MitmModule  WebAPIMitmModule `json:"mitmModule"`
	DetachedKey WebAPIContentKey `json:"detachedKey"`
}

func (s *WebAPIContentService) Detach(ctx context.Context, request WebAPIMitmDetach) (WebAPIReceipt, error) {
	return s.store.Transaction(ctx, request.WebAPIMutation, "mitm-modules:detach", request, func(tx *WebAPIWriteTx) (any, error) {
		module, err := tx.MitmModule(request.ID)
		if err != nil {
			return nil, err
		}
		key, ok := module.Resources[request.SourceReference]
		if !ok {
			return nil, webAPIError(404, "not_found", "attachment does not exist")
		}
		delete(module.Resources, request.SourceReference)
		module.Enabled = false
		if err = tx.SaveMitmModule(module); err != nil {
			return nil, err
		}
		if !webContentModuleHasKey(module, key) {
			if err = tx.DeleteContent(WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: module.ID, Name: key}); err != nil {
				return nil, err
			}
		}
		return WebAPIMitmDetachResult{MitmModule: module, DetachedKey: WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: module.ID, Name: key}}, nil
	})
}

func (s *WebAPIContentService) SetArguments(ctx context.Context, request WebAPIMitmArguments) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "mitm-modules:arguments", request); done || err != nil {
		return receipt, err
	}
	key := WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: request.ID}
	var job webContentPendingJob
	arguments := request.Arguments
	var content WebAPIContent
	if request.PreparedJobID != "" {
		var err error
		ctx, job, err = s.usePending(ctx, request.WebAPIContentJobSelection, request.WebAPIMutation, "mitm-modules:arguments", key)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		if arguments != nil && !webContentPendingEqual(arguments, job.arguments) {
			return WebAPIReceipt{}, webAPIError(409, "content_preparation_mismatch", "arguments changed after preparation")
		}
		arguments = job.arguments
		content = WebAPIContent{Key: key, Bytes: job.body}
	} else {
		if request.RequestDigest != "" || len(request.AuthorizationIDs) > 0 {
			return WebAPIReceipt{}, webAPIError(400, "invalid_content_preparation", "dependency authorizations require a pending argument job")
		}
		_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error { var err error; content, err = tx.Content(key); return err })
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	if arguments == nil {
		return WebAPIReceipt{}, webAPIError(400, "invalid_module_arguments", "arguments must be an object")
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	prepared, err := s.prepareWithArguments(ctx, key, content.Bytes, arguments)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	finish := func(bool) {}
	if request.PreparedJobID != "" {
		finish, err = s.beginPendingCommit(ctx, job)
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, "mitm-modules:arguments", request, func(tx *WebAPIWriteTx) (any, error) {
		if request.PreparedJobID != "" && !s.now().Before(job.expires) {
			return nil, webAPIError(409, "content_preparation_invalid", "argument preparation expired")
		}
		return s.apply(tx, prepared)
	})
	finish(err == nil)
	return receipt, err
}
