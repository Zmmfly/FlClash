//go:build webpanel && !android

package core

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type WebAPIUtilityOptions struct {
	SourceOptions WebAPISourceFetchOptions
	Now           func() time.Time
	RemoteEnabled bool
}

type webUtilityIPFlight struct {
	done   chan struct{}
	record WebAPIIPQualityRecord
	err    error
}

type webUtilityReleaseFlight struct {
	done    chan struct{}
	release WebAPIUtilityRelease
	err     error
}

type webUtilityIPCooldown struct {
	until    time.Time
	failures []WebAPIUtilityIPFailure
}

type WebAPIUtilityService struct {
	store         *WebAPIStore
	options       WebAPIUtilityOptions
	now           func() time.Time
	mu            sync.Mutex
	release       *WebAPIUtilityRelease
	releaseUntil  time.Time
	releaseFlight *webUtilityReleaseFlight
	ipFlights     map[string]*webUtilityIPFlight
	ipCooldowns   map[string]webUtilityIPCooldown
	images        *webImageService
}

func NewWebAPIUtilityService(store *WebAPIStore, options WebAPIUtilityOptions) *WebAPIUtilityService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	if options.SourceOptions.Now == nil {
		options.SourceOptions.Now = now
	}
	s := &WebAPIUtilityService{store: store, options: options, now: now, ipFlights: map[string]*webUtilityIPFlight{}, ipCooldowns: map[string]webUtilityIPCooldown{}}
	s.images = newWebImageService(s)
	return s
}

func (s *WebAPIUtilityService) remoteAllowed() error {
	if !s.options.RemoteEnabled {
		return webAPIError(409, "utility_remote_disabled", "remote utilities are disabled by the server safety policy")
	}
	return nil
}

func (s *WebAPIUtilityService) releaseLatest(ctx context.Context) (WebAPIUtilityRelease, error) {
	s.mu.Lock()
	if s.release != nil && s.now().Before(s.releaseUntil) {
		result := *s.release
		s.mu.Unlock()
		return result, nil
	}
	if pending := s.releaseFlight; pending != nil {
		s.mu.Unlock()
		select {
		case <-pending.done:
			return pending.release, pending.err
		case <-ctx.Done():
			return WebAPIUtilityRelease{}, ctx.Err()
		}
	}
	pending := &webUtilityReleaseFlight{done: make(chan struct{})}
	s.releaseFlight = pending
	s.mu.Unlock()
	result, err := s.fetchRelease(ctx)
	s.mu.Lock()
	if err == nil {
		s.release = &result
		s.releaseUntil = s.now().Add(15 * time.Minute)
	}
	pending.release, pending.err = result, err
	s.releaseFlight = nil
	close(pending.done)
	s.mu.Unlock()
	return result, err
}

func (s *WebAPIUtilityService) fetchRelease(ctx context.Context) (WebAPIUtilityRelease, error) {
	if err := s.remoteAllowed(); err != nil {
		return WebAPIUtilityRelease{}, err
	}
	request := WebAPISourceRequest{Purpose: WebAPISourcePurpose("app-release"), URL: webUtilityReleaseMetadataURL}
	response, err := WebAPIFetchSource(ctx, s.store, request, s.options.SourceOptions)
	if err != nil {
		return WebAPIUtilityRelease{}, err
	}
	return webUtilityParseRelease(response.Bytes, s.now())
}

func (s *WebAPIUtilityService) revision(ctx context.Context) (WebAPIRevision, error) {
	return s.store.View(ctx, func(*WebAPIReadTx) error { return nil })
}

func webUtilityPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WebAPIWriteError(w, webAPIError(405, "method_not_allowed", "POST is required"))
		return false
	}
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return false
	}
	return true
}

func RegisterWebAPIUtilityRoutes(mux *http.ServeMux, store *WebAPIStore, options WebAPIUtilityOptions) *WebAPIUtilityService {
	s := NewWebAPIUtilityService(store, options)
	mux.HandleFunc("/api/v1/app/release", func(w http.ResponseWriter, r *http.Request) {
		if !webAPIGetMethod(w, r) {
			return
		}
		if _, err := WebAPIQuery(r); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		result, err := s.releaseLatest(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, err := s.revision(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		release, err := WebAPIAcquireCommitAuthorization(r.Context())
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		defer release()
		WebAPIWriteSuccess(w, revision, result)
	})
	mux.HandleFunc("/api/v1/ip-quality:lookup", s.ipLookupHTTP)
	s.images.registerRoutes(mux)
	return s
}

func webUtilityIPFailureStatus(err error) string {
	var failure webUtilityIPFailureError
	if errors.As(err, &failure) {
		return failure.status
	}
	var api *WebAPIError
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &api) && (api.Code == "source_timeout" || api.Status == http.StatusGatewayTimeout) {
		return "timeout"
	}
	if errors.As(err, &api) && (api.Code == "source_rate_limited" || api.Status == http.StatusTooManyRequests) {
		return "rateLimited"
	}
	return "failed"
}
