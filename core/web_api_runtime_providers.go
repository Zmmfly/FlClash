//go:build webpanel && !android

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"sort"
	"sync"

	bolt "github.com/metacubex/bbolt"
)

const webRuntimeProviderBucket = "runtime-provider-sources"
const webRuntimeRefreshProvider WebAPIRuntimeAction = "refresh-provider-source"

type WebAPIRuntimeProviderID string

type WebAPIRuntimeProviderSource struct {
	ID            WebAPIRuntimeProviderID `json:"id"`
	ProfileID     WebAPIID                `json:"profileId"`
	Name          string                  `json:"name"`
	Purpose       string                  `json:"purpose"`
	SourceVersion string                  `json:"sourceVersion"`
	Phase         string                  `json:"phase"`
}

type webRuntimeProviderDescriptor struct {
	Source     WebAPIRuntimeProviderSource     `json:"source"`
	URL        string                          `json:"url"`
	Headers    map[string][]string             `json:"headers,omitempty"`
	Format     string                          `json:"format"`
	Behavior   string                          `json:"behavior"`
	Evaluation *webRuntimeEvaluationProvenance `json:"evaluation,omitempty"`
}
type webRuntimePreparedProvider struct {
	OperationID string                       `json:"operationId,omitempty"`
	Descriptor  webRuntimeProviderDescriptor `json:"descriptor"`
	Bytes       []byte                       `json:"bytes"`
	Phase       string                       `json:"phase"`
}
type WebAPIRuntimeProviderRefresh struct {
	WebAPIMutation
	ID              WebAPIRuntimeProviderID `json:"id"`
	Purpose         string                  `json:"purpose"`
	AuthorizationID string                  `json:"authorizationId,omitempty"`
}
type webRuntimeDesiredProviderRefresh struct {
	Request        WebAPIRuntimeProviderRefresh `json:"request"`
	Descriptor     webRuntimeProviderDescriptor `json:"descriptor"`
	SchedulerEpoch *string                      `json:"schedulerEpoch,omitempty"`
}
type webRuntimeProviderRegistry struct {
	mu       sync.Mutex
	entries  map[WebAPIRuntimeProviderID]webRuntimeProviderDescriptor
	sessions sync.Map
}

func webRuntimeOpaqueID(value any) WebAPIRuntimeProviderID {
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return WebAPIRuntimeProviderID(new(big.Int).SetBytes(hash[:]).String())
}
func webRuntimeOpaqueValid(id WebAPIRuntimeProviderID) bool {
	if len(id) < 1 || len(id) > 78 || id[0] == '0' {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	value, ok := new(big.Int).SetString(string(id), 10)
	return ok && value.BitLen() <= 256
}

func webRuntimeProviderDescriptors(tx *WebAPIReadTx, id WebAPIID, storeID string) ([]webRuntimeProviderDescriptor, error) {
	source, err := webRuntimeProfileSource(tx, id)
	if err != nil {
		return nil, err
	}
	if source.Profile.ExtendType == "script" {
		return nil, webAPIError(409, "evaluation_required", "script providers require the frozen evaluated snapshot")
	}
	return webRuntimeProviderDescriptorsFromSource(source, storeID)
}

func webRuntimeProviderDescriptorsFromSource(source webRuntimeSource, storeID string) ([]webRuntimeProviderDescriptor, error) {
	id := source.Profile.ID
	document, err := webRuntimeDocument(source.Plain)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(source.Plain)
	sourceHash := hex.EncodeToString(hash[:])
	out := []webRuntimeProviderDescriptor{}
	for _, kind := range []string{"proxy-providers", "rule-providers"} {
		providers, ok := root[kind].(map[string]any)
		if !ok {
			continue
		}
		for name, value := range providers {
			mapping, ok := value.(map[string]any)
			if !ok {
				return nil, webAPIError(400, "invalid_provider", "provider source declaration is not a map")
			}
			if mapping["type"] != "http" {
				continue
			}
			url, ok := mapping["url"].(string)
			if !ok {
				return nil, webAPIError(400, "invalid_provider", "provider source has no URL")
			}
			if _, err = webContentURL(url); err != nil {
				return nil, err
			}
			encoded, _ := json.Marshal(mapping)
			declaration := sha256.Sum256(encoded)
			version := hex.EncodeToString(declaration[:])
			opaque := webRuntimeOpaqueID(struct {
				Store   string
				Profile WebAPIID
				Source  string
				Kind    string
				Name    string
				Version string
			}{storeID, id, sourceHash, kind, name, version})
			d := webRuntimeProviderDescriptor{Source: WebAPIRuntimeProviderSource{ID: opaque, ProfileID: id, Name: name, Purpose: kind, SourceVersion: version, Phase: "unprepared"}, URL: url, Evaluation: source.Evaluation}
			if format, ok := mapping["format"].(string); ok {
				d.Format = format
			}
			if behavior, ok := mapping["behavior"].(string); ok {
				d.Behavior = behavior
			}
			if headers, ok := mapping["header"].(map[string]any); ok {
				d.Headers = map[string][]string{}
				for key, value := range headers {
					switch value := value.(type) {
					case string:
						d.Headers[key] = []string{value}
					case []any:
						for _, item := range value {
							text, ok := item.(string)
							if !ok {
								return nil, webAPIError(400, "invalid_provider", "provider header values must be strings")
							}
							d.Headers[key] = append(d.Headers[key], text)
						}
					default:
						return nil, webAPIError(400, "invalid_provider", "provider header values must be strings")
					}
				}
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source.ID < out[j].Source.ID })
	return out, nil
}

func (tx *WebAPIReadTx) runtimeProvider(id WebAPIRuntimeProviderID) (webRuntimePreparedProvider, error) {
	var value webRuntimePreparedProvider
	if tx.tx.Bucket([]byte(webRuntimeProviderBucket)) == nil {
		return value, webAPIError(404, "provider_source_unavailable", "provider source is not prepared")
	}
	err := webAPIGet(tx.tx, webRuntimeProviderBucket, string(id), &value)
	return value, err
}
func (tx *WebAPIWriteTx) saveRuntimeProvider(value webRuntimePreparedProvider) error {
	if _, err := tx.tx.CreateBucketIfNotExists([]byte(webRuntimeProviderBucket)); err != nil {
		return err
	}
	return webAPIPut(tx.tx, webRuntimeProviderBucket, string(value.Descriptor.Source.ID), value)
}

func (rt *WebAPIRuntime) rememberProvider(d webRuntimeProviderDescriptor) {
	rt.providers.mu.Lock()
	defer rt.providers.mu.Unlock()
	if rt.providers.entries == nil {
		rt.providers.entries = map[WebAPIRuntimeProviderID]webRuntimeProviderDescriptor{}
	}
	if len(rt.providers.entries) >= 4096 {
		clear(rt.providers.entries)
	}
	rt.providers.entries[d.Source.ID] = d
}
func (rt *WebAPIRuntime) lookupProviderTx(ctx context.Context, tx *WebAPIReadTx, id WebAPIRuntimeProviderID) (webRuntimeProviderDescriptor, error) {
	var result webRuntimeProviderDescriptor
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !webRuntimeOpaqueValid(id) {
		return result, webAPIError(400, "invalid_provider_id", "provider source identities are opaque decimal strings")
	}
	rt.providers.mu.Lock()
	known, found := rt.providers.entries[id]
	rt.providers.mu.Unlock()
	if !found {
		stored, err := tx.runtimeProvider(id)
		if err == nil {
			known = stored.Descriptor
		} else {
			// GET enumeration is pure and never creates a persistent lookup index.
			// A bounded RAM hint may have been invalidated by an unrelated commit;
			// resolve the opaque identity against this exact authoritative snapshot.
			profiles, err := tx.Profiles()
			if err != nil {
				return result, err
			}
			for _, profile := range profiles {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				descriptors, err := webRuntimeProviderDescriptors(tx, profile.ID, rt.Events.storeID)
				if err != nil {
					continue
				}
				for _, descriptor := range descriptors {
					if descriptor.Source.ID == id {
						rt.rememberProvider(descriptor)
						return descriptor, nil
					}
				}
			}
			return result, webAPIError(404, "provider_source_unavailable", "provider source is not present in this snapshot")
		}
	}
	if known.Evaluation != nil {
		return rt.lookupEvaluatedProviderTx(ctx, tx, known)
	}
	current, err := webRuntimeProviderDescriptors(tx, known.Source.ProfileID, rt.Events.storeID)
	if err != nil {
		return result, err
	}
	for _, d := range current {
		if d.Source.ID == id {
			return d, nil
		}
	}
	return result, webAPIError(409, "source_target_changed", "provider source changed; reload its descriptor and authorization")
}

func (rt *WebAPIRuntime) lookupProvider(ctx context.Context, id WebAPIRuntimeProviderID) (webRuntimeProviderDescriptor, error) {
	var result webRuntimeProviderDescriptor
	_, err := rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		result, err = rt.lookupProviderTx(ctx, tx, id)
		return err
	})
	return result, err
}

func (rt *WebAPIRuntime) ResolveRuntimeSource(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (string, error) {
	source, err := rt.ResolveRuntimeSourceIdentity(ctx, tx, key)
	return source.URL, err
}

func (rt *WebAPIRuntime) ResolveRuntimeSourceIdentity(ctx context.Context, tx *WebAPIReadTx, key WebAPISourceResource) (WebAPICommittedSource, error) {
	if key.Kind == "geo" {
		descriptor, err := webRuntimeGeoByID(tx, rt.Events.storeID, WebAPIRuntimeProviderID(key.ID))
		return WebAPICommittedSource{URL: descriptor.URL}, err
	}
	if key.Kind != "external-provider" {
		return WebAPICommittedSource{}, webAPIError(400, "invalid_source_kind", "runtime source kind is not supported")
	}
	descriptor, err := rt.lookupProviderTx(ctx, tx, WebAPIRuntimeProviderID(key.ID))
	if err != nil {
		return WebAPICommittedSource{}, err
	}
	headers := map[string][]string{}
	for name, values := range descriptor.Headers {
		headers[name] = append([]string(nil), values...)
	}
	return WebAPICommittedSource{URL: descriptor.URL, CommittedHeaders: headers}, nil
}

func (rt *WebAPIRuntime) providerSourcesHTTP(w http.ResponseWriter, r *http.Request) {
	q, err := WebAPIQuery(r, "profileId", "evaluationId")
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if _, err = webAPIInteger(q["profileId"], true); err != nil {
		WebAPIWriteError(w, webAPIError(400, "invalid_id", "profileId is required"))
		return
	}
	var descriptors []webRuntimeProviderDescriptor
	var result []WebAPIRuntimeProviderSource
	rev, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error {
		var err error
		descriptors, err = rt.evaluatedProviderDescriptors(r.Context(), tx, WebAPIID(q["profileId"]), q["evaluationId"])
		if err != nil {
			return err
		}
		result = []WebAPIRuntimeProviderSource{}
		for _, d := range descriptors {
			if cached, err := tx.runtimeProvider(d.Source.ID); err == nil {
				d.Source.Phase = cached.Phase
			}
			result = append(result, d.Source)
		}
		return nil
	})
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	for _, d := range descriptors {
		rt.rememberProvider(d)
	}
	rt.profileAnswer(w, r, rev, result)
}

func (rt *WebAPIRuntime) refreshProviderHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIRuntimeProviderRefresh
	if err := WebAPIDecodeJSON(w, r, &p); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if receipt, found, err := rt.preflightApply(r.Context(), p.WebAPIMutation, "runtime/provider-sources:refresh", p); err != nil {
		WebAPIWriteError(w, err)
		return
	} else if found {
		WebAPIWriteReceipt(w, receipt)
		return
	}
	if rt.backend.SafeMode {
		WebAPIWriteError(w, webRuntimeSafeMode())
		return
	}
	d, err := rt.lookupProvider(r.Context(), p.ID)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if p.Purpose != d.Source.Purpose {
		WebAPIWriteError(w, webAPIError(400, "invalid_source_purpose", "provider source purpose does not match"))
		return
	}
	retained := context.WithoutCancel(r.Context())
	_, existed := rt.providers.sessions.LoadOrStore(p.OperationID, retained)
	receipt, err := rt.store.Transaction(r.Context(), p.WebAPIMutation, "runtime/provider-sources:refresh", p, func(tx *WebAPIWriteTx) (any, error) {
		current, err := rt.lookupProviderTx(r.Context(), tx.WebAPIReadTx, d.Source.ID)
		if err != nil {
			return nil, err
		}
		d = current
		previous, _ := tx.runtimeProvider(p.ID)
		previous.Descriptor, previous.Phase, previous.OperationID = d, "pending", p.OperationID
		if err = tx.saveRuntimeProvider(previous); err != nil {
			return nil, err
		}
		return tx.RecordRuntimeIntent(p.OperationID, webRuntimeRefreshProvider, webRuntimeDesiredProviderRefresh{Request: p, Descriptor: d})
	})
	if err != nil {
		if !existed {
			rt.providers.sessions.Delete(p.OperationID)
		}
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	} else if !existed {
		rt.providers.sessions.Delete(p.OperationID)
	}
	WebAPIWriteReceipt(w, receipt)
}

func (rt *WebAPIRuntime) refreshProvider(ctx context.Context, desired webRuntimeDesiredProviderRefresh) (any, error) {
	defer rt.providers.sessions.Delete(desired.Request.OperationID)
	original, ok := rt.providers.sessions.Load(desired.Request.OperationID)
	if !ok {
		return nil, webAPIError(401, "session_unavailable", "provider refresh has no live authorization context")
	}
	ctx = webRuntimeRestoreAuthorization(ctx, original.(context.Context))
	ctx = context.WithValue(ctx, webRuntimeAcceptedEvaluationKey{}, desired.Request.OperationID)
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	release()
	var current webRuntimeProviderDescriptor
	_, err = rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webRuntimeCheckBackgroundEpoch(tx, desired.SchedulerEpoch); err != nil {
			return err
		}
		var err error
		current, err = rt.lookupProviderTx(ctx, tx, desired.Request.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	fetched, err := WebAPIFetchSource(ctx, rt.store, WebAPISourceRequest{Purpose: WebAPISourceExternalProviderRefresh, Key: &WebAPISourceResource{Kind: "external-provider", ID: string(current.Source.ID)}, URL: current.URL, CommittedHeaders: current.Headers, AuthorizationID: desired.Request.AuthorizationID}, rt.workspace.options.SourceFetchOptions)
	if err != nil {
		return nil, err
	}
	data, err := rt.prepareProviderBytes(ctx, current, fetched.Bytes)
	if err != nil {
		return nil, err
	}
	// updateRuntime delegates the restored authorization to store.write; do not
	// acquire a second non-reentrant browser lease around that commit.
	err = rt.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		read := &WebAPIReadTx{tx}
		if err := webRuntimeCheckBackgroundEpoch(read, desired.SchedulerEpoch); err != nil {
			return err
		}
		if _, err := rt.lookupProviderTx(ctx, read, current.Source.ID); err != nil {
			return err
		}
		prior, err := read.runtimeProvider(current.Source.ID)
		if err != nil {
			return err
		}
		if prior.OperationID != desired.Request.OperationID || prior.Phase != "pending" {
			return webAPIError(409, "operation_superseded", "a newer provider operation superseded this refresh")
		}
		return (&WebAPIWriteTx{WebAPIReadTx: read}).saveRuntimeProvider(webRuntimePreparedProvider{Descriptor: current, Bytes: data, Phase: "ready", OperationID: desired.Request.OperationID})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": current.Source.ID, "sourceVersion": current.Source.SourceVersion, "prepared": true, "requiresApply": true}, nil
}

func (rt *WebAPIRuntime) invalidateProviderSources() {
	rt.providers.mu.Lock()
	clear(rt.providers.entries)
	rt.providers.mu.Unlock()
}

func (rt *WebAPIRuntime) sweepProviderSources(ctx context.Context) error {
	return rt.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(webRuntimeProviderBucket))
		if bucket == nil {
			return nil
		}
		var invalid []webRuntimePreparedProvider
		if err := bucket.ForEach(func(_, raw []byte) error {
			var record webRuntimePreparedProvider
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			if record.Descriptor.Evaluation != nil {
				if webRuntimeEvaluationInputs(&WebAPIReadTx{tx}, record.Descriptor.Source.ProfileID, record.Descriptor.Evaluation) != nil {
					invalid = append(invalid, record)
				}
				return nil
			}
			descriptors, err := webRuntimeProviderDescriptors(&WebAPIReadTx{tx}, record.Descriptor.Source.ProfileID, rt.Events.storeID)
			if err != nil {
				invalid = append(invalid, record)
				return nil
			}
			for _, d := range descriptors {
				if d.Source.ID == record.Descriptor.Source.ID {
					return nil
				}
			}
			invalid = append(invalid, record)
			return nil
		}); err != nil {
			return err
		}
		write := &WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}}
		for _, record := range invalid {
			if err := write.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: "external-provider", ID: string(record.Descriptor.Source.ID)}); err != nil {
				return err
			}
			if err := bucket.Delete([]byte(record.Descriptor.Source.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}
