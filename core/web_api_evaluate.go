//go:build webpanel && !android

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	WebAPIEvaluatePreviewBodyLimit = 72 << 20
	webEvaluateCacheBytes          = 64 << 20
	webEvaluateCacheEntries        = 8
	webEvaluateCacheTTL            = 2 * time.Minute
)

type WebAPIEvaluateRequest struct {
	ExpectedRevision WebAPIRevision `json:"expectedRevision"`
	ProfileID        WebAPIID       `json:"profileId"`
	ScriptID         WebAPIID       `json:"scriptId"`
}

type WebAPIScriptPreviewRequest struct {
	WebAPIEvaluateRequest
	ScriptText string          `json:"scriptText"`
	Config     json.RawMessage `json:"config"`
}

type WebAPIEvaluationCounts struct {
	Proxies int `json:"proxies"`
	Groups  int `json:"groups"`
	Rules   int `json:"rules"`
}

type WebAPIEvaluationSnapshot struct {
	EvaluationID string          `json:"evaluationId,omitempty"`
	ProfileID    WebAPIID        `json:"profileId"`
	ScriptID     WebAPIID        `json:"scriptId"`
	Revision     WebAPIRevision  `json:"readRevision"`
	Config       json.RawMessage `json:"config"`
	SHA256       string          `json:"sha256"`
	SourceDigest string          `json:"sourceDigest"`
	ScriptDigest string          `json:"scriptDigest"`
}

type WebAPIEvaluateResult struct {
	WebAPIEvaluationSnapshot
	Counts  WebAPIEvaluationCounts `json:"counts"`
	Console string                 `json:"console"`
	Preview bool                   `json:"preview"`
}

type webEvaluationCacheEntry struct {
	snapshot WebAPIEvaluationSnapshot
	session  string
	expires  time.Time
	sequence uint64
	cost     int
}

type WebAPIEvaluator struct {
	store    *WebAPIStore
	engine   *webEvaluateEngine
	mu       sync.Mutex
	cache    map[string]webEvaluationCacheEntry
	bytes    int
	sequence uint64
	closed   bool
	now      func() time.Time
}

var webEvaluationRegistry = struct {
	sync.Mutex
	services map[*WebAPIStore]*WebAPIEvaluator
}{services: map[*WebAPIStore]*WebAPIEvaluator{}}

func RegisterWebAPIEvaluateRoutes(mux *http.ServeMux, store *WebAPIStore) (*WebAPIEvaluator, error) {
	engine, err := newWebEvaluateEngine(context.Background())
	if err != nil {
		return nil, err
	}
	service := &WebAPIEvaluator{store: store, engine: engine, cache: map[string]webEvaluationCacheEntry{}, now: time.Now}
	webEvaluationRegistry.Lock()
	if webEvaluationRegistry.services[store] != nil {
		webEvaluationRegistry.Unlock()
		engine.Close()
		return nil, errors.New("evaluation service already registered")
	}
	webEvaluationRegistry.services[store] = service
	webEvaluationRegistry.Unlock()
	mux.Handle("/api/v1/profiles:evaluate", service)
	mux.Handle("/api/v1/scripts:preview", service)
	return service, nil
}

func (e *WebAPIEvaluator) Close() error {
	webEvaluationRegistry.Lock()
	if webEvaluationRegistry.services[e.store] == e {
		delete(webEvaluationRegistry.services, e.store)
	}
	webEvaluationRegistry.Unlock()
	e.mu.Lock()
	e.closed = true
	clear(e.cache)
	e.bytes = 0
	e.mu.Unlock()
	return e.engine.Close()
}

func webEvaluationUnavailable() error {
	return webAPIError(409, "evaluation_unavailable", "The evaluation expired or does not belong to this session and source revision")
}

// The caller supplies its actual snapshot revision and still owns the final apply CAS.
func ResolveWebAPIEvaluation(ctx context.Context, store *WebAPIStore, evaluationID string, profileID WebAPIID, expectedRevision WebAPIRevision) (WebAPIEvaluationSnapshot, error) {
	webEvaluationRegistry.Lock()
	e := webEvaluationRegistry.services[store]
	webEvaluationRegistry.Unlock()
	if e == nil || len(evaluationID) != 43 || WebAPIRequestSessionID(ctx) == "" || ctx.Err() != nil {
		return WebAPIEvaluationSnapshot{}, webEvaluationUnavailable()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.expireLocked()
	entry, ok := e.cache[evaluationID]
	if e.closed || !ok || entry.session != WebAPIRequestSessionID(ctx) || entry.snapshot.ProfileID != profileID || entry.snapshot.Revision != expectedRevision {
		return WebAPIEvaluationSnapshot{}, webEvaluationUnavailable()
	}
	e.sequence++
	entry.sequence = e.sequence
	e.cache[evaluationID] = entry
	result := entry.snapshot
	result.Config = append(json.RawMessage{}, result.Config...)
	return result, nil
}

func (e *WebAPIEvaluator) expireLocked() {
	now := e.now()
	for id, entry := range e.cache {
		if !entry.expires.After(now) {
			delete(e.cache, id)
			e.bytes -= entry.cost
		}
	}
}

func (e *WebAPIEvaluator) remember(ctx context.Context, snapshot WebAPIEvaluationSnapshot) (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", webAPIError(503, "evaluation_unavailable", "Evaluation provenance is unavailable")
	}
	session := WebAPIRequestSessionID(ctx)
	cost := len(snapshot.Config) + len(session) + 2048
	if session == "" || len(session) > 512 || cost > webEvaluateCacheBytes || ctx.Err() != nil {
		return "", webEvaluationUnavailable()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", webEvaluationUnavailable()
	}
	e.expireLocked()
	for len(e.cache) >= webEvaluateCacheEntries || e.bytes+cost > webEvaluateCacheBytes {
		var oldest string
		var sequence uint64
		for id, entry := range e.cache {
			if oldest == "" || entry.sequence < sequence {
				oldest, sequence = id, entry.sequence
			}
		}
		e.bytes -= e.cache[oldest].cost
		delete(e.cache, oldest)
	}
	id := base64.RawURLEncoding.EncodeToString(nonce[:])
	snapshot.EvaluationID = id
	snapshot.Config = append(json.RawMessage{}, snapshot.Config...)
	e.sequence++
	e.cache[id] = webEvaluationCacheEntry{snapshot: snapshot, session: session, expires: e.now().Add(webEvaluateCacheTTL), sequence: e.sequence, cost: cost}
	e.bytes += cost
	return id, nil
}

func webEvaluateHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func webEvaluateRealLabel(profile WebAPIProfile) string {
	if label := strings.TrimFunc(profile.Label, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' }); label != "" {
		return label
	}
	if u, err := url.Parse(profile.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" {
		return u.Hostname()
	}
	return string(profile.ID)
}

func (e *WebAPIEvaluator) source(ctx context.Context, request WebAPIEvaluateRequest) (script, input []byte, name, sourceDigest string, revision WebAPIRevision, err error) {
	if n, invalid := webAPIInteger(string(request.ExpectedRevision), false); invalid != nil || n < 0 {
		err = webAPIError(400, "invalid_revision", "expectedRevision is required")
		return
	}
	if _, invalid := webAPIInteger(string(request.ProfileID), true); invalid != nil {
		err = webAPIError(400, "invalid_id", "profileId is required")
		return
	}
	if _, invalid := webAPIInteger(string(request.ScriptID), true); invalid != nil {
		err = webAPIError(400, "invalid_id", "scriptId is required")
		return
	}
	revision, err = e.store.View(ctx, func(tx *WebAPIReadTx) error {
		meta, err := tx.Meta()
		if err != nil {
			return err
		}
		if meta.Revision != request.ExpectedRevision {
			conflict := webAPIError(409, "revision_conflict", "Evaluation inputs changed; reload the original source")
			conflict.Revision = meta.Revision
			return conflict
		}
		profile, err := tx.Profile(request.ProfileID)
		if err != nil {
			return err
		}
		if profile.Type == "custom" || profile.ExtendType != "script" || profile.ScriptID == nil || *profile.ScriptID != request.ScriptID {
			return webAPIError(409, "script_association_conflict", "The committed profile does not select this extension script")
		}
		if _, err = tx.Script(request.ScriptID); err != nil {
			return err
		}
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentScript, ID: request.ScriptID})
		if err != nil {
			return err
		}
		if len(content.Bytes) > webEvaluateMaxBytes {
			return webAPIError(413, "evaluation_input_limit", "The script exceeds its byte budget")
		}
		profileContent, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: request.ProfileID})
		if err != nil {
			return err
		}
		sourceDigest = webEvaluateHash(profileContent.Bytes)
		source, err := webRuntimeProfileSource(tx, request.ProfileID)
		if err != nil {
			return err
		}
		sourceDocument, err := webEvaluateParseSource(source.Plain)
		if err != nil {
			return err
		}
		raw, err := prepareDependencyDocument(sourceDocument, nil)
		if err != nil {
			return err
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var config map[string]any
		if err = json.Unmarshal(data, &config); err != nil {
			return err
		}
		if !webEvaluateSourceDeclares(sourceDocument.Content[0], "external-ui-url") {
			delete(config, "external-ui-url")
		}
		config["rules"] = config["rule"]
		delete(config, "rule")
		if config["proxy-providers"] == nil {
			config["proxy-providers"] = map[string]any{}
		}
		input, err = json.Marshal(config)
		if err != nil {
			return err
		}
		if len(input) > webEvaluateMaxBytes {
			return webAPIError(413, "evaluation_input_limit", "The parsed configuration exceeds its byte budget")
		}
		script = append([]byte{}, content.Bytes...)
		name = webEvaluateRealLabel(profile)
		return nil
	})
	return
}
