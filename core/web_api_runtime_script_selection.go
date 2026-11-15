//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
)

type webRuntimeResolvedSelectionKey struct{}
type webRuntimeResolvedSelection struct {
	operationID string
	generation  WebAPIRevision
	profileID   WebAPIID
	members     []string
}

func (rt *WebAPIRuntime) selectionState(ctx context.Context, group string) (WebAPIRuntimeCoreState, *webRuntimeResolvedSelection, error) {
	var script bool
	_, err := rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.CurrentProfileID == nil {
			return nil
		}
		profile, err := tx.Profile(*settings.CurrentProfileID)
		if err == nil {
			script = profile.Type != "custom" && profile.ExtendType == "script"
		}
		return err
	})
	if err != nil {
		return WebAPIRuntimeCoreState{}, nil, err
	}
	if !script {
		state, err := rt.backend.State(ctx)
		return state, nil, err
	}
	if !rt.backendMu.TryRLock() {
		return WebAPIRuntimeCoreState{}, nil, webAPIError(409, "runtime_transition_pending", "runtime state is changing")
	}
	defer rt.backendMu.RUnlock()
	state, err := rt.backend.State(ctx)
	if err != nil {
		return state, nil, err
	}
	if state.ProfileID == nil || rt.backend.Proxies == nil {
		return state, nil, webRuntimeUnavailable()
	}
	snapshot, err := rt.backend.Proxies(ctx)
	if err != nil {
		return state, nil, err
	}
	value, ok := snapshot.Proxies[group]
	if !ok {
		return state, nil, webAPIError(409, "invalid_selection", "group is not in the applied runtime")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return state, nil, err
	}
	var resolved struct {
		Type string    `json:"type"`
		All  *[]string `json:"all"`
	}
	if json.Unmarshal(data, &resolved) != nil || resolved.All == nil || !webAPIContains([]string{"Selector", "URLTest", "Fallback"}, resolved.Type) {
		return state, nil, webAPIError(409, "catalog_unavailable", "applied selectable group has no complete resolved member snapshot")
	}
	for _, member := range *resolved.All {
		if err := webRuntimeName(member, false); err != nil {
			return state, nil, err
		}
	}
	rt.applied.mu.RLock()
	op, generation, known := rt.applied.operationID, rt.applied.generation, rt.applied.known && rt.applied.actualConfig == "candidate"
	rt.applied.mu.RUnlock()
	if !known || generation == "" || generation != state.RuntimeGeneration {
		return state, nil, webAPIError(409, "runtime_generation_conflict", "applied generation has not been confirmed")
	}
	return state, &webRuntimeResolvedSelection{operationID: op, generation: generation, profileID: *state.ProfileID, members: append([]string{}, (*resolved.All)...)}, nil
}

func (rt *WebAPIRuntime) scriptSelectionMembers(ctx context.Context, tx *WebAPIReadTx, profile WebAPIProfile, generation WebAPIRevision) ([]string, error) {
	if err := tx.RequireProfileCatalogAvailable(profile.ID); err != nil {
		return nil, err
	}
	resolved, _ := ctx.Value(webRuntimeResolvedSelectionKey{}).(*webRuntimeResolvedSelection)
	if resolved == nil || resolved.profileID != profile.ID || resolved.generation != generation {
		return nil, webAPIError(409, "runtime_generation_conflict", "selection snapshot does not match the requested profile and generation")
	}
	rt.applied.mu.RLock()
	same := rt.applied.known && rt.applied.actualConfig == "candidate" && rt.applied.operationID == resolved.operationID && rt.applied.generation == resolved.generation
	rt.applied.mu.RUnlock()
	if !same {
		return nil, webAPIError(409, "runtime_generation_conflict", "applied provenance changed before selection acceptance")
	}
	record, err := tx.runtimeRecord(resolved.operationID)
	if err != nil {
		return nil, err
	}
	var desired webRuntimeDesiredApply
	if record.State.Phase != WebAPIRuntimeApplied || json.Unmarshal(record.Desired, &desired) != nil || desired.Request.ProfileID == nil || *desired.Request.ProfileID != profile.ID || desired.Evaluation == nil {
		return nil, webAPIError(409, "applied_state_unavailable", "script selection requires a confirmed evaluated apply")
	}
	if err := webRuntimeEvaluationInputs(tx, profile.ID, desired.Evaluation); err != nil {
		return nil, err
	}
	return resolved.members, nil
}
