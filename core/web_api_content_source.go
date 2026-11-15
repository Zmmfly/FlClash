//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type WebAPISourceOptions struct {
	ResolveCustomImportJobSource func(context.Context, *WebAPIReadTx, string, string) (WebAPIProspectiveSource, error)
	ResolveRuntimeSourceIdentity func(context.Context, *WebAPIReadTx, WebAPISourceResource) (WebAPICommittedSource, error)
	ResolveUtilitySource         func(context.Context, *WebAPIReadTx, WebAPISourceResource) (string, error)
	WebAPISourceFetchOptions
	ResolveRuntimeSource   func(context.Context, *WebAPIReadTx, WebAPISourceResource) (string, error)
	ResolveImportJobSource func(context.Context, *WebAPIReadTx, string, string) (WebAPIProspectiveSource, error)
}

type webSourceStage struct {
	busy         bool
	domainDigest string
	request      WebAPISourcePrepare
	preparation  WebAPISourcePreparation
	sessionID    string
	expires      time.Time
}

type WebAPISourceService struct {
	store   *WebAPIStore
	options WebAPISourceOptions
	now     func() time.Time
	mu      sync.Mutex
	stages  map[string]webSourceStage
}

func NewWebAPISourceService(store *WebAPIStore, options WebAPISourceOptions) *WebAPISourceService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &WebAPISourceService{store: store, options: options, now: now, stages: map[string]webSourceStage{}}
}

func webSourceResourcePurpose(key *WebAPISourceResource, purpose WebAPISourcePurpose) bool {
	if purpose == WebAPISourceCustomProfileImport || purpose == WebAPISourceCustomProfileDependency {
		if key == nil {
			return false
		}
		if _, err := webAPIInteger(key.ID, true); err != nil {
			return false
		}
		if purpose == WebAPISourceCustomProfileImport {
			return key.Kind == "profile" && key.Name == ""
		}
		return key.Kind == "custom-import-dependency" && webAPISourceOpaqueKey(key.Name)
	}
	if webSourcePublicUtilityPurpose(purpose) {
		return key == nil
	}
	if webSourceDAVPurpose(purpose) {
		return key != nil && key.Kind == "dav" && key.ID == "settings" && key.Name == ""
	}
	if purpose == WebAPISourceIconImage {
		if key == nil || key.Kind != "icon-image" || key.Name != "" || len(key.ID) == 0 || len(key.ID) > 78 {
			return false
		}
		id, ok := new(big.Int).SetString(key.ID, 10)
		return ok && id.Sign() >= 0 && id.BitLen() <= 256 && id.String() == key.ID
	}
	if key == nil {
		return webSourceImportPurpose(purpose)
	}
	if webSourceImportPurpose(purpose) {
		return map[string]WebAPISourcePurpose{"profile": WebAPISourceProfileImport, "script": WebAPISourceScriptImport, "rule-provider": WebAPISourceRuleProviderImport, "mitm-module": WebAPISourceMitmModuleImport, "mitm-resource": WebAPISourceMitmResourceImport, "icon-set": WebAPISourceIconSetImport}[key.Kind] == purpose && key.ID != ""
	}
	allowed := map[string]WebAPISourcePurpose{"profile": WebAPISourceProfileRefresh, "script": WebAPISourceScriptRefresh, "rule-provider": WebAPISourceRuleProviderRefresh, "mitm-module": WebAPISourceMitmModuleRefresh, "mitm-resource": WebAPISourceMitmResourceRefresh, "icon-set": WebAPISourceIconSetRefresh, "external-provider": WebAPISourceExternalProviderRefresh, "geo": WebAPISourceGeoRefresh}
	return allowed[key.Kind] == purpose && key.ID != ""
}

func webSourceStoredURL(tx *WebAPIReadTx, key WebAPISourceResource) (string, error) {
	if _, err := webAPIInteger(key.ID, true); err != nil {
		return "", webAPIError(400, "invalid_id", "invalid source resource identity")
	}
	if key.Kind != "mitm-resource" && key.Name != "" {
		return "", webAPIError(400, "invalid_source_resource", "this source has no named child")
	}
	id := WebAPIID(key.ID)
	switch key.Kind {
	case "profile":
		p, err := tx.Profile(id)
		return p.URL, err
	case "script":
		p, err := tx.Script(id)
		if err != nil {
			return "", err
		}
		if p.URL != nil {
			return *p.URL, nil
		}
	case "rule-provider":
		p, err := tx.ClashProvider(id)
		return p.URL, err
	case "mitm-module":
		p, err := tx.MitmModule(id)
		if err != nil {
			return "", err
		}
		if p.URL != nil {
			return *p.URL, nil
		}
	case "mitm-resource":
		p, err := tx.MitmModule(id)
		if err != nil {
			return "", err
		}
		sources := []string{}
		for source, name := range p.Resources {
			if name == key.Name && (strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")) {
				sources = append(sources, source)
			}
		}
		sort.Strings(sources)
		if len(sources) > 0 {
			return sources[0], nil
		}
	case "icon-set":
		p, err := tx.IconSet(id)
		return p.URL, err
	default:
		return "", webAPIError(400, "invalid_source_resource", "unknown source resource kind")
	}
	return "", webAPIError(409, "resource_not_remote", "resource has no remote source")
}

type webSourceResolved struct {
	CommittedHeaders map[string][]string
	URL              string
	Key              *WebAPISourceResource
	RequestDigest    string
}

func webSourceOwnerExists(tx *WebAPIReadTx, key WebAPISourceResource) error {
	if _, err := webAPIInteger(key.ID, true); err != nil {
		return webAPIError(400, "invalid_id", "invalid source resource identity")
	}
	id := WebAPIID(key.ID)
	switch key.Kind {
	case "profile":
		_, err := tx.Profile(id)
		return err
	case "script":
		_, err := tx.Script(id)
		return err
	case "rule-provider":
		_, err := tx.ClashProvider(id)
		return err
	case "mitm-module", "mitm-resource":
		_, err := tx.MitmModule(id)
		return err
	case "icon-set":
		_, err := tx.IconSet(id)
		return err
	}
	return webAPIError(400, "invalid_source_resource", "invalid source resource kind")
}

func (s *WebAPISourceService) resolveInTx(ctx context.Context, tx *WebAPIReadTx, request WebAPISourcePrepare) (webSourceResolved, error) {
	var result webSourceResolved
	if !webSourcePurposeValid(request.Purpose) {
		return result, webAPIError(400, "invalid_source_purpose", "unknown source purpose")
	}
	if request.ImportJobID != "" {
		resolver := s.options.ResolveImportJobSource
		if request.Purpose == WebAPISourceCustomProfileDependency {
			resolver = s.options.ResolveCustomImportJobSource
		}
		if !webSourceDependencyPurpose(request.Purpose) || request.SourceReference == "" || request.Key != nil || request.URL != "" || resolver == nil {
			return result, webAPIError(400, "invalid_source_request", "pending dependency source is resolved only from its parsed job")
		}
		pending, err := resolver(ctx, tx, request.ImportJobID, request.SourceReference)
		if err != nil {
			return result, err
		}
		if pending.ExpectedRevision != request.ExpectedRevision {
			return result, webAPIError(409, "revision_conflict", "pending job revision changed")
		}
		if request.Purpose == WebAPISourceCustomProfileDependency {
			if !webSourceResourcePurpose(pending.Key, request.Purpose) || pending.Key.Name != request.SourceReference || pending.ImportJobID != request.ImportJobID || pending.SourceReference != request.SourceReference || !webAPISourceDigest(pending.RequestDigest) {
				return result, webAPIError(409, "source_target_changed", "custom dependency identity no longer matches its parsed job")
			}
			if err := webSourceCustomProfile(tx, pending.Key); err != nil {
				return result, err
			}
		} else if len(pending.CommittedHeaders) != 0 {
			return result, webAPIError(400, "invalid_source_headers", "this pending source does not declare committed headers")
		}
		return webSourceResolved{URL: pending.URL, Key: webSourceCloneKey(pending.Key), RequestDigest: pending.RequestDigest, CommittedHeaders: pending.CommittedHeaders}, nil
	}
	if webSourceDependencyPurpose(request.Purpose) || request.SourceReference != "" {
		return result, webAPIError(409, "source_import_job_required", "dependency requires a parsed pending job")
	}
	if !webSourceResourcePurpose(request.Key, request.Purpose) {
		return result, webAPIError(400, "invalid_source_purpose", "source purpose does not match the resource")
	}
	result.Key = webSourceCloneKey(request.Key)
	if webSourcePublicUtilityPurpose(request.Purpose) {
		if err := ValidateWebAPIUtilitySource(request.Purpose, request.URL); err != nil {
			return result, err
		}
		result.URL = request.URL
		return result, nil
	}
	if webSourceDAVPurpose(request.Purpose) {
		if request.URL != "" {
			return result, webAPIError(400, "invalid_source_request", "DAV sources accept only the committed settings identity")
		}
		settings, err := tx.Settings()
		if err != nil {
			return result, err
		}
		if settings.DAV == nil {
			return result, webAPIError(409, "dav_not_configured", "DAV is not configured")
		}
		if _, _, err = webDAVBase(*settings.DAV); err != nil {
			return result, err
		}
		result.URL, result.RequestDigest = settings.DAV.URI, webDAVTupleDigest(*settings.DAV)
		return result, nil
	}
	if webSourceImportPurpose(request.Purpose) {
		if request.URL == "" {
			return result, webAPIError(400, "invalid_source_request", "prospective source requires a URL")
		}
		if request.Purpose == WebAPISourceCustomProfileImport {
			if err := webSourceCustomProfile(tx, request.Key); err != nil {
				return result, err
			}
		}
		if request.Key != nil {
			if err := webSourceOwnerExists(tx, *request.Key); err != nil {
				return result, err
			}
		}
		result.URL = request.URL
		return result, nil
	}
	if request.Key == nil || request.URL != "" {
		return result, webAPIError(400, "invalid_source_request", "stored sources accept only a resource identity")
	}
	var err error
	if request.Key.Kind == "icon-image" {
		if s.options.ResolveUtilitySource == nil {
			return result, webAPIError(501, "utility_source_unavailable", "utility source resolver is not attached")
		}
		result.URL, err = s.options.ResolveUtilitySource(ctx, tx, *request.Key)
	} else if request.Key.Kind == "external-provider" || request.Key.Kind == "geo" {
		resolver := s.options.ResolveRuntimeSourceIdentity
		if resolver == nil {
			resolver = s.options.WebAPISourceFetchOptions.ResolveRuntimeSourceIdentity
		}
		if resolver != nil {
			identity, err := resolver(ctx, tx, *request.Key)
			result.URL = identity.URL
			result.CommittedHeaders = identity.CommittedHeaders
			return result, err
		}
		if s.options.ResolveRuntimeSource == nil {
			return result, webAPIError(501, "runtime_source_unavailable", "runtime source resolver is not attached")
		}
		result.URL, err = s.options.ResolveRuntimeSource(ctx, tx, *request.Key)
	} else {
		result.URL, err = webSourceStoredURL(tx, *request.Key)
	}
	return result, err
}

func (s *WebAPISourceService) resolve(ctx context.Context, request WebAPISourcePrepare) (WebAPIRevision, webSourceResolved, error) {
	var result webSourceResolved
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		result, err = s.resolveInTx(ctx, tx, request)
		return err
	})
	return revision, result, err
}

func (s *WebAPISourceService) Prepare(ctx context.Context, request WebAPISourcePrepare) (WebAPIRevision, WebAPISourcePreparation, error) {
	var result WebAPISourcePreparation
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return "", result, webAPIError(401, "unauthorized", "source preparation requires an authenticated session")
	}
	revision, resolved, err := s.resolve(ctx, request)
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "source preparation target changed")
	}
	source := resolved.URL
	domainDigest := resolved.RequestDigest
	u, digest, _, err := webSourceIdentity(source, resolved.CommittedHeaders)
	if err != nil {
		return revision, result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, private, err := webSourceResolve(ctx, u, s.options.WebAPISourceFetchOptions)
	if err != nil {
		return revision, result, err
	}
	if private && webSourcePublicUtilityPurpose(request.Purpose) {
		return revision, result, webAPIError(403, "source_address_forbidden", "utility sources require public addresses")
	}
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return revision, result, err
	}
	defer release()
	token, err := webBackupToken()
	if err != nil {
		return revision, result, err
	}
	now := s.now()
	expires := now.Add(5 * time.Minute)
	scheme, host, port := webSourceOrigin(u)
	result = WebAPISourcePreparation{PreparationID: token, ExpiresAt: expires.UTC().Format(time.RFC3339Nano), URLDigest: digest, Purpose: request.Purpose, Key: resolved.Key, NetworkView: "server", Scheme: scheme, Host: host, Port: port, Addresses: webSourceAddressStrings(addresses), RequiresPrivateNetworkConfirmation: private}
	if !private {
		return revision, result, nil
	}
	request.Key = webSourceCloneKey(request.Key)
	stagePreparation := result
	stagePreparation.Key = webSourceCloneKey(result.Key)
	stagePreparation.Addresses = append([]string(nil), result.Addresses...)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, stage := range s.stages {
		if !now.Before(stage.expires) {
			delete(s.stages, id)
		}
	}
	if len(s.stages) >= 128 {
		return revision, WebAPISourcePreparation{}, webAPIError(429, "source_staging_full", "too many pending source authorizations")
	}
	s.stages[token] = webSourceStage{request: request, preparation: stagePreparation, sessionID: sessionID, expires: expires, domainDigest: domainDigest}
	return revision, result, nil
}

func (s *WebAPISourceService) Authorize(ctx context.Context, request WebAPISourceAuthorize) (WebAPISourceAuthorizationReceipt, error) {
	replayed, err := s.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(*WebAPISourceAuthorizationTx) (any, error) { return nil, webContentNeedsPreparation })
	if !errors.Is(err, webContentNeedsPreparation) {
		return replayed, err
	}
	if request.Intent != "authorize-server-private-source" {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(400, "source_intent_required", "explicit server-network source authorization is required")
	}
	s.mu.Lock()
	stage, exists := s.stages[request.PreparationID]
	if exists && !stage.busy {
		stage.busy = true
		s.stages[request.PreparationID] = stage
	} else {
		exists = false
	}
	s.mu.Unlock()
	if exists {
		defer func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if current, ok := s.stages[request.PreparationID]; ok {
				current.busy = false
				s.stages[request.PreparationID] = current
			}
		}()
	}
	if !exists || !stage.preparation.RequiresPrivateNetworkConfirmation || !s.now().Before(stage.expires) || stage.sessionID != WebAPIRequestSessionID(ctx) || request.ExpectedRevision != stage.request.ExpectedRevision || request.URLDigest != stage.preparation.URLDigest {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(409, "source_preparation_invalid", "source preparation expired or changed")
	}
	revision, resolved, err := s.resolve(ctx, stage.request)
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	if revision != stage.request.ExpectedRevision {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(409, "revision_conflict", "source resource changed; prepare again")
	}
	if resolved.RequestDigest != stage.domainDigest || !reflect.DeepEqual(resolved.Key, stage.preparation.Key) {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(409, "source_target_changed", "pending content changed; prepare again")
	}
	source := resolved.URL
	u, digest, _, err := webSourceIdentity(source, resolved.CommittedHeaders)
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	if digest != request.URLDigest {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(409, "source_target_changed", "source URL changed; prepare again")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, _, err := webSourceResolve(ctx, u, s.options.WebAPISourceFetchOptions)
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	if !reflect.DeepEqual(stage.preparation.Addresses, webSourceAddressStrings(addresses)) {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(409, "source_target_changed", "source addresses changed; prepare again")
	}
	id, err := webBackupToken()
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	expires := s.now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	grant := WebAPISourceGrant{ID: id, Purpose: stage.request.Purpose, Key: resolved.Key, URLDigest: digest, Scheme: stage.preparation.Scheme, Host: stage.preparation.Host, Port: stage.preparation.Port, Addresses: stage.preparation.Addresses, PolicyVersion: WebAPISourcePolicyVersion, SessionID: stage.sessionID, ExpectedRevision: stage.request.ExpectedRevision, ExpiresAt: expires}
	grant.RequestDigest = stage.domainDigest
	if webSourceImportPurpose(stage.request.Purpose) {
		grant.ImportJobID = stage.request.ImportJobID
		if grant.ImportJobID == "" {
			grant.ImportJobID = stage.preparation.PreparationID
		}
		grant.SourceReference = stage.request.SourceReference
	}
	receipt, err := s.store.SourceAuthorizationTransaction(ctx, request.OperationID, request, func(tx *WebAPISourceAuthorizationTx) (any, error) {
		if !s.now().Before(stage.expires) {
			return nil, webAPIError(409, "source_preparation_invalid", "source preparation expired")
		}
		current, err := s.resolveInTx(ctx, tx.WebAPIReadTx, stage.request)
		if err != nil {
			return nil, err
		}
		currentDigest, err := webSourceIdentityDigest(current.URL, current.CommittedHeaders)
		if err != nil || currentDigest != digest || current.RequestDigest != stage.domainDigest || !reflect.DeepEqual(current.Key, grant.Key) {
			return nil, webAPIError(409, "source_target_changed", "source changed during authorization")
		}
		if err := tx.SaveSourceGrant(grant); err != nil {
			return nil, err
		}
		return WebAPISourceAuthorizationResult{AuthorizationID: id, ExpiresAt: expires, ImportOperationID: grant.ImportJobID}, nil
	})
	if err == nil {
		s.mu.Lock()
		delete(s.stages, request.PreparationID)
		s.mu.Unlock()
	}
	return receipt, err
}

func webSourceCustomProfile(tx *WebAPIReadTx, key *WebAPISourceResource) error {
	if key == nil {
		return webAPIError(400, "invalid_source_resource", "custom source requires a target profile")
	}
	profile, err := tx.Profile(WebAPIID(key.ID))
	if err != nil {
		return err
	}
	if profile.Type != "custom" {
		return webAPIError(409, "invalid_profile_state", "custom source requires an existing custom profile")
	}
	return nil
}

func webSourceCloneKey(key *WebAPISourceResource) *WebAPISourceResource {
	if key == nil {
		return nil
	}
	copy := *key
	return &copy
}

func WebAPIFetchSource(ctx context.Context, store *WebAPIStore, request WebAPISourceRequest, options WebAPISourceFetchOptions) (WebAPISourceFetchResult, error) {
	if err := webSourceValidateFetchRequest(request); err != nil {
		return WebAPISourceFetchResult{}, err
	}
	if store == nil {
		return WebAPISourceFetchResult{}, webAPIError(503, "source_store_unavailable", "source authority is unavailable")
	}
	request.Key = webSourceCloneKey(request.Key)
	_, _, headers, err := webSourceIdentity(request.URL, request.CommittedHeaders)
	if err != nil {
		return WebAPISourceFetchResult{}, err
	}
	request.CommittedHeaders = headers
	var grant *WebAPISourceGrant
	if request.AuthorizationID != "" {
		value, err := store.SourceGrant(ctx, request.AuthorizationID)
		if err != nil {
			return WebAPISourceFetchResult{}, err
		}
		grant = &value
	}
	check := func(ctx context.Context) error {
		_, err := store.View(ctx, func(tx *WebAPIReadTx) error {
			release, err := WebAPIAcquireCommitAuthorization(ctx)
			if err != nil {
				return err
			}
			defer release()
			if grant != nil {
				current, err := tx.SourceGrant(grant.ID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(current, *grant) {
					return webAPIError(409, "source_authorization_invalid", "source authorization changed before the request")
				}
			}
			if request.Purpose == WebAPISourceCustomProfileImport || request.Purpose == WebAPISourceCustomProfileDependency {
				if err := webSourceCustomProfile(tx, request.Key); err != nil {
					return err
				}
				if grant != nil {
					revision, err := webAPIRevision(tx.tx, "meta")
					if err != nil {
						return err
					}
					if revision != grant.ExpectedRevision {
						return webAPIError(409, "revision_conflict", "custom import source revision changed")
					}
				}
			}
			if request.Key == nil || webSourceImportPurpose(request.Purpose) {
				return nil
			}
			var source string
			var committed map[string][]string
			if request.Key.Kind == "external-provider" || request.Key.Kind == "geo" {
				if options.ResolveRuntimeSourceIdentity == nil {
					return webAPIError(501, "runtime_source_unavailable", "runtime source identity resolver is not attached")
				}
				var current WebAPICommittedSource
				current, err = options.ResolveRuntimeSourceIdentity(ctx, tx, *request.Key)
				source, committed = current.URL, current.CommittedHeaders
			} else if request.Purpose == WebAPISourceIconImage {
				source, err = ResolveWebAPIUtilitySource(ctx, tx, *request.Key)
			} else {
				source, err = webSourceStoredURL(tx, *request.Key)
			}
			if err != nil {
				return err
			}
			current, err := webSourceIdentityDigest(source, committed)
			if err != nil {
				return err
			}
			supplied, err := webSourceIdentityDigest(request.URL, request.CommittedHeaders)
			if err != nil {
				return err
			}
			if current != supplied {
				return webAPIError(409, "source_target_changed", "source request no longer matches its resource")
			}
			return nil
		})
		return err
	}
	if err := check(ctx); err != nil {
		return WebAPISourceFetchResult{}, err
	}
	return webFetchPurposeSourceChecked(ctx, request, grant, options, check)
}

func RegisterWebAPISourceRoutes(mux *http.ServeMux, store *WebAPIStore, options WebAPISourceOptions) *WebAPISourceService {
	s := NewWebAPISourceService(store, options)
	mux.HandleFunc("/api/v1/source-operations/", s.sourceOperationHTTP)
	mux.HandleFunc("/api/v1/sources:prepare", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPISourcePrepare
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, result, err := s.Prepare(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
	mux.HandleFunc("/api/v1/sources:authorize", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		var request WebAPISourceAuthorize
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := s.Authorize(r.Context(), request)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		webAPIWriteJSON(w, receipt.Body)
	})
	return s
}
