//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/metacubex/mihomo/config"
)

type webRuntimeTopLevelCounts struct {
	Groups  int `json:"groups"`
	Proxies int `json:"proxies"`
	Rules   int `json:"rules"`
}

type WebAPIRuntimeAppliedCounts struct {
	RuntimeGeneration WebAPIRevision `json:"runtimeGeneration"`
	ActualConfig      string         `json:"actualConfig"`
	webRuntimeTopLevelCounts
}

type webRuntimeAppliedProvenance struct {
	mu           sync.RWMutex
	known        bool
	operationID  string
	actualConfig string
	counts       webRuntimeTopLevelCounts
	generation   WebAPIRevision
}

func (state *webRuntimeAppliedProvenance) clear() {
	state.mu.Lock()
	state.known = false
	state.operationID, state.actualConfig = "", ""
	state.mu.Unlock()
}

func (rt *WebAPIRuntime) recordAppliedProvenance(record webRuntimeRecord, phase WebAPIRuntimePhase, result any) {
	if phase == WebAPIRuntimeUnknown && (record.State.Action == webRuntimeApply || record.State.Action == webRuntimeReconcile) {
		rt.applied.clear()
		return
	}
	if record.State.Action != webRuntimeApply {
		if record.State.Action == webRuntimeReconcile && phase == WebAPIRuntimeApplied {
			if actual, ok := result.(WebAPIRuntimeApplyResult); ok && actual.Route.CoreEpoch > 0 {
				rt.applied.mu.Lock()
				rt.applied.generation = WebAPIRevision(strconv.FormatUint(actual.Route.CoreEpoch, 10))
				rt.applied.mu.Unlock()
			}
		}
		return
	}
	actual, ok := result.(WebAPIRuntimeApplyResult)
	if !ok || actual.ActualConfig != "candidate" && actual.ActualConfig != "default-fallback" {
		if phase != WebAPIRuntimePending {
			rt.applied.clear()
		}
		return
	}
	if actual.ActualConfig == "candidate" && phase != WebAPIRuntimeApplied {
		rt.applied.clear()
		return
	}
	var desired webRuntimeDesiredApply
	if json.Unmarshal(record.Desired, &desired) != nil {
		rt.applied.clear()
		return
	}
	counts := desired.Prepared.TopLevelCounts
	if actual.ActualConfig == "default-fallback" {
		// Count the exact built-in raw fallback, not the rejected candidate or
		// the previously installed config. This does not construct adapters.
		fallback := config.DefaultRawConfig()
		counts = webRuntimeTopLevelCounts{Groups: len(fallback.ProxyGroup), Proxies: len(fallback.Proxy), Rules: len(fallback.Rule)}
		rt.effect.needsReconcile.Store(false)
	}
	rt.applied.mu.Lock()
	rt.applied.known, rt.applied.operationID = true, record.State.OperationID
	rt.applied.actualConfig, rt.applied.counts = actual.ActualConfig, counts
	rt.applied.generation = ""
	if actual.Route.CoreEpoch > 0 {
		rt.applied.generation = WebAPIRevision(strconv.FormatUint(actual.Route.CoreEpoch, 10))
	}
	rt.applied.mu.Unlock()
}

func (rt *WebAPIRuntime) appliedSnapshot(ctx context.Context) (WebAPIRevision, WebAPIRuntimeAppliedCounts, string, error) {
	var result WebAPIRuntimeAppliedCounts
	var operationID string
	if rt.effect.transition.Load() || rt.effect.needsReconcile.Load() {
		return "", result, "", webAPIError(409, "applied_state_unknown", "the actual installed configuration has an unconfirmed runtime transition")
	}
	revision, err := rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		pending, err := tx.runtimeTransitionPending()
		if err != nil {
			return err
		}
		if pending {
			return webAPIError(409, "applied_state_unknown", "wait for the accepted runtime transition before reading installed configuration metadata")
		}
		rt.applied.mu.RLock()
		defer rt.applied.mu.RUnlock()
		if !rt.applied.known {
			return webAPIError(409, "applied_state_unavailable", "no confirmed installed configuration metadata is available")
		}
		result.ActualConfig, result.webRuntimeTopLevelCounts = rt.applied.actualConfig, rt.applied.counts
		operationID = rt.applied.operationID
		return nil
	})
	if err != nil {
		return revision, result, operationID, err
	}
	if rt.backend.State == nil {
		return revision, result, operationID, webRuntimeUnavailable()
	}
	state, err := rt.backend.State(ctx)
	if err != nil {
		return revision, result, operationID, err
	}
	if !state.Initialized {
		return revision, result, operationID, webAPIError(503, "core_unavailable", "core is not initialized")
	}
	result.RuntimeGeneration = state.RuntimeGeneration
	return revision, result, operationID, nil
}

func (rt *WebAPIRuntime) appliedCountsHTTP(w http.ResponseWriter, r *http.Request) {
	if err := webRuntimeNoQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if !rt.backendMu.TryRLock() {
		WebAPIWriteError(w, webAPIError(503, "runtime_busy", "a runtime effect is still in flight"))
		return
	}
	revision, result, _, err := rt.appliedSnapshot(r.Context())
	rt.backendMu.RUnlock()
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	rt.profileAnswer(w, r, revision, result)
}

func (rt *WebAPIRuntime) appliedPublicDefinition(ctx context.Context, name, providerName string) (map[string]any, bool, error) {
	_, snapshot, operationID, err := rt.appliedSnapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	if snapshot.ActualConfig == "default-fallback" {
		return nil, true, nil
	}
	var desired webRuntimeDesiredApply
	if _, err = rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		record, err := tx.runtimeRecord(operationID)
		if err != nil {
			return err
		}
		return json.Unmarshal(record.Desired, &desired)
	}); err != nil {
		return nil, false, err
	}
	data, err := rt.workspace.read(desired.Prepared.ConfigPath)
	if err != nil {
		return nil, false, err
	}
	document, err := webRuntimeDocument(data)
	if err != nil {
		return nil, false, err
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return nil, false, err
	}
	var raw any
	if providerName == "" {
		raw = root["proxies"]
	} else {
		providers, _ := root["proxy-providers"].(map[string]any)
		provider, _ := providers[providerName].(map[string]any)
		if provider == nil {
			return nil, false, webAPIError(409, "applied_state_unknown", "running provider does not match the confirmed apply provenance")
		}
		if provider["type"] == "inline" {
			raw = provider["payload"]
		} else {
			path, _ := provider["path"].(string)
			data, err = rt.workspace.read(path)
			if err != nil {
				return nil, false, err
			}
			document, err = webRuntimeDocument(data)
			if err != nil {
				return nil, false, err
			}
			root = map[string]any{}
			if err = document.Decode(&root); err != nil {
				return nil, false, err
			}
			raw = root["proxies"]
		}
	}
	definitions, err := webRuntimeDefinitionList(raw)
	if err != nil {
		return nil, false, err
	}
	for _, definition := range definitions {
		if definition["name"] == name {
			if err := webRuntimeDefinitions([]map[string]any{definition}); err != nil {
				return nil, false, err
			}
			return definition, true, nil
		}
	}
	return nil, false, nil
}

func (rt *WebAPIRuntime) proxyDefinition(ctx context.Context, name string) (map[string]any, error) {
	providerName := ""
	if rt.backend.ProxyDefinitionProvider != nil {
		var err error
		providerName, err = rt.backend.ProxyDefinitionProvider(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	definition, found, err := rt.appliedPublicDefinition(ctx, name, providerName)
	if err == nil && found {
		return definition, nil
	}
	if err != nil && rt.effect.needsReconcile.Load() {
		return nil, err
	}
	if rt.backend.ProxyDefinition == nil {
		return nil, webRuntimeUnavailable()
	}
	definition, backendErr := rt.backend.ProxyDefinition(ctx, name)
	if backendErr != nil {
		return nil, backendErr
	}
	if definition != nil {
		if stateful, validateErr := WebAPIValidateStatefulDefinition(definition); stateful || validateErr != nil {
			if err != nil {
				return nil, err
			}
			return nil, webAPIError(409, "applied_state_unknown", "stateful definition has no confirmed public apply provenance")
		}
		if err := webRuntimeDefinitions([]map[string]any{definition}); err != nil {
			return nil, err
		}
	}
	return definition, nil
}
