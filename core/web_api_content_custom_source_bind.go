//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"reflect"
	"sync"
)

func (s *WebAPICustomSourceService) reserve(ctx context.Context, job *webCustomSourceJob, size int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil || s.jobs[job.info.PreparedJobID] != job || !s.now().Before(job.expires) {
		return webAPIError(409, "content_preparation_invalid", "custom import was canceled or expired")
	}
	if size > WebAPIBackupExpandedLimit-s.bytes {
		return webAPIError(413, "content_too_large", "custom import dependencies exceed the staging limit")
	}
	s.bytes += size
	s.extra[job.info.PreparedJobID] += size
	return nil
}

func (s *WebAPICustomSourceService) Bind(ctx context.Context, request WebAPICustomProfileImportRequest, selection WebAPIContentJobSelection) (context.Context, WebAPICustomProfileImportDTO, func(bool), error) {
	var empty WebAPICustomProfileImportDTO
	noop := func(bool) {}
	if !webContentPendingEqual(request.Custom, empty) {
		return ctx, empty, noop, webAPIError(409, "content_preparation_mismatch", "prepared custom import does not accept replacement content")
	}
	if len(selection.AuthorizationIDs) > 1000 {
		return ctx, empty, noop, webAPIError(413, "source_authorization_limit", "too many dependency grants")
	}
	child, done, err := s.start(ctx)
	if err != nil {
		return ctx, empty, noop, err
	}
	child, cancel := context.WithCancel(child)
	s.mu.Lock()
	s.purgeLocked()
	job := s.jobs[selection.PreparedJobID]
	if job == nil || job.session != WebAPIRequestSessionID(ctx) || job.revision != request.ExpectedRevision || job.info.OperationID != request.OperationID || job.info.ProfileID != request.ID || job.info.RequestDigest != selection.RequestDigest {
		err = webAPIError(409, "content_preparation_mismatch", "custom import preparation does not match the command")
	} else if s.busy[selection.PreparedJobID] != nil {
		err = webAPIError(409, "content_preparation_busy", "custom import is already being confirmed")
	} else {
		s.busy[selection.PreparedJobID] = cancel
	}
	s.mu.Unlock()
	if err != nil {
		cancel()
		done()
		return ctx, empty, noop, err
	}
	var once sync.Once
	finish := func(bool) {
		once.Do(func() {
			s.mu.Lock()
			if s.jobs[selection.PreparedJobID] == job {
				s.removeLocked(selection.PreparedJobID)
			}
			s.mu.Unlock()
			cancel()
			done()
		})
	}
	fail := func(err error) (context.Context, WebAPICustomProfileImportDTO, func(bool), error) {
		finish(false)
		return ctx, empty, noop, err
	}
	grants := map[string]string{}
	seen := map[string]bool{}
	for _, id := range selection.AuthorizationIDs {
		if seen[id] {
			return fail(webAPIError(400, "duplicate_source_authorization", "source authorization is repeated"))
		}
		seen[id] = true
		grant, err := s.store.SourceGrant(child, id)
		if err != nil {
			return fail(err)
		}
		var dependency *webCustomSourceDependency
		for i := range job.parsed.Dependencies {
			if job.parsed.Dependencies[i].Reference == grant.SourceReference {
				dependency = &job.parsed.Dependencies[i]
				break
			}
		}
		if dependency == nil {
			return fail(webAPIError(409, "source_authorization_mismatch", "source is not declared by this custom import"))
		}
		key := &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(job.info.ProfileID), Name: dependency.Reference}
		digest, err := webSourceIdentityDigest(dependency.URL, dependency.Headers)
		if err != nil || grant.Purpose != webCustomSourceDependencyPurpose || grant.ImportJobID != request.OperationID || grant.RequestDigest != job.info.RequestDigest || grant.ExpectedRevision != job.revision || grant.SessionID != job.session || grant.URLDigest != digest || !reflect.DeepEqual(grant.Key, key) {
			return fail(webAPIError(409, "source_authorization_mismatch", "dependency authorization does not match the parsed job"))
		}
		if grants[dependency.Reference] != "" {
			return fail(webAPIError(400, "duplicate_source_authorization", "dependency has multiple authorizations"))
		}
		grants[dependency.Reference] = id
	}
	bound := webCustomSourceBound{service: s, job: job}
	for _, profiles := range []bool{true, false} {
		sources := job.parsed.Custom.RuleProviders
		if profiles {
			sources = job.parsed.Custom.ProxyProviders
		}
		for index, source := range sources {
			if err := child.Err(); err != nil {
				return fail(err)
			}
			data := []byte(source.Content)
			if source.URL != "" {
				var dependency *webCustomSourceDependency
				for i := range job.parsed.Dependencies {
					item := &job.parsed.Dependencies[i]
					if item.Profiles == profiles && item.Index == index {
						dependency = item
						break
					}
				}
				if dependency == nil {
					return fail(webAPIError(409, "content_preparation_mismatch", "custom provider was not derived from the parsed source"))
				}
				fetched, err := WebAPIFetchSource(child, s.store, WebAPISourceRequest{Purpose: webCustomSourceDependencyPurpose, Key: &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(job.info.ProfileID), Name: dependency.Reference}, URL: dependency.URL, CommittedHeaders: dependency.Headers, AuthorizationID: grants[dependency.Reference], ImportJobID: request.OperationID, SourceReference: dependency.Reference, RequestDigest: job.info.RequestDigest}, s.options.SourceOptions)
				if err != nil {
					return fail(err)
				}
				data = fetched.Bytes
			}
			if profiles || !bytes.HasPrefix(data, zstdMagic) {
				data, err = webContentDecompress(data)
				if err != nil {
					return fail(err)
				}
			}
			if err := s.reserve(child, job, len(data)); err != nil {
				return fail(err)
			}
			item, err := webCustomSourcePrepareProvider(source, data, profiles)
			if err != nil {
				return fail(err)
			}
			if profiles {
				bound.profiles = append(bound.profiles, item)
			} else {
				bound.providers = append(bound.providers, item)
			}
		}
	}
	if err := webCustomSourceGraph(job.snapshot, job.parsed.Custom, bound.profiles, bound.providers); err != nil {
		return fail(err)
	}
	effective, err := webContentCloneValue(job.parsed.Custom)
	if err != nil {
		return fail(err)
	}
	return context.WithValue(child, webCustomSourceBoundKey{}, bound), effective, finish, nil
}

func webCustomSourceBoundProviders(ctx context.Context, sources []WebAPICustomImportedProvider, profiles bool) ([]webContentCustomProvider, bool, error) {
	bound, ok := ctx.Value(webCustomSourceBoundKey{}).(webCustomSourceBound)
	if !ok {
		return nil, false, nil
	}
	original, prepared := bound.job.parsed.Custom.RuleProviders, bound.providers
	if profiles {
		original, prepared = bound.job.parsed.Custom.ProxyProviders, bound.profiles
	}
	if !webContentPendingEqual(sources, original) {
		return nil, true, webAPIError(409, "content_preparation_mismatch", "prepared provider payload changed")
	}
	return prepared, true, nil
}

func webCustomSourceValidateCommit(ctx context.Context, tx *WebAPIReadTx) error {
	bound, ok := ctx.Value(webCustomSourceBoundKey{}).(webCustomSourceBound)
	if !ok {
		return nil
	}
	s, job := bound.service, bound.job
	s.mu.Lock()
	valid := !s.closed && ctx.Err() == nil && s.jobs[job.info.PreparedJobID] == job && s.busy[job.info.PreparedJobID] != nil && s.now().Before(job.expires)
	s.mu.Unlock()
	if !valid || job.session != WebAPIRequestSessionID(ctx) {
		return webAPIError(409, "content_preparation_invalid", "custom import was canceled, expired, or changed session")
	}
	revision, err := webAPIRevision(tx.tx, "meta")
	if err != nil {
		return err
	}
	profile, err := tx.Profile(job.info.ProfileID)
	if err != nil {
		return err
	}
	if revision != job.revision || !webContentPendingEqual(profile, job.snapshot.Target) {
		return webAPIError(409, "revision_conflict", "custom import target changed")
	}
	return nil
}
