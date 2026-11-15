//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type WebAPIContentJobSelection struct {
	PreparedJobID    string   `json:"preparedJobId,omitempty"`
	RequestDigest    string   `json:"requestDigest,omitempty"`
	AuthorizationIDs []string `json:"authorizationIds,omitempty"`
}

type WebAPIContentDependency struct {
	SourceReference string `json:"sourceReference"`
	URLDigest       string `json:"urlDigest"`
}

type WebAPIContentPreparation struct {
	PreparedJobID        string                    `json:"preparedJobId"`
	OperationID          string                    `json:"operationId"`
	RequestDigest        string                    `json:"requestDigest"`
	ContentSHA256        string                    `json:"contentSha256"`
	ExpiresAt            string                    `json:"expiresAt"`
	Dependencies         []WebAPIContentDependency `json:"dependencies"`
	AttachmentReferences []string                  `json:"attachmentReferences"`
}

type WebAPIContentPrepareWrite struct {
	ExpectedRevision WebAPIRevision            `json:"expectedRevision"`
	Key              WebAPIContentKey          `json:"key"`
	Bytes            []byte                    `json:"bytes"`
	Metadata         *WebAPIContentMetadata    `json:"metadata,omitempty"`
	Attachments      []WebAPIContentAttachment `json:"attachments,omitempty"`
	DetachReferences []string                  `json:"detachReferences,omitempty"`
}

type WebAPIContentPrepareArguments struct {
	ExpectedRevision WebAPIRevision    `json:"expectedRevision"`
	ID               WebAPIID          `json:"id"`
	Arguments        map[string]string `json:"arguments"`
}

type webContentPendingJob struct {
	info          WebAPIContentPreparation
	command       string
	revision      WebAPIRevision
	session       string
	expires       time.Time
	key           WebAPIContentKey
	body          []byte
	metadata      *WebAPIContentMetadata
	attachments   []WebAPIContentAttachment
	detach        []string
	arguments     map[string]string
	importRequest *WebAPIResourceImport
	sources       map[string]string
	memory        int
	finalizing    bool
}

type webContentPendingJobs struct {
	mu      sync.Mutex
	entries map[string]webContentPendingJob
	bytes   int
}
type webContentDependencyContext struct {
	job    webContentPendingJob
	grants map[string]string
}
type webContentDependencyContextKey struct{}

func (s *WebAPIContentService) storePending(ctx context.Context, job webContentPendingJob, operation string) (WebAPIContentPreparation, error) {
	var empty WebAPIContentPreparation
	session := WebAPIRequestSessionID(ctx)
	if session == "" {
		return empty, webAPIError(401, "unauthorized", "content preparation requires an authenticated session")
	}
	if len(job.body) > WebAPIMaxContentBytes {
		return empty, webAPIError(413, "content_too_large", "module source exceeds 32 MiB")
	}
	job.session = session
	job.body = append([]byte{}, job.body...)
	var err error
	job.metadata, err = webContentCloneValue(job.metadata)
	if err != nil {
		return empty, err
	}
	job.arguments, err = webContentCloneValue(job.arguments)
	if err != nil {
		return empty, err
	}
	job.detach = append([]string{}, job.detach...)
	job.attachments = append([]WebAPIContentAttachment{}, job.attachments...)
	if job.importRequest != nil {
		copy := *job.importRequest
		copy.Source.Bytes = nil
		copy.Attachments = nil
		copy.Source.Links = append([]string{}, copy.Source.Links...)
		copy.Metadata = job.metadata
		job.importRequest = &copy
	}
	job.memory = len(job.body)
	if job.arguments == nil {
		job.arguments = map[string]string{}
	}
	parsed, err := webContentParseModule(job.body, job.arguments)
	if err != nil {
		return empty, err
	}
	attachmentReferences, err := webContentValidatePendingAttachments(parsed, job.attachments)
	if err != nil {
		return empty, err
	}
	job.sources = map[string]string{}
	dependencies := []WebAPIContentDependency{}
	for _, source := range parsed.Resources {
		if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
			_, digest, err := webSourceCanonical(source)
			if err != nil {
				return empty, err
			}
			reference := "dependency-" + webContentDigest([]byte(source))[:32]
			job.sources[reference] = source
			dependencies = append(dependencies, WebAPIContentDependency{SourceReference: reference, URLDigest: digest})
		}
	}
	for i := range job.attachments {
		job.attachments[i].Bytes = append([]byte{}, job.attachments[i].Bytes...)
		job.memory += len(job.attachments[i].Bytes)
		if len(job.attachments[i].Bytes) > 16<<20 || job.memory > WebAPIBackupExpandedLimit {
			return empty, webAPIError(413, "content_too_large", "pending module exceeds its aggregate size limit")
		}
	}
	if operation == "" {
		operation, err = webBackupToken()
		if err != nil {
			return empty, err
		}
	}
	if !webAPIOperationID(operation) {
		return empty, webAPIError(400, "invalid_operation_id", "invalid content preparation operation")
	}
	digest, memory, err := webContentPendingBinding(job)
	job.memory = memory
	if err != nil {
		return empty, err
	}
	sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].SourceReference < dependencies[j].SourceReference })
	now := s.now()
	job.expires = now.Add(5 * time.Minute)
	job.info = WebAPIContentPreparation{PreparedJobID: operation, OperationID: operation, RequestDigest: digest, ContentSHA256: webContentDigest(job.body), ExpiresAt: job.expires.UTC().Format(time.RFC3339Nano), Dependencies: dependencies, AttachmentReferences: attachmentReferences}
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return empty, err
	}
	defer release()
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	if s.jobs.entries == nil {
		s.jobs.entries = map[string]webContentPendingJob{}
	}
	for id, old := range s.jobs.entries {
		if !old.finalizing && !now.Before(old.expires) {
			delete(s.jobs.entries, id)
			s.jobs.bytes -= old.memory
		}
	}
	if _, exists := s.jobs.entries[operation]; exists {
		return empty, webAPIError(409, "content_job_exists", "an import job with this operation already exists")
	}
	if len(s.jobs.entries) >= 16 || job.memory > WebAPIBackupExpandedLimit-s.jobs.bytes {
		return empty, webAPIError(429, "content_staging_full", "too many pending module bytes")
	}
	s.jobs.entries[operation] = job
	s.jobs.bytes += job.memory
	return job.info, nil
}

func (s *WebAPIContentService) pending(ctx context.Context, id string) (webContentPendingJob, error) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	job, ok := s.jobs.entries[id]
	if !ok || job.session != WebAPIRequestSessionID(ctx) || !s.now().Before(job.expires) {
		if ok && !s.now().Before(job.expires) {
			delete(s.jobs.entries, id)
			s.jobs.bytes -= job.memory
		}
		return webContentPendingJob{}, webAPIError(409, "content_preparation_invalid", "content preparation expired or belongs to another session")
	}
	if job.finalizing {
		return webContentPendingJob{}, webAPIError(409, "content_preparation_busy", "content preparation is committing")
	}
	return job, nil
}

func (s *WebAPIContentService) PendingSource(ctx context.Context, tx *WebAPIReadTx, jobID, reference string) (WebAPIProspectiveSource, error) {
	job, err := s.pending(ctx, jobID)
	if err != nil {
		return WebAPIProspectiveSource{}, err
	}
	source, ok := job.sources[reference]
	if !ok {
		return WebAPIProspectiveSource{}, webAPIError(404, "not_found", "source is not declared by the pending content")
	}
	var key *WebAPISourceResource
	if job.key.ID != "" {
		if _, err := tx.MitmModule(job.key.ID); err != nil {
			return WebAPIProspectiveSource{}, err
		}
		key = &WebAPISourceResource{Kind: "mitm-resource", ID: string(job.key.ID), Name: reference}
	}
	return WebAPIProspectiveSource{URL: source, Key: key, ExpectedRevision: job.revision, ImportJobID: jobID, SourceReference: reference, RequestDigest: job.info.RequestDigest}, nil
}

func (s *WebAPIContentService) usePending(ctx context.Context, selection WebAPIContentJobSelection, mutation WebAPIMutation, command string, key WebAPIContentKey) (context.Context, webContentPendingJob, error) {
	job, err := s.pending(ctx, selection.PreparedJobID)
	if err != nil {
		return ctx, job, err
	}
	if job.command != command || job.key != key || job.revision != mutation.ExpectedRevision || job.info.OperationID != mutation.OperationID || job.info.RequestDigest != selection.RequestDigest {
		return ctx, job, webAPIError(409, "content_preparation_mismatch", "content preparation does not match the command")
	}
	if len(selection.AuthorizationIDs) > 1000 {
		return ctx, job, webAPIError(413, "source_authorization_limit", "too many dependency authorizations")
	}
	grants := map[string]string{}
	ids := map[string]bool{}
	for _, id := range selection.AuthorizationIDs {
		if ids[id] {
			return ctx, job, webAPIError(400, "duplicate_source_authorization", "dependency authorization is repeated")
		}
		ids[id] = true
		grant, err := s.store.SourceGrant(ctx, id)
		if err != nil {
			return ctx, job, err
		}
		source, ok := job.sources[grant.SourceReference]
		if !ok || grant.Purpose != WebAPISourceMitmResourceImport || grant.ImportJobID != job.info.OperationID || grant.RequestDigest != job.info.RequestDigest {
			return ctx, job, webAPIError(409, "source_authorization_mismatch", "dependency authorization belongs to a different job")
		}
		_, digest, err := webSourceCanonical(source)
		if err != nil || digest != grant.URLDigest {
			return ctx, job, webAPIError(409, "source_authorization_mismatch", "dependency authorization URL does not match the parsed job")
		}
		if _, exists := grants[grant.SourceReference]; exists {
			return ctx, job, webAPIError(400, "duplicate_source_authorization", "dependency has multiple authorizations")
		}
		grants[grant.SourceReference] = id
	}
	return context.WithValue(ctx, webContentDependencyContextKey{}, webContentDependencyContext{job: job, grants: grants}), job, nil
}

func (s *WebAPIContentService) discardPending(ctx context.Context, id string) error {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	job, ok := s.jobs.entries[id]
	if !ok {
		return nil
	}
	if job.session != WebAPIRequestSessionID(ctx) {
		return webAPIError(403, "source_authorization_mismatch", "content preparation belongs to another session")
	}
	if job.finalizing {
		return webAPIError(409, "content_preparation_busy", "content preparation is committing")
	}
	delete(s.jobs.entries, id)
	s.jobs.bytes -= job.memory
	return nil
}

func (s *WebAPIContentService) PrepareWrite(ctx context.Context, request WebAPIContentPrepareWrite) (WebAPIRevision, WebAPIContentPreparation, error) {
	var result WebAPIContentPreparation
	if request.Key.Kind != WebAPIContentMitmModule {
		return "", result, webAPIError(400, "invalid_content_preparation", "dependency preparation is for module content only")
	}
	if err := webContentMetadataKind(request.Key, request.Metadata); err != nil {
		return "", result, err
	}
	var arguments map[string]string
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		m, err := tx.MitmModule(request.Key.ID)
		arguments = m.Arguments
		return err
	})
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "module changed; prepare again")
	}
	if request.Metadata != nil && request.Metadata.MitmModule != nil {
		arguments = request.Metadata.MitmModule.Arguments
	}
	result, err = s.storePending(ctx, webContentPendingJob{command: "content:write", revision: request.ExpectedRevision, key: request.Key, body: request.Bytes, metadata: request.Metadata, attachments: request.Attachments, detach: request.DetachReferences, arguments: arguments}, "")
	return revision, result, err
}

func (s *WebAPIContentService) PrepareArguments(ctx context.Context, request WebAPIContentPrepareArguments) (WebAPIRevision, WebAPIContentPreparation, error) {
	var result WebAPIContentPreparation
	var content WebAPIContent
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		content, err = tx.Content(WebAPIContentKey{Kind: WebAPIContentMitmModule, ID: request.ID})
		return err
	})
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "module changed; prepare again")
	}
	if request.Arguments == nil {
		return revision, result, webAPIError(400, "invalid_module_arguments", "arguments must be an object")
	}
	result, err = s.storePending(ctx, webContentPendingJob{command: "mitm-modules:arguments", revision: request.ExpectedRevision, key: content.Key, body: content.Bytes, arguments: request.Arguments}, "")
	return revision, result, err
}

func (s *WebAPIContentService) registerPending(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/content:prepare-write", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIContentPrepareWrite
		if err := WebAPIDecodeJSONLimit(w, r, &request, WebAPIMaxContentJSONBytes); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.PrepareWrite(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
	mux.HandleFunc("/api/v1/mitm-modules:prepare-arguments", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIContentPrepareArguments
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.PrepareArguments(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
	mux.HandleFunc("/api/v1/content:discard-preparation", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request struct {
			PreparedJobID string `json:"preparedJobId"`
		}
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if err := s.discardPending(r.Context(), request.PreparedJobID); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func webContentPendingEqual[T any](a, b T) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return reflect.DeepEqual(aa, bb)
}

func (s *WebAPIContentService) beginPendingCommit(ctx context.Context, job webContentPendingJob) (func(bool), error) {
	s.jobs.mu.Lock()
	stored, ok := s.jobs.entries[job.info.PreparedJobID]
	if !ok || stored.session != WebAPIRequestSessionID(ctx) || stored.info.RequestDigest != job.info.RequestDigest || stored.finalizing || !s.now().Before(stored.expires) {
		s.jobs.mu.Unlock()
		return nil, webAPIError(409, "content_preparation_invalid", "content preparation changed, expired, or is already committing")
	}
	stored.finalizing = true
	s.jobs.entries[job.info.PreparedJobID] = stored
	s.jobs.mu.Unlock()
	return func(success bool) {
		s.jobs.mu.Lock()
		defer s.jobs.mu.Unlock()
		current, ok := s.jobs.entries[job.info.PreparedJobID]
		if !ok {
			return
		}
		if success {
			delete(s.jobs.entries, job.info.PreparedJobID)
			s.jobs.bytes -= current.memory
		} else {
			current.finalizing = false
			s.jobs.entries[job.info.PreparedJobID] = current
		}
	}, nil
}
