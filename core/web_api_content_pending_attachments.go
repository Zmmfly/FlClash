//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

type WebAPIContentPrepareAttachment struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	RequestDigest    string         `json:"requestDigest"`
	SourceReference  string         `json:"sourceReference"`
	Bytes            []byte         `json:"bytes"`
}

type webContentAttachmentDescriptor struct {
	SourceReference string
	SHA256          string
	Size            int
}

// The immutable binding contains descriptors, never another base64 copy of the
// staged body or attachments. memory counts retained payload and binding bytes,
// not Go allocator overhead or a hard process RSS ceiling.
func webContentPendingBinding(job webContentPendingJob) (string, int, error) {
	attachments := make([]webContentAttachmentDescriptor, 0, len(job.attachments))
	memory := len(job.body)
	for _, item := range job.attachments {
		attachments = append(attachments, webContentAttachmentDescriptor{item.SourceReference, webContentDigest(item.Bytes), len(item.Bytes)})
		memory += len(item.Bytes)
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].SourceReference < attachments[j].SourceReference })
	var imported *WebAPIResourceImport
	if job.importRequest != nil {
		copy := *job.importRequest
		copy.Source.Bytes = nil
		copy.Attachments = nil
		imported = &copy
	}
	binding, err := webContentMarshal(struct {
		Command     string
		Revision    WebAPIRevision
		Key         WebAPIContentKey
		BodySHA     string
		Metadata    *WebAPIContentMetadata
		Attachments []webContentAttachmentDescriptor
		Detach      []string
		Arguments   map[string]string
		Import      *WebAPIResourceImport
	}{job.command, job.revision, job.key, webContentDigest(job.body), job.metadata, attachments, job.detach, job.arguments, imported})
	if err != nil {
		return "", 0, err
	}
	memory += len(binding)
	if memory > WebAPIBackupExpandedLimit {
		return "", 0, webAPIError(413, "content_too_large", "pending module exceeds its aggregate payload limit")
	}
	return webContentDigest(binding), memory, nil
}

func webContentValidatePendingAttachments(parsed webContentModule, attachments []WebAPIContentAttachment) ([]string, error) {
	declared := map[string]bool{}
	for _, reference := range parsed.Resources {
		if !strings.HasPrefix(reference, "http://") && !strings.HasPrefix(reference, "https://") {
			declared[reference] = true
		}
	}
	seen := map[string]bool{}
	for _, item := range attachments {
		if !declared[item.SourceReference] || seen[item.SourceReference] {
			return nil, webAPIError(400, "invalid_content_attachment", "attachment must match one declared local module resource")
		}
		if len(item.Bytes) > 16<<20 {
			return nil, webAPIError(413, "content_too_large", "module attachment exceeds 16 MiB")
		}
		seen[item.SourceReference] = true
	}
	references := make([]string, 0, len(declared))
	for reference := range declared {
		references = append(references, reference)
	}
	sort.Strings(references)
	return references, nil
}

func (s *WebAPIContentService) PrepareAttachment(ctx context.Context, jobID string, request WebAPIContentPrepareAttachment) (WebAPIRevision, WebAPIContentPreparation, error) {
	var result WebAPIContentPreparation
	if len(request.Bytes) > 16<<20 {
		return "", result, webAPIError(413, "content_too_large", "module attachment exceeds 16 MiB")
	}
	job, err := s.pending(ctx, jobID)
	if err != nil {
		return "", result, err
	}
	if request.ExpectedRevision != job.revision || request.RequestDigest != job.info.RequestDigest {
		return "", result, webAPIError(409, "content_preparation_mismatch", "attachment upload belongs to a different preparation")
	}
	if job.command != "content:write" && job.command != "mitm-modules:import" {
		return "", result, webAPIError(400, "invalid_content_attachment", "this preparation does not accept attachments")
	}
	allowed := false
	for _, reference := range job.info.AttachmentReferences {
		allowed = allowed || reference == request.SourceReference
	}
	if !allowed {
		return "", result, webAPIError(400, "invalid_content_attachment", "attachment is not declared by the prepared module")
	}
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		if job.key.ID != "" {
			_, err := tx.MitmModule(job.key.ID)
			return err
		}
		return nil
	})
	if err != nil {
		return revision, result, err
	}
	if revision != job.revision {
		return revision, result, webAPIError(409, "revision_conflict", "module changed; prepare again")
	}
	oldDigest := job.info.RequestDigest
	job.attachments = append([]WebAPIContentAttachment{}, job.attachments...)
	item := WebAPIContentAttachment{SourceReference: request.SourceReference, Bytes: append([]byte{}, request.Bytes...)}
	replaced := false
	for i := range job.attachments {
		if job.attachments[i].SourceReference == item.SourceReference {
			job.attachments[i] = item
			replaced = true
			break
		}
	}
	if !replaced {
		job.attachments = append(job.attachments, item)
	}
	job.info.RequestDigest, job.memory, err = webContentPendingBinding(job)
	if err != nil {
		return revision, result, err
	}
	// Session authority is acquired only for the short RAM publication, never
	// across parsing, network, a store lock, or an HTTP response write.
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return revision, result, err
	}
	defer release()
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	current, ok := s.jobs.entries[jobID]
	if !ok || current.session != WebAPIRequestSessionID(ctx) || !s.now().Before(current.expires) {
		return revision, result, webAPIError(409, "content_preparation_invalid", "content preparation expired or belongs to another session")
	}
	if current.finalizing {
		return revision, result, webAPIError(409, "content_preparation_busy", "content preparation is committing")
	}
	if current.info.RequestDigest != oldDigest {
		return revision, result, webAPIError(409, "content_preparation_mismatch", "content preparation changed during attachment upload")
	}
	if job.memory > WebAPIBackupExpandedLimit-(s.jobs.bytes-current.memory) {
		return revision, result, webAPIError(429, "content_staging_full", "too many pending module bytes")
	}
	s.jobs.bytes += job.memory - current.memory
	s.jobs.entries[jobID] = job
	return revision, job.info, nil
}

func (s *WebAPIContentService) registerPendingAttachments(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/content-preparations/{jobId}/attachments", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPIContentPrepareAttachment
		if err := WebAPIDecodeJSONLimit(w, r, &request, 24<<20); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.PrepareAttachment(r.Context(), r.PathValue("jobId"), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
}
