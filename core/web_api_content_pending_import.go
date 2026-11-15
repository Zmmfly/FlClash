//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
)

func (s *WebAPIContentService) PrepareModuleImport(ctx context.Context, input WebAPIResourceImportCommand[webAPIModuleInput]) (WebAPIRevision, WebAPIContentPreparation, error) {
	var result WebAPIContentPreparation
	if input.PreparedJobID != "" || input.RequestDigest != "" || len(input.AuthorizationIDs) > 0 {
		return "", result, webAPIError(400, "invalid_content_preparation", "a new preparation cannot select an existing job")
	}
	var metadata *WebAPIContentMetadata
	if input.Metadata != nil {
		metadata = &WebAPIContentMetadata{MitmModule: input.Metadata}
	}
	request := WebAPIResourceImport{WebAPIMutation: input.WebAPIMutation, ID: input.ID, Label: input.Label, Source: input.Source, Metadata: metadata, Attachments: input.Attachments}
	key := WebAPIContentKey{Kind: WebAPIContentMitmModule}
	if input.ID != nil {
		key.ID = *input.ID
	}
	var old *WebAPIMitmModule
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		if input.ID == nil {
			return nil
		}
		value, err := tx.MitmModule(*input.ID)
		old = &value
		return err
	})
	if err != nil {
		return revision, result, err
	}
	if revision != input.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "module import target changed")
	}
	moduleInput := webAPIModuleInput{Name: input.Label, Enabled: true, Arguments: map[string]string{}}
	if old != nil {
		moduleInput = webAPIModuleInput{Name: old.Name, Description: old.Description, URL: old.URL, Enabled: old.Enabled, Arguments: old.Arguments, System: old.System}
		if input.Label != "" {
			moduleInput.Name = input.Label
		}
	}
	if input.Source.Kind == "url" {
		moduleInput.URL = &input.Source.URL
	}
	if input.Metadata != nil {
		moduleInput = *input.Metadata
	}
	if input.Label != "" && input.Label != moduleInput.Name {
		return revision, result, webAPIError(400, "metadata_label_conflict", "module label and metadata must agree")
	}
	shadow := old
	if old != nil {
		copy := *old
		copy.Scripts = 0
		shadow = &copy
	}
	if _, err = webAPIModuleResource().build(key.ID, moduleInput, shadow); err != nil {
		return revision, result, err
	}
	release, err := s.slot(ctx)
	if err != nil {
		return revision, result, err
	}
	defer release()
	data := input.Source.Bytes
	operation := ""
	switch input.Source.Kind {
	case "file":
		if input.Source.URL != "" || len(input.Source.Links) > 0 || input.Source.AuthorizationID != "" {
			return revision, result, webAPIError(400, "invalid_import_source", "invalid file source")
		}
	case "url":
		if len(data) > 0 || len(input.Source.Links) > 0 || moduleInput.URL == nil || *moduleInput.URL != input.Source.URL {
			return revision, result, webAPIError(400, "invalid_import_source", "invalid module source or metadata URL")
		}
		if input.Source.AuthorizationID != "" {
			grant, err := s.store.SourceGrant(ctx, input.Source.AuthorizationID)
			if err != nil {
				return revision, result, err
			}
			operation = grant.ImportJobID
			if operation == "" || input.OperationID != "" && input.OperationID != operation {
				return revision, result, webAPIError(409, "source_import_job_mismatch", "module import must use its source preparation operation")
			}
			request.OperationID = operation
			if job, err := s.pending(ctx, operation); err == nil {
				if job.importRequest != nil && webContentPendingImportEqual(job, request) {
					return revision, job.info, nil
				}
				return revision, result, webAPIError(409, "content_job_exists", "import job already has different content")
			}
		}
		var resource *WebAPISourceResource
		if input.ID != nil {
			resource = &WebAPISourceResource{Kind: "mitm-module", ID: string(*input.ID)}
		}
		fetched, err := WebAPIFetchSource(ctx, s.store, WebAPISourceRequest{Purpose: WebAPISourceMitmModuleImport, Key: resource, URL: input.Source.URL, AuthorizationID: input.Source.AuthorizationID, ImportJobID: operation}, s.sourceOptions)
		if err != nil {
			return revision, result, err
		}
		data = fetched.Bytes
	default:
		return revision, result, webAPIError(400, "invalid_import_source", "module source must be file or url")
	}
	if moduleInput.Arguments == nil {
		moduleInput.Arguments = map[string]string{}
	}
	result, err = s.storePending(ctx, webContentPendingJob{command: "mitm-modules:import", revision: input.ExpectedRevision, key: key, body: data, metadata: metadata, arguments: moduleInput.Arguments, attachments: input.Attachments, importRequest: &request}, operation)
	return revision, result, err
}

func webContentPendingImportEqual(job webContentPendingJob, request WebAPIResourceImport) bool {
	if job.importRequest == nil {
		return false
	}
	if len(request.Source.Bytes) > 0 && webContentDigest(request.Source.Bytes) != job.info.ContentSHA256 {
		return false
	}
	for _, attachment := range request.Attachments {
		matched := false
		for _, stored := range job.attachments {
			if stored.SourceReference == attachment.SourceReference && webContentDigest(stored.Bytes) == webContentDigest(attachment.Bytes) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	request.Source.Bytes = nil
	request.Attachments = nil
	return webContentPendingEqual(*job.importRequest, request)
}

func (s *WebAPIContentService) registerPendingImport(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/mitm-modules:prepare-import", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIResourceImportCommand[webAPIModuleInput]
		if err := WebAPIDecodeJSONLimit(w, r, &request, WebAPIMaxContentJSONBytes); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.PrepareModuleImport(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
}
