//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type WebAPIResourceImport struct {
	WebAPIMutation
	WebAPIContentJobSelection
	ID          *WebAPIID                 `json:"id,omitempty"`
	Label       string                    `json:"label"`
	Source      WebAPIContentImportSource `json:"source"`
	Metadata    *WebAPIContentMetadata    `json:"metadata,omitempty"`
	Attachments []WebAPIContentAttachment `json:"attachments,omitempty"`
}

type WebAPIResourceImportCommand[T any] struct {
	WebAPIMutation
	WebAPIContentJobSelection
	ID          *WebAPIID                 `json:"id,omitempty"`
	Label       string                    `json:"label"`
	Source      WebAPIContentImportSource `json:"source"`
	Metadata    *T                        `json:"metadata,omitempty"`
	Attachments []WebAPIContentAttachment `json:"attachments,omitempty"`
}

func (s *WebAPIContentService) importResource(ctx context.Context, kind WebAPIContentKind, request WebAPIResourceImport) (WebAPIReceipt, error) {
	command := map[WebAPIContentKind]string{WebAPIContentScript: "scripts:import", WebAPIContentRuleProvider: "rule-providers:import", WebAPIContentMitmModule: "mitm-modules:import"}[kind]
	if command == "" {
		return WebAPIReceipt{}, webAPIError(400, "invalid_resource_kind", "unsupported resource import kind")
	}
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, command, request); done || err != nil {
		return receipt, err
	}
	id := WebAPIID("")
	if request.ID != nil {
		id = *request.ID
	}
	key := WebAPIContentKey{Kind: kind, ID: id}
	fingerprint := request
	var pendingJob webContentPendingJob
	usingPending := request.PreparedJobID != ""
	if kind != WebAPIContentMitmModule && (usingPending || len(request.Attachments) > 0) {
		return WebAPIReceipt{}, webAPIError(400, "invalid_import_attachments", "only module imports accept attachments or prepared jobs")
	}
	if usingPending {
		var err error
		ctx, pendingJob, err = s.usePending(ctx, request.WebAPIContentJobSelection, request.WebAPIMutation, command, key)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		if pendingJob.importRequest == nil {
			return WebAPIReceipt{}, webAPIError(409, "content_preparation_mismatch", "pending job is not a module import")
		}
		saved := *pendingJob.importRequest
		if request.Label != "" && request.Label != saved.Label || request.Source.Kind != "" && (request.Source.Kind != saved.Source.Kind || request.Source.URL != saved.Source.URL) || len(request.Source.Bytes) > 0 && webContentDigest(request.Source.Bytes) != pendingJob.info.ContentSHA256 || request.Source.AuthorizationID != "" && request.Source.AuthorizationID != saved.Source.AuthorizationID || request.Metadata != nil && !webContentPendingEqual(request.Metadata, saved.Metadata) || len(request.Attachments) > 0 && !webContentPendingEqual(request.Attachments, pendingJob.attachments) {
			return WebAPIReceipt{}, webAPIError(409, "content_preparation_mismatch", "module import changed after preparation")
		}
		saved.WebAPIMutation = request.WebAPIMutation
		saved.WebAPIContentJobSelection = request.WebAPIContentJobSelection
		saved.Attachments = pendingJob.attachments
		saved.Metadata, err = webContentCloneValue(saved.Metadata)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		request = saved
	} else if request.RequestDigest != "" || len(request.AuthorizationIDs) > 0 {
		return WebAPIReceipt{}, webAPIError(400, "invalid_content_preparation", "dependency authorizations require a prepared import")
	}
	if err := webContentMetadataKind(key, request.Metadata); err != nil {
		return WebAPIReceipt{}, err
	}
	var oldScript *WebAPIScript
	var oldProvider *WebAPIClashProvider
	var oldModule *WebAPIMitmModule
	var previous WebAPIContent
	var all []WebAPIContent
	if request.ID != nil {
		_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
			if err := webContentOwner(tx, key); err != nil {
				return err
			}
			switch kind {
			case WebAPIContentScript:
				v, err := tx.Script(id)
				oldScript = &v
				return err
			case WebAPIContentRuleProvider:
				v, err := tx.ClashProvider(id)
				oldProvider = &v
				return err
			case WebAPIContentMitmModule:
				v, err := tx.MitmModule(id)
				if err != nil {
					return err
				}
				oldModule = &v
				previous, _ = tx.Content(key)
				all = nil
				return err
			}
			return nil
		})
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	metadata := request.Metadata
	if metadata == nil {
		metadata = &WebAPIContentMetadata{}
		switch kind {
		case WebAPIContentScript:
			v := webAPIScriptInput{Label: request.Label}
			if oldScript != nil {
				v.Label = oldScript.Label
				v.URL = oldScript.URL
			}
			if request.Label != "" {
				v.Label = request.Label
			}
			if request.Source.Kind == "url" {
				v.URL = &request.Source.URL
			}
			metadata.Script = &v
		case WebAPIContentRuleProvider:
			v := webAPIProviderInput{Label: request.Label}
			if oldProvider != nil {
				v.Label = oldProvider.Label
				v.URL = oldProvider.URL
			}
			if request.Label != "" {
				v.Label = request.Label
			}
			if request.Source.Kind == "url" {
				v.URL = request.Source.URL
			}
			metadata.RuleProvider = &v
		case WebAPIContentMitmModule:
			v := webAPIModuleInput{Name: request.Label, Enabled: true, Arguments: map[string]string{}}
			if oldModule != nil {
				v = webAPIModuleInput{Name: oldModule.Name, Description: oldModule.Description, URL: oldModule.URL, Enabled: oldModule.Enabled, Arguments: oldModule.Arguments, System: oldModule.System}
			}
			if request.Label != "" {
				v.Name = request.Label
			}
			if request.Source.Kind == "url" {
				v.URL = &request.Source.URL
			}
			metadata.MitmModule = &v
		}
	}
	label := ""
	switch kind {
	case WebAPIContentScript:
		label = metadata.Script.Label
	case WebAPIContentRuleProvider:
		label = metadata.RuleProvider.Label
	case WebAPIContentMitmModule:
		label = metadata.MitmModule.Name
	}
	if !webAPICatalogName(label) || request.Label != "" && request.Label != label {
		return WebAPIReceipt{}, webAPIError(400, "metadata_label_conflict", "resource label and metadata must agree")
	}
	if request.Source.Kind == "url" {
		url, _ := webContentMetadataURL(metadata)
		if url != request.Source.URL {
			return WebAPIReceipt{}, webAPIError(400, "metadata_source_conflict", "source URL and metadata must agree")
		}
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	data := request.Source.Bytes
	if usingPending {
		data = pendingJob.body
	} else {
		switch request.Source.Kind {
		case "file":
			if request.Source.URL != "" || len(request.Source.Links) != 0 || request.Source.AuthorizationID != "" {
				return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "invalid file source")
			}
		case "url":
			if len(data) != 0 || len(request.Source.Links) != 0 {
				return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "invalid URL source")
			}
			purpose := map[WebAPIContentKind]WebAPISourcePurpose{WebAPIContentScript: WebAPISourceScriptImport, WebAPIContentRuleProvider: WebAPISourceRuleProviderImport, WebAPIContentMitmModule: WebAPISourceMitmModuleImport}[kind]
			var resource *WebAPISourceResource
			if request.ID != nil {
				resource = &WebAPISourceResource{Kind: string(kind), ID: string(id)}
			}
			fetched, err := WebAPIFetchSource(ctx, s.store, WebAPISourceRequest{Purpose: purpose, Key: resource, URL: request.Source.URL, AuthorizationID: request.Source.AuthorizationID, ImportJobID: request.OperationID}, s.sourceOptions)
			if err != nil {
				return WebAPIReceipt{}, err
			}
			data = fetched.Bytes
		default:
			return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "resource source must be file or url")
		}
	}
	if len(data) > WebAPIMaxContentBytes {
		return WebAPIReceipt{}, webAPIError(413, "content_too_large", "resource exceeds the content limit")
	}
	prepared := webContentPrepared{sourceKey: key, content: WebAPIContent{Key: key, Bytes: data, SHA256: webContentDigest(data)}}
	switch kind {
	case WebAPIContentScript:
		if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 || request.Source.Kind == "url" && json.Valid(data) {
			return WebAPIReceipt{}, webAPIError(400, "invalid_script_content", "source is not a JavaScript document")
		}
		prepared.content.MediaType = "text/javascript"
	case WebAPIContentRuleProvider:
		info, _, err := webContentRuleSet(data, true)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		prepared.ruleInfo = info
		prepared.content.MediaType = "application/octet-stream"
	case WebAPIContentMitmModule:
		module := WebAPIMitmModule{ID: id, Name: label, Enabled: metadata.MitmModule.Enabled, Arguments: map[string]string{}, Resources: map[string]string{}}
		if oldModule != nil {
			module = *oldModule
		}
		module.URL = metadata.MitmModule.URL
		if metadata.MitmModule.Arguments == nil {
			metadata.MitmModule.Arguments = map[string]string{}
		}
		prepared, err = s.prepareModule(ctx, prepared, module, previous, all, metadata.MitmModule.Arguments)
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	if kind == WebAPIContentMitmModule {
		prior := WebAPIMitmModule{Resources: map[string]string{}, Arguments: map[string]string{}}
		if oldModule != nil {
			prior = *oldModule
		}
		if err = webContentBindModuleAttachments(&prepared, request.Metadata, request.Attachments, nil, prior, previous); err != nil {
			return WebAPIReceipt{}, err
		}
	}
	if kind == WebAPIContentMitmModule && request.Metadata == nil {
		metadata.MitmModule.Name = prepared.module.Name
		metadata.MitmModule.Description = prepared.module.Description
		metadata.MitmModule.System = prepared.module.System
	}
	finish := func(bool) {}
	if usingPending {
		finish, err = s.beginPendingCommit(ctx, pendingJob)
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, command, fingerprint, func(tx *WebAPIWriteTx) (any, error) {
		if usingPending && !s.now().Before(pendingJob.expires) {
			return nil, webAPIError(409, "content_preparation_invalid", "content preparation expired")
		}
		if request.ID != nil {
			return s.applyMetadata(tx, prepared, metadata)
		}
		id, err := tx.AllocateID()
		if err != nil {
			return nil, err
		}
		prepared.content.Key.ID = id
		prepared.sourceKey = prepared.content.Key
		now := s.now().UTC().Format(time.RFC3339Nano)
		result := WebAPIContentMutationResult{}
		switch kind {
		case WebAPIContentScript:
			resource := webAPIScriptResource()
			script, err := resource.build(id, *metadata.Script, nil)
			if err != nil {
				return nil, err
			}
			items, err := tx.Scripts()
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if item.Label == script.Label {
					return nil, webAPIError(409, "duplicate_label", "script label already exists")
				}
			}
			order := int32(len(items))
			script.Order = &order
			script.LastUpdateTime = now
			if err = tx.InsertScript(script); err != nil {
				return nil, err
			}
			result.Script = &script
		case WebAPIContentRuleProvider:
			provider, err := webAPIProviderResource().build(id, *metadata.RuleProvider, nil)
			if err != nil {
				return nil, err
			}
			items, err := tx.ClashProviders()
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if item.Label == provider.Label {
					return nil, webAPIError(409, "duplicate_label", "rule provider label already exists")
				}
			}
			order := int32(len(items))
			provider.Order = &order
			provider.Behavior = prepared.ruleInfo.Behavior
			provider.Format = prepared.ruleInfo.Format
			if err = tx.InsertClashProvider(provider); err != nil {
				return nil, err
			}
			result.RuleProvider = &provider
		case WebAPIContentMitmModule:
			module := *prepared.module
			module.ID = id
			items, err := tx.MitmModules()
			if err != nil {
				return nil, err
			}
			order := int32(len(items))
			module.Order = &order
			module.LastUpdateTime = now
			if request.Metadata != nil && metadata.MitmModule.Name != "" {
				derived, err := webContentParseModule(data, module.Arguments)
				if err != nil {
					return nil, err
				}
				if derived.Name != "" && derived.Name != metadata.MitmModule.Name {
					return nil, webAPIError(409, "module_metadata_conflict", "module content name conflicts with metadata")
				}
				module.Name = metadata.MitmModule.Name
			}
			module.Enabled = metadata.MitmModule.Enabled && !prepared.consentRequired
			if err = tx.InsertMitmModule(module); err != nil {
				return nil, err
			}
			for _, attachment := range prepared.attachments {
				attachment.Key.ID = id
				if err = tx.PutContent(attachment); err != nil {
					return nil, err
				}
			}
			result.MitmModule = &module
		}
		if err = tx.PutContent(prepared.content); err != nil {
			return nil, err
		}
		result.Content = webContentSummary(prepared.content)
		return result, nil
	})
	finish(err == nil)
	return receipt, err
}

func (s *WebAPIContentService) registerImports(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/scripts:import", webContentCommand(WebAPIMaxContentJSONBytes, func(ctx context.Context, r WebAPIResourceImportCommand[webAPIScriptInput]) (WebAPIReceipt, error) {
		var metadata *WebAPIContentMetadata
		if r.Metadata != nil {
			metadata = &WebAPIContentMetadata{Script: r.Metadata}
		}
		return s.importResource(ctx, WebAPIContentScript, WebAPIResourceImport{WebAPIMutation: r.WebAPIMutation, ID: r.ID, Label: r.Label, Source: r.Source, Metadata: metadata, WebAPIContentJobSelection: r.WebAPIContentJobSelection, Attachments: r.Attachments})
	}))
	mux.HandleFunc("/api/v1/rule-providers:import", webContentCommand(WebAPIMaxContentJSONBytes, func(ctx context.Context, r WebAPIResourceImportCommand[webAPIProviderInput]) (WebAPIReceipt, error) {
		var metadata *WebAPIContentMetadata
		if r.Metadata != nil {
			metadata = &WebAPIContentMetadata{RuleProvider: r.Metadata}
		}
		return s.importResource(ctx, WebAPIContentRuleProvider, WebAPIResourceImport{WebAPIMutation: r.WebAPIMutation, ID: r.ID, Label: r.Label, Source: r.Source, Metadata: metadata, WebAPIContentJobSelection: r.WebAPIContentJobSelection, Attachments: r.Attachments})
	}))
	mux.HandleFunc("/api/v1/mitm-modules:import", webContentCommand(WebAPIMaxContentJSONBytes, func(ctx context.Context, r WebAPIResourceImportCommand[webAPIModuleInput]) (WebAPIReceipt, error) {
		var metadata *WebAPIContentMetadata
		if r.Metadata != nil {
			metadata = &WebAPIContentMetadata{MitmModule: r.Metadata}
		}
		return s.importResource(ctx, WebAPIContentMitmModule, WebAPIResourceImport{WebAPIMutation: r.WebAPIMutation, ID: r.ID, Label: r.Label, Source: r.Source, Metadata: metadata, WebAPIContentJobSelection: r.WebAPIContentJobSelection, Attachments: r.Attachments})
	}))
}
