//go:build webpanel && !android

package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"time"
)

const (
	webCustomSourceRootPurpose       = WebAPISourceCustomProfileImport
	webCustomSourceDependencyPurpose = WebAPISourceCustomProfileDependency
	webCustomSourceTTL               = 5 * time.Minute
)

type WebAPICustomSourceOptions struct {
	SourceOptions WebAPISourceFetchOptions
	Now           func() time.Time
}

type WebAPICustomSourcePrepare struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	ProfileID        WebAPIID       `json:"profileId"`
	URL              string         `json:"url"`
	AuthorizationID  string         `json:"authorizationId,omitempty"`
}

type WebAPICustomSourceSummary struct {
	ProxyProviders  int `json:"proxyProviders"`
	RuleProviders   int `json:"ruleProviders"`
	Proxies         int `json:"proxies"`
	ProxyGroups     int `json:"proxyGroups"`
	Rules           int `json:"rules"`
	DroppedSubRules int `json:"droppedSubRules"`
}

type WebAPICustomSourcePreparation struct {
	WebAPIContentPreparation
	ProfileID WebAPIID                  `json:"profileId"`
	Summary   WebAPICustomSourceSummary `json:"summary"`
}

type webCustomSourceJob struct {
	info     WebAPICustomSourcePreparation
	session  string
	revision WebAPIRevision
	expires  time.Time
	source   []byte
	snapshot webCustomSourceSnapshot
	parsed   webCustomSourceParsed
	memory   int
}

type WebAPICustomSourceService struct {
	store   *WebAPIStore
	options WebAPICustomSourceOptions
	now     func() time.Time
	life    context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	jobs    map[string]*webCustomSourceJob
	busy    map[string]context.CancelFunc
	timers  map[string]*time.Timer
	extra   map[string]int
	bytes   int
	active  int
	work    sync.WaitGroup
}

type webCustomSourceBound struct {
	service   *WebAPICustomSourceService
	job       *webCustomSourceJob
	profiles  []webContentCustomProvider
	providers []webContentCustomProvider
}
type webCustomSourceBoundKey struct{}

func NewWebAPICustomSourceService(store *WebAPIStore, options WebAPICustomSourceOptions) *WebAPICustomSourceService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	if options.SourceOptions.Now == nil {
		options.SourceOptions.Now = now
	}
	life, cancel := context.WithCancel(context.Background())
	return &WebAPICustomSourceService{store: store, options: options, now: now, life: life, cancel: cancel, jobs: map[string]*webCustomSourceJob{}, busy: map[string]context.CancelFunc{}, timers: map[string]*time.Timer{}, extra: map[string]int{}}
}

func (s *WebAPICustomSourceService) start(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if WebAPIRequestSessionID(ctx) == "" {
		return ctx, nil, webAPIError(401, "unauthorized", "custom import requires an authenticated session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ctx, nil, webAPIError(503, "custom_import_closed", "custom import service is closed")
	}
	if s.active >= 4 {
		return ctx, nil, webAPIError(429, "content_busy", "too many custom import operations")
	}
	s.active++
	s.work.Add(1)
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(s.life, cancel)
	var once sync.Once
	return child, func() {
		once.Do(func() {
			stop()
			cancel()
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
			s.work.Done()
		})
	}, nil
}

func (s *WebAPICustomSourceService) removeLocked(id string) {
	if job, ok := s.jobs[id]; ok {
		delete(s.jobs, id)
		s.bytes -= job.memory + s.extra[id]
		delete(s.extra, id)
	}
	if timer := s.timers[id]; timer != nil {
		timer.Stop()
		delete(s.timers, id)
	}
	if cancel := s.busy[id]; cancel != nil {
		cancel()
		delete(s.busy, id)
	}
}

func (s *WebAPICustomSourceService) purgeLocked() {
	for id, job := range s.jobs {
		if !s.now().Before(job.expires) {
			s.removeLocked(id)
		}
	}
}

func (s *WebAPICustomSourceService) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		for id := range s.jobs {
			s.removeLocked(id)
		}
	}
	s.mu.Unlock()
	s.work.Wait()
}

func webCustomSourceOperation() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *WebAPICustomSourceService) Prepare(ctx context.Context, request WebAPICustomSourcePrepare) (WebAPIRevision, WebAPICustomSourcePreparation, error) {
	var result WebAPICustomSourcePreparation
	ctx, done, err := s.start(ctx)
	if err != nil {
		return "", result, err
	}
	defer done()
	if _, err := webAPIInteger(string(request.ExpectedRevision), false); err != nil {
		return "", result, webAPIError(400, "invalid_revision", "invalid custom import revision")
	}
	if _, err := webAPIInteger(string(request.ProfileID), true); err != nil {
		return "", result, webAPIError(400, "invalid_id", "invalid custom profile identity")
	}
	_, _, err = webSourceCanonical(request.URL)
	if err != nil {
		return "", result, err
	}
	revision, err := s.store.View(ctx, func(tx *WebAPIReadTx) error {
		profile, err := tx.Profile(request.ProfileID)
		if err != nil {
			return err
		}
		if profile.Type != "custom" || webAPIUnreadable(profile.WebAPIProfileInput) {
			return webAPIError(409, "invalid_profile_state", "custom import requires a readable custom profile")
		}
		return nil
	})
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "custom profile changed; prepare again")
	}
	operation, err := webCustomSourceOperation()
	if err != nil {
		return revision, result, err
	}
	key := &WebAPISourceResource{Kind: "profile", ID: string(request.ProfileID)}
	domainDigest := ""
	if request.AuthorizationID != "" {
		grant, err := s.store.SourceGrant(ctx, request.AuthorizationID)
		if err != nil {
			return revision, result, err
		}
		if grant.Purpose != webCustomSourceRootPurpose || !reflect.DeepEqual(grant.Key, key) || grant.ExpectedRevision != request.ExpectedRevision || grant.SourceReference != "" || !webAPIOperationID(grant.ImportJobID) {
			return revision, result, webAPIError(409, "source_authorization_mismatch", "custom source authorization belongs to another operation")
		}
		operation, domainDigest = grant.ImportJobID, grant.RequestDigest
	}
	s.mu.Lock()
	s.purgeLocked()
	_, exists := s.jobs[operation]
	full := len(s.jobs) >= 16 || s.bytes >= WebAPIBackupExpandedLimit
	s.mu.Unlock()
	if exists {
		return revision, result, webAPIError(409, "content_job_exists", "custom import operation already has an immutable preparation")
	}
	if full {
		return revision, result, webAPIError(429, "content_staging_full", "custom import staging is full")
	}
	fetched, err := WebAPIFetchSource(ctx, s.store, WebAPISourceRequest{Purpose: webCustomSourceRootPurpose, Key: key, URL: request.URL, AuthorizationID: request.AuthorizationID, ImportJobID: operation, RequestDigest: domainDigest}, s.options.SourceOptions)
	if err != nil {
		return revision, result, err
	}
	data, err := webContentDecompress(fetched.Bytes)
	if err != nil {
		return revision, result, err
	}
	var snapshot webCustomSourceSnapshot
	var parsed webCustomSourceParsed
	revision, err = s.store.View(ctx, func(tx *WebAPIReadTx) error {
		var e error
		snapshot, parsed, e = webCustomSourceReadSnapshot(ctx, tx, request.ProfileID, data)
		return e
	})
	if err != nil {
		return revision, result, err
	}
	if revision != request.ExpectedRevision {
		return revision, result, webAPIError(409, "revision_conflict", "custom import snapshot changed")
	}
	snapshotDigest, snapshotBytes, err := webCustomSourceSnapshotDigest(snapshot)
	if err != nil {
		return revision, result, err
	}
	binding, err := json.Marshal(struct {
		Request                                    WebAPICustomSourcePrepare
		Operation, Session, SourceSHA, SnapshotSHA string
		Parsed                                     webCustomSourceParsed
	}{request, operation, WebAPIRequestSessionID(ctx), webContentDigest(data), snapshotDigest, parsed})
	if err != nil {
		return revision, result, err
	}
	memory := len(data) + snapshotBytes + len(binding)
	if memory > WebAPIBackupExpandedLimit {
		return revision, result, webAPIError(413, "content_too_large", "custom import staging exceeds 128 MiB")
	}
	dependencies := []WebAPIContentDependency{}
	for _, dependency := range parsed.Dependencies {
		digest, err := webSourceIdentityDigest(dependency.URL, dependency.Headers)
		if err != nil {
			return revision, result, err
		}
		dependencies = append(dependencies, WebAPIContentDependency{SourceReference: dependency.Reference, URLDigest: digest})
	}
	expires := s.now().Add(webCustomSourceTTL)
	result = WebAPICustomSourcePreparation{WebAPIContentPreparation: WebAPIContentPreparation{PreparedJobID: operation, OperationID: operation, RequestDigest: webContentDigest(binding), ContentSHA256: webContentDigest(data), ExpiresAt: expires.UTC().Format(time.RFC3339Nano), Dependencies: dependencies}, ProfileID: request.ProfileID, Summary: WebAPICustomSourceSummary{ProxyProviders: len(parsed.Custom.ProxyProviders), RuleProviders: len(parsed.Custom.RuleProviders), Proxies: len(parsed.Custom.Proxies), ProxyGroups: len(parsed.Custom.ProxyGroups), Rules: len(parsed.Custom.Rules), DroppedSubRules: parsed.DroppedSubRules}}
	job := &webCustomSourceJob{info: result, session: WebAPIRequestSessionID(ctx), revision: revision, expires: expires, source: data, snapshot: snapshot, parsed: parsed, memory: memory}
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return revision, WebAPICustomSourcePreparation{}, err
	}
	s.mu.Lock()
	s.purgeLocked()
	if s.closed || ctx.Err() != nil {
		err = webAPIError(409, "content_preparation_invalid", "custom import preparation was canceled")
	} else if _, exists := s.jobs[operation]; exists {
		err = webAPIError(409, "content_job_exists", "custom import operation already has a preparation")
	} else if len(s.jobs) >= 16 || memory > WebAPIBackupExpandedLimit-s.bytes {
		err = webAPIError(429, "content_staging_full", "custom import staging is full")
	} else {
		s.jobs[operation] = job
		s.bytes += memory
		s.timers[operation] = time.AfterFunc(webCustomSourceTTL, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.jobs[operation] == job {
				s.removeLocked(operation)
			}
		})
	}
	s.mu.Unlock()
	release()
	if err != nil {
		return revision, WebAPICustomSourcePreparation{}, err
	}
	result, err = webContentCloneValue(result)
	return revision, result, err
}

func (s *WebAPICustomSourceService) PendingSource(ctx context.Context, tx *WebAPIReadTx, jobID, reference string) (WebAPIProspectiveSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	job := s.jobs[jobID]
	if s.closed || job == nil || job.session != WebAPIRequestSessionID(ctx) {
		return WebAPIProspectiveSource{}, webAPIError(409, "content_preparation_invalid", "custom import preparation expired or belongs to another session")
	}
	if s.busy[jobID] != nil {
		return WebAPIProspectiveSource{}, webAPIError(409, "content_preparation_busy", "custom import is being confirmed")
	}
	profile, err := tx.Profile(job.info.ProfileID)
	if err != nil {
		return WebAPIProspectiveSource{}, err
	}
	revision, err := webAPIRevision(tx.tx, "meta")
	if err != nil {
		return WebAPIProspectiveSource{}, err
	}
	if revision != job.revision || !webContentPendingEqual(profile, job.snapshot.Target) {
		return WebAPIProspectiveSource{}, webAPIError(409, "revision_conflict", "custom import target changed")
	}
	for _, dependency := range job.parsed.Dependencies {
		if dependency.Reference == reference {
			headers, err := WebAPIValidateCommittedSourceHeaders(dependency.Headers)
			if err != nil {
				return WebAPIProspectiveSource{}, err
			}
			return WebAPIProspectiveSource{URL: dependency.URL, Key: &WebAPISourceResource{Kind: "custom-import-dependency", ID: string(job.info.ProfileID), Name: reference}, ExpectedRevision: job.revision, ImportJobID: jobID, SourceReference: reference, RequestDigest: job.info.RequestDigest, CommittedHeaders: headers}, nil
		}
	}
	return WebAPIProspectiveSource{}, webAPIError(404, "not_found", "source is not declared by this custom import")
}

func (s *WebAPICustomSourceService) Discard(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	if job.session != WebAPIRequestSessionID(ctx) {
		return webAPIError(403, "source_authorization_mismatch", "custom import belongs to another session")
	}
	s.removeLocked(id)
	return nil
}

func (s *WebAPICustomSourceService) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/profiles:prepare-custom-import", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		var request WebAPICustomSourcePrepare
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
	mux.HandleFunc("/api/v1/profiles:discard-custom-import", func(w http.ResponseWriter, r *http.Request) {
		if !webContentPost(w, r) {
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
		defer controller.SetReadDeadline(time.Time{})
		var request struct {
			PreparedJobID string `json:"preparedJobId"`
		}
		if err := WebAPIDecodeJSON(w, r, &request); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		if err := s.Discard(r.Context(), request.PreparedJobID); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
