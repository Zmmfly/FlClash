//go:build webpanel && !android

package core

import (
	"context"
	"reflect"
	"strings"
)

type WebAPIContentMetadata struct {
	Profile      *WebAPIProfileInput  `json:"profile,omitempty"`
	Script       *webAPIScriptInput   `json:"script,omitempty"`
	RuleProvider *webAPIProviderInput `json:"ruleProvider,omitempty"`
	MitmModule   *webAPIModuleInput   `json:"mitmModule,omitempty"`
}

type WebAPIContentAttachment struct {
	SourceReference string `json:"sourceReference"`
	Bytes           []byte `json:"bytes"`
}

func webContentMetadataKind(key WebAPIContentKey, metadata *WebAPIContentMetadata) error {
	if metadata == nil {
		return nil
	}
	count := 0
	kind := WebAPIContentKind("")
	if metadata.Profile != nil {
		count++
		kind = WebAPIContentProfile
	}
	if metadata.Script != nil {
		count++
		kind = WebAPIContentScript
	}
	if metadata.RuleProvider != nil {
		count++
		kind = WebAPIContentRuleProvider
	}
	if metadata.MitmModule != nil {
		count++
		kind = WebAPIContentMitmModule
	}
	if key.Kind == WebAPIContentMitmResource && kind == WebAPIContentMitmModule && count == 1 {
		return nil
	}
	if count != 1 || kind != key.Kind {
		return webAPIError(400, "invalid_content_metadata", "metadata must match exactly one content owner")
	}
	return nil
}

func (s *WebAPIContentService) prepareMetadata(ctx context.Context, key WebAPIContentKey, data []byte, metadata *WebAPIContentMetadata, attachments []WebAPIContentAttachment, detach []string) (webContentPrepared, error) {
	if err := webContentMetadataKind(key, metadata); err != nil {
		return webContentPrepared{}, err
	}
	if (len(attachments) > 0 || len(detach) > 0) && key.Kind != WebAPIContentMitmModule {
		return webContentPrepared{}, webAPIError(400, "invalid_content_attachments", "attachments are only valid for a complete module save")
	}
	var prepared webContentPrepared
	var err error
	if metadata != nil && metadata.Profile != nil {
		var current WebAPIProfile
		_, err = s.store.View(ctx, func(tx *WebAPIReadTx) error { var err error; current, err = tx.Profile(key.ID); return err })
		if err != nil {
			return prepared, err
		}
		if webAPIUnreadable(current.WebAPIProfileInput) || webAPIUnreadable(*metadata.Profile) {
			return prepared, webAPIError(409, "unreadable_overrides", "repair unreadable profile overrides before editing content")
		}
		if current.Type != metadata.Profile.Type || current.Type == "custom" {
			return prepared, webAPIError(409, "immutable_profile_type", "content save cannot change the profile type")
		}
		raw, catalog, err := webContentProfile(data, metadata.Profile.AgeSecretKey)
		if err != nil {
			return prepared, err
		}
		prepared = webContentPrepared{sourceKey: key, content: WebAPIContent{Key: key, Bytes: data, MediaType: "application/yaml", SHA256: webContentDigest(data)}, catalog: &catalog, rawProfile: raw}
	} else {
		var arguments map[string]string
		if metadata != nil && metadata.MitmModule != nil {
			arguments = metadata.MitmModule.Arguments
			if arguments == nil {
				arguments = map[string]string{}
			}
		}
		prepared, err = s.prepareWithArguments(ctx, key, data, arguments)
		if err != nil {
			return prepared, err
		}
	}
	if key.Kind != WebAPIContentMitmModule {
		return prepared, nil
	}
	var prior WebAPIMitmModule
	var previous WebAPIContent
	_, err = s.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		prior, err = tx.MitmModule(key.ID)
		if err != nil {
			return err
		}
		previous, _ = tx.Content(key)
		return nil
	})
	if err != nil {
		return prepared, err
	}
	if err := webContentBindModuleAttachments(&prepared, metadata, attachments, detach, prior, previous); err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (s *WebAPIContentService) applyMetadata(tx *WebAPIWriteTx, prepared webContentPrepared, metadata *WebAPIContentMetadata) (any, error) {
	value, err := s.apply(tx, prepared)
	if err != nil || metadata == nil {
		return value, err
	}
	if err := webContentMetadataKind(prepared.sourceKey, metadata); err != nil {
		return nil, err
	}
	result := value.(WebAPIContentMutationResult)
	id := prepared.sourceKey.ID
	switch {
	case metadata.Profile != nil:
		profile, err := tx.Profile(id)
		if err != nil {
			return nil, err
		}
		if profile.Label != metadata.Profile.Label {
			if _, err = tx.RenameProfile(id, metadata.Profile.Label); err != nil {
				return nil, err
			}
		}
		profile, err = tx.UpdateProfile(id, *metadata.Profile)
		if err != nil {
			return nil, err
		}
		result.Profile = &profile
	case metadata.Script != nil:
		old, err := tx.Script(id)
		if err != nil {
			return nil, err
		}
		next, err := webAPIScriptResource().build(id, *metadata.Script, &old)
		if err != nil {
			return nil, err
		}
		items, err := tx.Scripts()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.ID != id && item.Label == next.Label {
				return nil, webAPIError(409, "duplicate_label", "script label already exists")
			}
		}
		if err = tx.SaveScript(next); err != nil {
			return nil, err
		}
		result.Script = &next
	case metadata.RuleProvider != nil:
		old, err := tx.ClashProvider(id)
		if err != nil {
			return nil, err
		}
		resource := webAPIProviderResource()
		next, err := resource.build(id, *metadata.RuleProvider, &old)
		if err != nil {
			return nil, err
		}
		items, err := tx.ClashProviders()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.ID != id && item.Label == next.Label {
				return nil, webAPIError(409, "duplicate_label", "rule provider label already exists")
			}
		}
		if err = tx.SaveClashProvider(next); err != nil {
			return nil, err
		}
		if err = resource.changed(tx, []WebAPIClashProvider{old}, []WebAPIClashProvider{next}); err != nil {
			return nil, err
		}
		result.RuleProvider = &next
	case metadata.MitmModule != nil:
		old, err := tx.MitmModule(id)
		if err != nil {
			return nil, err
		}
		input := *metadata.MitmModule
		if prepared.module == nil && !reflect.DeepEqual(input.Arguments, old.Arguments) {
			return nil, webAPIError(409, "content_command_required", "module argument changes require a complete module save")
		}
		shadow := old
		shadow.Scripts = 0
		next, err := webAPIModuleResource().build(id, input, &shadow)
		if err != nil {
			return nil, err
		}
		next.Scripts = old.Scripts
		next.Resources = old.Resources
		if prepared.consentRequired {
			next.Enabled = false
		}
		if prepared.module != nil && !reflect.DeepEqual(next.Arguments, prepared.module.Arguments) {
			return nil, webAPIError(409, "module_arguments_changed", "module arguments do not match content validation")
		}
		if err = tx.SaveMitmModule(next); err != nil {
			return nil, err
		}
		result.MitmModule = &next
	}
	return result, nil
}

func webContentMetadataURL(metadata *WebAPIContentMetadata) (string, bool) {
	if metadata == nil {
		return "", false
	}
	if metadata.Profile != nil {
		return metadata.Profile.URL, true
	}
	if metadata.Script != nil {
		if metadata.Script.URL == nil {
			return "", true
		}
		return *metadata.Script.URL, true
	}
	if metadata.RuleProvider != nil {
		return metadata.RuleProvider.URL, true
	}
	if metadata.MitmModule != nil {
		if metadata.MitmModule.URL == nil {
			return "", true
		}
		return *metadata.MitmModule.URL, true
	}
	return "", false
}

func webContentPruneModuleResources(tx *WebAPIWriteTx, module WebAPIMitmModule, previousNames ...string) error {
	seen := map[string]bool{}
	for _, name := range previousNames {
		if name != "" && !seen[name] && !webContentModuleHasKey(module, name) {
			if err := tx.DeleteContent(WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: module.ID, Name: name}); err != nil {
				return err
			}
		}
		seen[name] = true
	}
	return nil
}

func webContentBindModuleAttachments(prepared *webContentPrepared, metadata *WebAPIContentMetadata, attachments []WebAPIContentAttachment, detach []string, prior WebAPIMitmModule, previous WebAPIContent) error {
	key := prepared.content.Key
	data := prepared.content.Bytes
	if metadata != nil && metadata.MitmModule != nil {
		if metadata.MitmModule.URL != nil && len(attachments) > 0 {
			return webAPIError(409, "remote_module_attachment", "local attachments require a local module")
		}
		if derived, err := webContentParseModule(data, prepared.module.Arguments); err == nil && derived.Name != "" && metadata.MitmModule.Name != derived.Name {
			return webAPIError(409, "module_metadata_conflict", "module content name conflicts with metadata")
		}
	}
	derived, err := webContentParseModule(data, prepared.module.Arguments)
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, reference := range derived.Resources {
		declared[reference] = true
	}
	seen := map[string]bool{}
	total := len(data)
	for _, remote := range prepared.attachments {
		total += len(remote.Bytes)
	}
	for _, attachment := range attachments {
		reference := attachment.SourceReference
		if !declared[reference] || seen[reference] || strings.HasPrefix(reference, "http://") || strings.HasPrefix(reference, "https://") || len(attachment.Bytes) > 16<<20 {
			return webAPIError(400, "invalid_content_attachment", "attachment must match one declared local module resource")
		}
		seen[reference] = true
		total += len(attachment.Bytes)
		if total > WebAPIBackupExpandedLimit {
			return webAPIError(413, "content_too_large", "module save exceeds the aggregate content limit")
		}
		name := webContentModuleResourceKey(attachment.Bytes)
		prepared.module.Resources[reference] = name
		prepared.attachments = append(prepared.attachments, WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: key.ID, Name: name}, MediaType: "application/octet-stream", Bytes: attachment.Bytes, SHA256: webContentDigest(attachment.Bytes)})
	}
	for _, reference := range detach {
		if seen[reference] || declared[reference] {
			return webAPIError(409, "module_resource_in_use", "cannot detach a resource still declared by the new module")
		}
		if _, ok := prior.Resources[reference]; !ok {
			return webAPIError(404, "not_found", "module resource is not attached")
		}
		delete(prepared.module.Resources, reference)
		seen[reference] = true
	}
	before, _ := webContentParseModule(previous.Bytes, prior.Arguments)
	if len(derived.Scripts) > 0 && webContentModuleScriptDigest(before, prior.Resources) != webContentModuleScriptDigest(derived, prepared.module.Resources) {
		prepared.consentRequired = true
		prepared.module.Enabled = false
	}
	return nil
}
