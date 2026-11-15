//go:build webpanel && !android

package core

import (
	"context"
	"maps"
	"net/http"
)

type WebAPIRuntimeProxySnapshot struct {
	ProxiesData
	RuntimeGeneration WebAPIRevision `json:"runtimeGeneration"`
	ProfileID         *WebAPIID      `json:"profileId"`
}

func webRuntimeSelectionMembers(tx *WebAPIReadTx, profile WebAPIProfile, group string) ([]string, bool, error) {
	if profile.Type == "custom" {
		return WebAPICatalogGroupMemberNames(tx, profile.ID, group)
	}
	source, err := webRuntimeProfileSource(tx, profile.ID)
	if err != nil {
		return nil, false, err
	}
	_, raw, err := webRuntimeBase(source.Plain)
	if err != nil {
		return nil, false, err
	}
	graph, err := buildDependencyGraph(raw)
	if err != nil {
		return nil, false, err
	}
	return webAPICatalogGroupMembers(graph, group)
}

func (rt *WebAPIRuntime) recordSelection(ctx context.Context, tx *WebAPIWriteTx, p WebAPIRuntimeSelectRequest, state WebAPIRuntimeCoreState) (any, error) {
	settings, err := tx.Settings()
	if err != nil {
		return nil, err
	}
	if settings.CurrentProfileID == nil {
		return nil, webAPIError(409, "no_active_profile", "selection requires a committed current profile")
	}
	profile, err := tx.Profile(*settings.CurrentProfileID)
	if err != nil {
		return nil, err
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return nil, webAPIError(409, "profile_quarantined", "stored profile overrides are unreadable")
	}
	if rt.effect.transition.Load() {
		return nil, webAPIError(409, "runtime_transition_pending", "wait for the accepted runtime transition before selecting")
	}
	if rt.effect.needsReconcile.Load() {
		return nil, webAPIError(409, "runtime_reconciliation_required", "reconcile the uncertain runtime state before selecting")
	}
	pending, err := tx.runtimeTransitionPending()
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, webAPIError(409, "runtime_transition_pending", "wait for the accepted runtime transition before selecting")
	}
	if !webAPIOwner(state.ProfileID, settings.CurrentProfileID) {
		return nil, webAPIError(409, "profile_not_applied", "the committed profile is not the applied runtime profile")
	}
	if state.RuntimeGeneration != p.RuntimeGeneration {
		return nil, webAPIError(409, "runtime_generation_conflict", "the runtime generation changed; reload the proxy snapshot")
	}
	var members []string
	var ready bool
	if profile.ExtendType == "script" && profile.Type != "custom" {
		members, err = rt.scriptSelectionMembers(ctx, tx.WebAPIReadTx, profile, p.RuntimeGeneration)
		ready = err == nil
	} else {
		members, ready, err = webRuntimeSelectionMembers(tx.WebAPIReadTx, profile, p.GroupName)
	}
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, webAPIError(409, "catalog_unavailable", "provider members are not prepared")
	}
	if *p.ProxyName != "" && !webAPIContains(members, *p.ProxyName) {
		return nil, webAPIError(409, "invalid_selection", "the target is not a selectable member of this group")
	}
	profile.SelectedMap = maps.Clone(profile.SelectedMap)
	if profile.SelectedMap == nil {
		profile.SelectedMap = map[string]string{}
	}
	profile.SelectedMap[p.GroupName] = *p.ProxyName
	if err = tx.SaveProfile(profile); err != nil {
		return nil, err
	}
	return tx.RecordRuntimeIntent(p.OperationID, webRuntimeSelect, p)
}

func (rt *WebAPIRuntime) selectHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIRuntimeSelectRequest
	if err := WebAPIDecodeJSON(w, r, &p); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if receipt, found, err := rt.preflightApply(r.Context(), p.WebAPIMutation, "runtime/proxies:select", p); err != nil {
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
	if rt.backend.SelectProxy == nil || rt.backend.State == nil {
		WebAPIWriteError(w, webRuntimeUnavailable())
		return
	}
	if err := webRuntimeName(p.GroupName, false); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if p.ProxyName == nil {
		WebAPIWriteError(w, webAPIError(400, "invalid_selection", "proxyName is required"))
		return
	}
	if err := webRuntimeName(*p.ProxyName, true); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if _, err := webAPIInteger(string(p.RuntimeGeneration), false); err != nil {
		WebAPIWriteError(w, webAPIError(400, "invalid_runtime_generation", "runtimeGeneration must come from the actual proxy snapshot"))
		return
	}
	state, resolved, err := rt.selectionState(r.Context(), p.GroupName)
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	selectionContext := r.Context()
	if resolved != nil {
		selectionContext = context.WithValue(selectionContext, webRuntimeResolvedSelectionKey{}, resolved)
	}
	receipt, err := rt.store.Transaction(selectionContext, p.WebAPIMutation, "runtime/proxies:select", p, func(tx *WebAPIWriteTx) (any, error) { return rt.recordSelection(selectionContext, tx, p, state) })
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	}
	WebAPIWriteReceipt(w, receipt)
}
