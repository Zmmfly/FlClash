//go:build webpanel && !android

package core

import (
	"container/list"
	"context"
	"net/http"
	"sync"
	"time"
)

const webImageCacheBytes = 64 << 20
const webImageCacheEntries = 256

type WebAPIImage struct {
	Bytes     []byte `json:"bytes"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
	SourceID  string `json:"sourceId"`
}

type WebAPIImageLoad struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}

type WebAPIImageRegister struct {
	WebAPIMutation
	URL    string `json:"url"`
	Intent string `json:"intent"`
}

type WebAPIImageRegistration struct {
	SourceID     string      `json:"sourceId"`
	URL          string      `json:"url"`
	LastAccessed WebAPIInt64 `json:"lastAccessed"`
}

type webImageEntry struct {
	key     string
	image   WebAPIImage
	expires time.Time
}

type webImageFlight struct {
	done  chan struct{}
	image WebAPIImage
	err   error
}

type webImageService struct {
	utility   *WebAPIUtilityService
	mu        sync.Mutex
	entries   map[string]*list.Element
	lru       *list.List
	bytes     int
	flights   map[string]*webImageFlight
	gate      chan struct{}
	responses chan struct{}
}

func newWebImageService(utility *WebAPIUtilityService) *webImageService {
	return &webImageService{utility: utility, entries: map[string]*list.Element{}, lru: list.New(), flights: map[string]*webImageFlight{}, gate: make(chan struct{}, 2), responses: make(chan struct{}, 2)}
}

func (s *webImageService) owned(ctx context.Context, raw string) (WebAPISourceResource, error) {
	key := webImageResourceKey(raw)
	if err := webImageRemoteURL(raw); err != nil {
		return key, err
	}
	_, err := s.utility.store.View(ctx, func(tx *WebAPIReadTx) error {
		current, err := ResolveWebAPIUtilitySource(ctx, tx, key)
		if err == nil && current != raw {
			return webAPIError(409, "source_target_changed", "icon resource source no longer matches")
		}
		return err
	})
	return key, err
}

func (s *webImageService) checkAuthorization(ctx context.Context, command WebAPIImageLoad, key WebAPISourceResource) error {
	if err := s.utility.remoteAllowed(); err != nil {
		return err
	}
	original, digest, err := webSourceCanonical(command.URL)
	if err != nil {
		return err
	}
	addresses, private, err := webSourceResolve(ctx, original, s.utility.options.SourceOptions)
	if err != nil {
		return err
	}
	var grant *WebAPISourceGrant
	if command.AuthorizationID != "" {
		value, err := s.utility.store.SourceGrant(ctx, command.AuthorizationID)
		if err != nil {
			return err
		}
		grant = &value
	}
	if private || grant != nil {
		return webSourceValidateGrant(ctx, WebAPISourceRequest{Purpose: WebAPISourcePurpose("icon-image"), Key: &key, URL: command.URL, AuthorizationID: command.AuthorizationID}, original, digest, addresses, grant, s.utility.now())
	}
	return nil
}

func (s *webImageService) load(ctx context.Context, command WebAPIImageLoad) (WebAPIImage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	key, err := s.owned(ctx, command.URL)
	if err != nil {
		return WebAPIImage{}, err
	}
	if err = s.checkAuthorization(ctx, command, key); err != nil {
		return WebAPIImage{}, err
	}
	cacheKey := WebAPIRequestSessionID(ctx) + ":" + key.ID + ":" + command.AuthorizationID
	s.mu.Lock()
	if item := s.entries[cacheKey]; item != nil {
		entry := item.Value.(webImageEntry)
		if s.utility.now().Before(entry.expires) {
			s.lru.MoveToFront(item)
			s.mu.Unlock()
			return entry.image, nil
		}
		s.remove(item)
	}
	if flight := s.flights[cacheKey]; flight != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return WebAPIImage{}, ctx.Err()
		case <-flight.done:
			return flight.image, flight.err
		}
	}
	if len(s.flights) >= 64 {
		s.mu.Unlock()
		return WebAPIImage{}, webAPIError(429, "image_busy", "too many pending icon image loads")
	}
	flight := &webImageFlight{done: make(chan struct{})}
	s.flights[cacheKey] = flight
	s.mu.Unlock()
	image, err := s.fetch(ctx, command, key)
	if err == nil {
		release, authorizationErr := WebAPIAcquireCommitAuthorization(ctx)
		if authorizationErr != nil {
			err = authorizationErr
			image = WebAPIImage{}
		} else {
			defer release()
		}
	}
	s.mu.Lock()
	if err == nil {
		s.remember(cacheKey, image)
	}
	flight.image, flight.err = image, err
	delete(s.flights, cacheKey)
	close(flight.done)
	s.mu.Unlock()
	return image, err
}

func (s *webImageService) remember(key string, image WebAPIImage) {
	if item := s.entries[key]; item != nil {
		s.remove(item)
	}
	entry := webImageEntry{key: key, image: image, expires: s.utility.now().Add(12 * time.Hour)}
	s.entries[key] = s.lru.PushFront(entry)
	s.bytes += len(image.Bytes)
	for s.bytes > webImageCacheBytes || s.lru.Len() > webImageCacheEntries {
		s.remove(s.lru.Back())
	}
}

func (s *webImageService) remove(item *list.Element) {
	entry := item.Value.(webImageEntry)
	s.bytes -= len(entry.image.Bytes)
	delete(s.entries, entry.key)
	s.lru.Remove(item)
}

func (s *webImageService) fetch(ctx context.Context, command WebAPIImageLoad, key WebAPISourceResource) (WebAPIImage, error) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return WebAPIImage{}, ctx.Err()
	}
	response, err := WebAPIFetchSource(ctx, s.utility.store, WebAPISourceRequest{Purpose: WebAPISourcePurpose("icon-image"), Key: &key, URL: command.URL, AuthorizationID: command.AuthorizationID}, s.utility.options.SourceOptions)
	if err != nil {
		return WebAPIImage{}, err
	}
	mediaType, err := webImageValidate(response.Bytes)
	if err != nil {
		return WebAPIImage{}, err
	}
	if _, err := s.owned(ctx, command.URL); err != nil {
		return WebAPIImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return WebAPIImage{}, err
	}
	return WebAPIImage{Bytes: response.Bytes, MediaType: mediaType, SHA256: webImageSHA(response.Bytes), Size: len(response.Bytes), SourceID: key.ID}, nil
}

func (s *webImageService) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/icon-images:register", func(w http.ResponseWriter, r *http.Request) {
		if !webUtilityPost(w, r) {
			return
		}
		var command WebAPIImageRegister
		if err := WebAPIDecodeJSON(w, r, &command); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		receipt, err := s.utility.store.CacheTransaction(r.Context(), command.WebAPIMutation, "icon-images:register", command, func(tx *WebAPICacheTx) (any, error) {
			if command.Intent != "preview-icon" {
				return nil, webAPIError(400, "icon_preview_intent_required", "explicit icon preview intent is required")
			}
			if err := webImageRemoteURL(command.URL); err != nil {
				return nil, err
			}
			record, err := webAPIPutIcon(tx, command.URL, s.utility.now(), false)
			return WebAPIImageRegistration{SourceID: webImageSourceID(record.URL), URL: record.URL, LastAccessed: record.LastAccessed}, err
		})
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		WebAPIWriteReceipt(w, receipt)
	})
	mux.HandleFunc("/api/v1/icon-images:load", func(w http.ResponseWriter, r *http.Request) {
		if !webUtilityPost(w, r) {
			return
		}
		var command WebAPIImageLoad
		if err := WebAPIDecodeJSON(w, r, &command); err != nil {
			WebAPIWriteError(w, err)
			return
		}
		result, err := s.load(r.Context(), command)
		if err != nil {
			WebAPIWriteError(w, err)
			return
		}
		revision, err := s.utility.revision(r.Context())
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
		select {
		case s.responses <- struct{}{}:
			defer func() { <-s.responses }()
		case <-r.Context().Done():
			WebAPIWriteError(w, webAPIError(503, "image_busy", "icon response serialization is busy"))
			return
		}
		WebAPIWriteSuccess(w, revision, result)
	})
}
