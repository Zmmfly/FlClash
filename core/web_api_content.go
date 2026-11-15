//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metacubex/mihomo/config"
)

type WebAPIContentOptions struct {
	Client        *http.Client
	SourceOptions WebAPISourceFetchOptions
	Now           func() time.Time
}

type WebAPIContentService struct {
	CustomSources *WebAPICustomSourceService
	store         *WebAPIStore
	sourceOptions WebAPISourceFetchOptions
	now           func() time.Time
	work          chan struct{}
	jobs          webContentPendingJobs
}

type WebAPIContentSummary struct {
	Key        WebAPIContentKey `json:"key"`
	MediaType  string           `json:"mediaType"`
	SHA256     string           `json:"sha256"`
	Size       int64            `json:"size"`
	ModifiedAt string           `json:"modifiedAt"`
}

type WebAPIContentResult struct {
	WebAPIContentSummary
	Bytes     []byte `json:"bytes"`
	Encrypted bool   `json:"encrypted"`
}

type WebAPIContentMutationResult struct {
	Content      WebAPIContentSummary `json:"content"`
	Profile      *WebAPIProfile       `json:"profile,omitempty"`
	Script       *WebAPIScript        `json:"script,omitempty"`
	RuleProvider *WebAPIClashProvider `json:"ruleProvider,omitempty"`
	MitmModule   *WebAPIMitmModule    `json:"mitmModule,omitempty"`
}

type WebAPIContentWrite struct {
	WebAPIMutation
	WebAPIContentJobSelection
	Key              WebAPIContentKey          `json:"key"`
	Bytes            []byte                    `json:"bytes,omitempty"`
	Metadata         *WebAPIContentMetadata    `json:"metadata,omitempty"`
	Attachments      []WebAPIContentAttachment `json:"attachments,omitempty"`
	DetachReferences []string                  `json:"detachReferences,omitempty"`
}
type WebAPIContentRefresh struct {
	WebAPIMutation
	Key             WebAPIContentKey       `json:"key"`
	Purpose         string                 `json:"purpose"`
	AuthorizationID string                 `json:"authorizationId,omitempty"`
	Metadata        *WebAPIContentMetadata `json:"metadata,omitempty"`
}
type WebAPIContentDecrypt struct {
	ID           WebAPIID `json:"id"`
	AgeSecretKey *string  `json:"ageSecretKey,omitempty"`
}
type WebAPIContentImportSource struct {
	Kind            string   `json:"kind"`
	Bytes           []byte   `json:"bytes,omitempty"`
	URL             string   `json:"url,omitempty"`
	Links           []string `json:"links,omitempty"`
	AuthorizationID string   `json:"authorizationId,omitempty"`
}
type WebAPIProfileImport struct {
	WebAPIMutation
	Label        string                    `json:"label"`
	AgeSecretKey string                    `json:"ageSecretKey,omitempty"`
	Source       WebAPIContentImportSource `json:"source"`
}
type WebAPIProfileClone struct {
	WebAPIMutation
	ID    WebAPIID `json:"id"`
	Label string   `json:"label,omitempty"`
}
type WebAPIMitmAttachment struct {
	WebAPIMutation
	ID              WebAPIID `json:"id"`
	SourceReference string   `json:"sourceReference"`
	Bytes           []byte   `json:"bytes"`
}
type WebAPIMitmDetach struct {
	WebAPIMutation
	ID              WebAPIID `json:"id"`
	SourceReference string   `json:"sourceReference"`
}
type WebAPIMitmArguments struct {
	WebAPIMutation
	WebAPIContentJobSelection
	ID        WebAPIID          `json:"id"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

func NewWebAPIContentService(store *WebAPIStore, options WebAPIContentOptions) *WebAPIContentService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	source := options.SourceOptions
	if options.Client != nil && source.LookupNetIP == nil && source.DialContext == nil {
		source.LookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, webAPIError(409, "source_policy_required", "content fetch requires the purpose-bound source policy")
		}
	}
	return &WebAPIContentService{store: store, sourceOptions: source, now: now, work: make(chan struct{}, 4)}
}

func webContentSummary(content WebAPIContent) WebAPIContentSummary {
	return WebAPIContentSummary{Key: content.Key, MediaType: content.MediaType, SHA256: content.SHA256, Size: int64(len(content.Bytes)), ModifiedAt: content.ModifiedAt}
}
func webContentResult(content WebAPIContent) WebAPIContentResult {
	return WebAPIContentResult{WebAPIContentSummary: webContentSummary(content), Bytes: content.Bytes, Encrypted: strings.HasPrefix(string(content.Bytes), "-----BEGIN AGE ENCRYPTED FILE-----") || strings.HasPrefix(string(content.Bytes), "age-encryption.org/v1")}
}

var webContentNeedsPreparation = errors.New("content preparation required")

func (s *WebAPIContentService) preflight(ctx context.Context, mutation WebAPIMutation, command string, request any) (WebAPIReceipt, bool, error) {
	receipt, err := s.store.Transaction(ctx, mutation, command, request, func(*WebAPIWriteTx) (any, error) { return nil, webContentNeedsPreparation })
	if errors.Is(err, webContentNeedsPreparation) {
		return WebAPIReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (s *WebAPIContentService) slot(ctx context.Context) (func(), error) {
	select {
	case s.work <- struct{}{}:
		return func() { <-s.work }, nil
	default:
		return nil, webAPIError(429, "content_busy", "too many content operations")
	}
}

func webContentOwner(tx *WebAPIReadTx, key WebAPIContentKey) error {
	if _, _, err := webAPIContentKey(key); err != nil {
		return err
	}
	switch key.Kind {
	case WebAPIContentProfile, WebAPIContentProfileAsset:
		_, err := tx.Profile(key.ID)
		return err
	case WebAPIContentScript:
		_, err := tx.Script(key.ID)
		return err
	case WebAPIContentRuleProvider:
		_, err := tx.ClashProvider(key.ID)
		return err
	case WebAPIContentMitmModule:
		_, err := tx.MitmModule(key.ID)
		return err
	case WebAPIContentMitmResource:
		m, err := tx.MitmModule(key.ID)
		if err != nil {
			return err
		}
		if !webContentModuleHasKey(m, key.Name) {
			return webAPIError(404, "not_found", "module resource key is not declared")
		}
	}
	return nil
}

func (s *WebAPIContentService) Read(ctx context.Context, key WebAPIContentKey) (WebAPIRevision, WebAPIContentResult, error) {
	var content WebAPIContent
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webContentOwner(tx, key); err != nil {
			return err
		}
		var err error
		content, err = tx.Content(key)
		return err
	})
	return revision, webContentResult(content), err
}

func (s *WebAPIContentService) Decrypt(ctx context.Context, request WebAPIContentDecrypt) (WebAPIRevision, WebAPIContentResult, error) {
	var content WebAPIContent
	var secret string
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		p, err := tx.Profile(request.ID)
		if err != nil {
			return err
		}
		secret = p.AgeSecretKey
		content, err = tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: request.ID})
		return err
	})
	if err != nil {
		return revision, WebAPIContentResult{}, err
	}
	if request.AgeSecretKey != nil {
		secret = *request.AgeSecretKey
	}
	content.Bytes, err = webContentDecompress(content.Bytes)
	if err == nil {
		content.Bytes, err = webContentDecrypt(content.Bytes, secret)
	}
	if err != nil {
		return revision, WebAPIContentResult{}, err
	}
	content.SHA256 = webContentDigest(content.Bytes)
	content.MediaType = "application/yaml"
	return revision, webContentResult(content), nil
}

func (s *WebAPIContentService) Import(ctx context.Context, request WebAPIProfileImport) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "profiles:import", request); done || err != nil {
		return receipt, err
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	var data []byte
	var subscription *WebAPISubscriptionInfo
	kind := "file"
	switch request.Source.Kind {
	case "file":
		if request.Source.URL != "" || len(request.Source.Links) != 0 {
			return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "file source cannot contain URLs or links")
		}
		data = request.Source.Bytes
	case "url":
		if len(request.Source.Bytes) != 0 || len(request.Source.Links) != 0 {
			return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "URL source cannot contain bytes or links")
		}
		if request.Source.AuthorizationID != "" {
			grant, err := s.store.SourceGrant(ctx, request.Source.AuthorizationID)
			if err != nil {
				return WebAPIReceipt{}, err
			}
			if grant.ImportJobID != request.OperationID {
				return WebAPIReceipt{}, webAPIError(409, "source_import_job_mismatch", "private import must use its prepared operationId")
			}
		}
		fetched, err := s.fetch(ctx, WebAPISourceRequest{Purpose: WebAPISourceProfileImport, URL: request.Source.URL, AuthorizationID: request.Source.AuthorizationID, ImportJobID: request.OperationID}, WebAPIMaxContentBytes)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		data = fetched.bytes
		subscription = fetched.subscription
		kind = "url"
	case "share":
		if request.Source.URL != "" || len(request.Source.Bytes) != 0 {
			return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "share source cannot contain URL or bytes")
		}
		data, err = webContentShareProfile(request.Source.Links)
		if err != nil {
			return WebAPIReceipt{}, err
		}
	default:
		return WebAPIReceipt{}, webAPIError(400, "invalid_import_source", "unknown profile import source")
	}
	data, err = webContentDecompress(data)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	raw, catalog, err := webContentProfile(data, request.AgeSecretKey)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	return s.store.Transaction(ctx, request.WebAPIMutation, "profiles:import", request, func(tx *WebAPIWriteTx) (any, error) {
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
			label = "Imported profile"
		}
		if !webAPICatalogName(label) {
			return nil, webAPIError(400, "invalid_label", "invalid profile label")
		}
		label = webContentUniqueName(label, taken)
		input := WebAPIProfileInput{Type: kind, Label: label, AgeSecretKey: request.AgeSecretKey, ExtendType: "standard", AutoUpdate: true, AutoUpdateDurationMillis: 86400000, SelectedMap: map[string]string{}, UnfoldSet: []string{}}
		if kind == "url" {
			input.URL = request.Source.URL
		}
		profile, err := tx.CreateProfile(input)
		if err != nil {
			return nil, err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		profile.LastUpdateDate = &now
		profile.SubscriptionInfo = subscription
		if err = tx.SaveProfile(profile); err != nil {
			return nil, err
		}
		if err = tx.SetProfileCatalog(profile.ID, catalog); err != nil {
			return nil, err
		}
		if err = webContentReconcileStateful(tx, profile.ID, raw); err != nil {
			return nil, err
		}
		if err = tx.PutContent(WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfile, ID: profile.ID}, MediaType: "application/yaml", Bytes: data}); err != nil {
			return nil, err
		}
		return profile, nil
	})
}

type webContentPrepared struct {
	consentRequired bool
	sourceKey       WebAPIContentKey
	content         WebAPIContent
	catalog         *WebAPIProfileCatalog
	rawProfile      *config.RawConfig
	ruleInfo        *RuleSetInfo
	module          *WebAPIMitmModule
	attachments     []WebAPIContent
	subscription    *WebAPISubscriptionInfo
}

func (s *WebAPIContentService) prepare(ctx context.Context, key WebAPIContentKey, data []byte) (webContentPrepared, error) {
	return s.prepareWithArguments(ctx, key, data, nil)
}

func (s *WebAPIContentService) prepareWithArguments(ctx context.Context, key WebAPIContentKey, data []byte, arguments map[string]string) (webContentPrepared, error) {
	p := webContentPrepared{sourceKey: key, content: WebAPIContent{Key: key, Bytes: data, SHA256: webContentDigest(data)}}
	if len(data) > WebAPIMaxContentBytes {
		return p, webAPIError(413, "content_too_large", "resource exceeds the content limit")
	}
	var profile WebAPIProfile
	var module WebAPIMitmModule
	var previous WebAPIContent
	var all []WebAPIContent
	var err error
	_, err = s.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webContentOwner(tx, key); err != nil {
			return err
		}
		switch key.Kind {
		case WebAPIContentProfile:
			profile, err = tx.Profile(key.ID)
		case WebAPIContentMitmModule:
			module, err = tx.MitmModule(key.ID)
			if err == nil {
				previous, _ = tx.Content(key)
				all = nil
			}
		}
		return err
	})
	if err != nil {
		return p, err
	}
	switch key.Kind {
	case WebAPIContentProfile:
		if webAPIUnreadable(profile.WebAPIProfileInput) {
			return p, webAPIError(409, "unreadable_overrides", "repair unreadable overrides before editing content")
		}
		if profile.Type == "custom" {
			return p, webAPIError(409, "custom_profile_content", "custom profiles are edited through their typed graph")
		}
		raw, catalog, err := webContentProfile(data, profile.AgeSecretKey)
		if err != nil {
			return p, err
		}
		p.catalog = &catalog
		p.rawProfile = raw
		p.content.MediaType = "application/yaml"
	case WebAPIContentScript:
		if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			return p, webAPIError(400, "invalid_script_content", "script must be UTF-8 text")
		}
		p.content.MediaType = "text/javascript"
	case WebAPIContentRuleProvider:
		info, _, err := webContentRuleSet(data, true)
		if err != nil {
			return p, err
		}
		p.ruleInfo = info
		p.content.MediaType = "application/octet-stream"
	case WebAPIContentMitmModule:
		return s.prepareModule(ctx, p, module, previous, all, arguments)
	case WebAPIContentProfileAsset:
		return p, webAPIError(409, "profile_asset_upload_required", "profile assets are immutable and require the purpose-bound upload command")
	case WebAPIContentMitmResource:
		if len(data) > 16<<20 {
			return p, webAPIError(413, "content_too_large", "module resource exceeds 16 MiB")
		}
		p.content.MediaType = "application/octet-stream"
		p.content.Key.Name = webContentModuleResourceKey(data)
	}
	return p, nil
}

func (s *WebAPIContentService) apply(tx *WebAPIWriteTx, p webContentPrepared) (any, error) {
	key := p.sourceKey
	previousNames := []string{}
	if key.Kind == WebAPIContentMitmModule || key.Kind == WebAPIContentMitmResource {
		module, err := tx.MitmModule(key.ID)
		if err != nil {
			return nil, err
		}
		for _, name := range module.Resources {
			previousNames = append(previousNames, name)
		}
	}
	if err := webContentOwner(tx.WebAPIReadTx, key); err != nil {
		return nil, err
	}
	result := WebAPIContentMutationResult{Content: webContentSummary(p.content)}
	now := s.now().UTC().Format(time.RFC3339Nano)
	switch key.Kind {
	case WebAPIContentProfile:
		profile, err := tx.Profile(key.ID)
		if err != nil {
			return nil, err
		}
		profile.LastUpdateDate = &now
		if p.subscription != nil {
			profile.SubscriptionInfo = p.subscription
		}
		if err = tx.SaveProfile(profile); err != nil {
			return nil, err
		}
		if err = tx.SetProfileCatalog(profile.ID, *p.catalog); err != nil {
			return nil, err
		}
		if err = webContentReconcileStateful(tx, profile.ID, p.rawProfile); err != nil {
			return nil, err
		}
		result.Profile = &profile
	case WebAPIContentScript:
		script, err := tx.Script(key.ID)
		if err != nil {
			return nil, err
		}
		script.LastUpdateTime = now
		if err = tx.SaveScript(script); err != nil {
			return nil, err
		}
		result.Script = &script
	case WebAPIContentRuleProvider:
		provider, err := tx.ClashProvider(key.ID)
		if err != nil {
			return nil, err
		}
		provider.Behavior = p.ruleInfo.Behavior
		provider.Format = p.ruleInfo.Format
		if err = tx.SaveClashProvider(provider); err != nil {
			return nil, err
		}
		result.RuleProvider = &provider
	case WebAPIContentMitmModule:
		if err := tx.SaveMitmModule(*p.module); err != nil {
			return nil, err
		}
		for _, content := range p.attachments {
			if err := tx.PutContent(content); err != nil {
				return nil, err
			}
		}
		result.MitmModule = p.module
	case WebAPIContentMitmResource:
		module, err := tx.MitmModule(key.ID)
		if err != nil {
			return nil, err
		}
		module.Enabled = false
		module.LastUpdateTime = now
		for source, name := range module.Resources {
			if name == key.Name {
				module.Resources[source] = p.content.Key.Name
			}
		}
		if err = tx.SaveMitmModule(module); err != nil {
			return nil, err
		}
		result.MitmModule = &module
	}
	if err := tx.PutContent(p.content); err != nil {
		return nil, err
	}
	if result.MitmModule != nil {
		if err := webContentPruneModuleResources(tx, *result.MitmModule, previousNames...); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *WebAPIContentService) Write(ctx context.Context, request WebAPIContentWrite) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "content:write", request); done || err != nil {
		return receipt, err
	}
	effective := request
	var job webContentPendingJob
	if request.PreparedJobID != "" {
		var err error
		ctx, job, err = s.usePending(ctx, request.WebAPIContentJobSelection, request.WebAPIMutation, "content:write", request.Key)
		if err != nil {
			return WebAPIReceipt{}, err
		}
		if len(request.Bytes) > 0 && webContentDigest(request.Bytes) != job.info.ContentSHA256 || request.Metadata != nil && !webContentPendingEqual(request.Metadata, job.metadata) || len(request.Attachments) > 0 && !webContentPendingEqual(request.Attachments, job.attachments) || len(request.DetachReferences) > 0 && !webContentPendingEqual(request.DetachReferences, job.detach) {
			return WebAPIReceipt{}, webAPIError(409, "content_preparation_mismatch", "content changed after preparation")
		}
		effective.Bytes = job.body
		effective.Metadata = job.metadata
		effective.Attachments = job.attachments
		effective.DetachReferences = job.detach
	} else if request.RequestDigest != "" || len(request.AuthorizationIDs) > 0 {
		return WebAPIReceipt{}, webAPIError(400, "invalid_content_preparation", "dependency authorizations require a pending content job")
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	prepared, err := s.prepareMetadata(ctx, effective.Key, effective.Bytes, effective.Metadata, effective.Attachments, effective.DetachReferences)
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
	receipt, err := s.store.Transaction(ctx, request.WebAPIMutation, "content:write", request, func(tx *WebAPIWriteTx) (any, error) {
		if request.PreparedJobID != "" && !s.now().Before(job.expires) {
			return nil, webAPIError(409, "content_preparation_invalid", "content preparation expired")
		}
		return s.applyMetadata(tx, prepared, effective.Metadata)
	})
	finish(err == nil)
	return receipt, err
}

func (s *WebAPIContentService) Refresh(ctx context.Context, request WebAPIContentRefresh) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "content:refresh", request); done || err != nil {
		return receipt, err
	}
	if request.Purpose != "source" && request.Purpose != "resource" {
		return WebAPIReceipt{}, webAPIError(400, "invalid_refresh_purpose", "unknown content refresh purpose")
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	var source string
	_, err = s.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webContentOwner(tx, request.Key); err != nil {
			return err
		}
		switch request.Key.Kind {
		case WebAPIContentProfile:
			p, err := tx.Profile(request.Key.ID)
			source = p.URL
			return err
		case WebAPIContentScript:
			p, err := tx.Script(request.Key.ID)
			if p.URL != nil {
				source = *p.URL
			}
			return err
		case WebAPIContentRuleProvider:
			p, err := tx.ClashProvider(request.Key.ID)
			source = p.URL
			return err
		case WebAPIContentMitmModule:
			p, err := tx.MitmModule(request.Key.ID)
			if p.URL != nil {
				source = *p.URL
			}
			return err
		case WebAPIContentMitmResource:
			p, err := tx.MitmModule(request.Key.ID)
			source = webContentModuleFirstSource(p, request.Key.Name)
			return err
		}
		return nil
	})
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if err := webContentMetadataKind(request.Key, request.Metadata); err != nil {
		return WebAPIReceipt{}, err
	}
	prospective := false
	if request.Key.Kind != WebAPIContentMitmResource {
		if changed, provided := webContentMetadataURL(request.Metadata); provided {
			prospective = changed != source
			source = changed
		}
	}
	if source == "" {
		return WebAPIReceipt{}, webAPIError(409, "resource_not_remote", "resource has no remote source")
	}
	if (request.Key.Kind == WebAPIContentMitmResource) != (request.Purpose == "resource") {
		return WebAPIReceipt{}, webAPIError(400, "invalid_refresh_purpose", "refresh purpose does not match the resource kind")
	}
	limit := int64(WebAPIMaxContentBytes)
	if request.Key.Kind == WebAPIContentMitmResource {
		limit = 16 << 20
	}
	purpose := map[WebAPIContentKind]WebAPISourcePurpose{WebAPIContentProfile: WebAPISourceProfileRefresh, WebAPIContentScript: WebAPISourceScriptRefresh, WebAPIContentRuleProvider: WebAPISourceRuleProviderRefresh, WebAPIContentMitmModule: WebAPISourceMitmModuleRefresh, WebAPIContentMitmResource: WebAPISourceMitmResourceRefresh}[request.Key.Kind]
	jobID := ""
	if prospective {
		purpose = map[WebAPIContentKind]WebAPISourcePurpose{WebAPIContentProfile: WebAPISourceProfileImport, WebAPIContentScript: WebAPISourceScriptImport, WebAPIContentRuleProvider: WebAPISourceRuleProviderImport, WebAPIContentMitmModule: WebAPISourceMitmModuleImport}[request.Key.Kind]
		jobID = request.OperationID
	}
	fetched, err := s.fetch(ctx, WebAPISourceRequest{Purpose: purpose, Key: &WebAPISourceResource{Kind: string(request.Key.Kind), ID: string(request.Key.ID), Name: request.Key.Name}, URL: source, AuthorizationID: request.AuthorizationID, ImportJobID: jobID}, limit)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if request.Key.Kind == WebAPIContentProfile {
		fetched.bytes, err = webContentDecompress(fetched.bytes)
		if err != nil {
			return WebAPIReceipt{}, err
		}
	}
	prepared, err := s.prepareMetadata(ctx, request.Key, fetched.bytes, request.Metadata, nil, nil)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	prepared.subscription = fetched.subscription
	return s.store.Transaction(ctx, request.WebAPIMutation, "content:refresh", request, func(tx *WebAPIWriteTx) (any, error) { return s.applyMetadata(tx, prepared, request.Metadata) })
}

func (s *WebAPIContentService) fetch(ctx context.Context, request WebAPISourceRequest, limit int64) (webContentFetched, error) {
	result, err := WebAPIFetchSource(ctx, s.store, request, s.sourceOptions)
	return webContentFetched{bytes: result.Bytes, subscription: result.Subscription}, err
}

func (s *WebAPIContentService) fetchDependency(ctx context.Context, source string, module WebAPIMitmModule, manifestDigest string) (webContentFetched, error) {
	if bound, ok := ctx.Value(webContentDependencyContextKey{}).(webContentDependencyContext); ok {
		reference := ""
		for ref, url := range bound.job.sources {
			if url == source {
				reference = ref
				break
			}
		}
		if reference == "" {
			return webContentFetched{}, webAPIError(409, "content_preparation_mismatch", "module dependency changed after preparation")
		}
		var prospective WebAPIProspectiveSource
		_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
			var err error
			prospective, err = s.PendingSource(ctx, tx, bound.job.info.PreparedJobID, reference)
			return err
		})
		if err != nil {
			return webContentFetched{}, err
		}
		if prospective.RequestDigest != bound.job.info.RequestDigest {
			return webContentFetched{}, webAPIError(409, "content_preparation_mismatch", "module dependency preparation changed before fetch")
		}
		result, err := WebAPIFetchSource(ctx, s.store, WebAPISourceRequest{Purpose: WebAPISourceMitmResourceImport, Key: prospective.Key, URL: source, AuthorizationID: bound.grants[reference], ImportJobID: bound.job.info.OperationID, SourceReference: reference, RequestDigest: bound.job.info.RequestDigest}, s.sourceOptions)
		return webContentFetched{bytes: result.Bytes, subscription: result.Subscription}, err
	}
	// A new declared child is a prospective import; an existing content key
	// uses its exact committed manifest binding. Neither inherits a parent
	// grant or browser credentials. Internal/background authority is passed
	// unchanged to the same shared source policy and remains public-only.
	reference := "dependency-" + webContentDigest([]byte(source))[:32]
	request := WebAPISourceRequest{Purpose: WebAPISourceMitmResourceImport, URL: source, SourceReference: reference, RequestDigest: manifestDigest}
	if module.ID != "" {
		request.Key = &WebAPISourceResource{Kind: "mitm-resource", ID: string(module.ID), Name: reference}
		if name := module.Resources[source]; name != "" && webContentModuleFirstSource(module, name) == source {
			request.Purpose = WebAPISourceMitmResourceRefresh
			request.Key.Name = name
			request.SourceReference, request.RequestDigest = "", ""
		}
	}
	result, err := WebAPIFetchSource(ctx, s.store, request, s.sourceOptions)
	return webContentFetched{bytes: result.Bytes, subscription: result.Subscription}, err
}

func (s *WebAPIContentService) prepareModule(ctx context.Context, p webContentPrepared, module WebAPIMitmModule, previous WebAPIContent, all []WebAPIContent, arguments map[string]string) (webContentPrepared, error) {
	data := p.content.Bytes
	oldArguments := module.Arguments
	if arguments != nil {
		module.Arguments = arguments
	}
	derived, err := webContentParseModule(data, module.Arguments)
	if err != nil {
		return p, err
	}
	resources := map[string]string{}
	for source, key := range module.Resources {
		if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
			resources[source] = key
		}
	}
	totalBytes := len(data)
	for _, source := range derived.Resources {
		if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
			continue
		}
		fetched, err := s.fetchDependency(ctx, source, module, p.content.SHA256)
		if err != nil {
			if webContentMayKeepCachedResource(err) {
				if cached, cacheErr := s.cachedModuleResource(ctx, module.ID, module.Resources[source]); cacheErr == nil {
					resources[source] = cached.Key.Name
					continue
				}
			}
			return p, err
		}
		totalBytes += len(fetched.bytes)
		if totalBytes > WebAPIBackupExpandedLimit {
			return p, webAPIError(413, "content_too_large", "module resources exceed the operation aggregate limit")
		}
		name := webContentModuleResourceKey(fetched.bytes)
		resources[source] = name
		p.attachments = append(p.attachments, WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: module.ID, Name: name}, Bytes: fetched.bytes, MediaType: "application/octet-stream", SHA256: webContentDigest(fetched.bytes)})
	}
	before, _ := webContentParseModule(previous.Bytes, oldArguments)
	if len(derived.Scripts) > 0 && (len(previous.Bytes) == 0 || webContentModuleScriptDigest(before, module.Resources) != webContentModuleScriptDigest(derived, resources)) {
		module.Enabled = false
		p.consentRequired = true
	}
	if derived.Name != "" {
		module.Name = derived.Name
	}
	module.Description = derived.Description
	module.System = derived.System
	module.Scripts = int32(len(derived.Scripts))
	module.Resources = resources
	module.LastUpdateTime = s.now().UTC().Format(time.RFC3339Nano)
	p.module = &module
	p.content.MediaType = "text/plain"

	return p, nil
}

func webContentMayKeepCachedResource(err error) bool {
	var api *WebAPIError
	return errors.As(err, &api) && (api.Code == "source_fetch_failed" || api.Code == "source_dns_failed")
}

func (s *WebAPIContentService) cachedModuleResource(ctx context.Context, id WebAPIID, name string) (WebAPIContent, error) {
	var content WebAPIContent
	if name == "" {
		return content, webAPIError(404, "not_found", "module has no cached resource")
	}
	_, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		content, err = tx.Content(WebAPIContentKey{Kind: WebAPIContentMitmResource, ID: id, Name: name})
		return err
	})
	return content, err
}
