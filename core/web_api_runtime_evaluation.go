//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
)

type webRuntimeAcceptedEvaluationKey struct{}

type webRuntimeEvaluationProvenance struct {
	ID           string         `json:"id"`
	Revision     WebAPIRevision `json:"revision"`
	ScriptID     WebAPIID       `json:"scriptId"`
	SourceDigest string         `json:"sourceDigest"`
	ScriptDigest string         `json:"scriptDigest"`
	ConfigSHA256 string         `json:"configSha256"`
	Label        string         `json:"label"`
	SessionID    string         `json:"sessionId"`
}

func webRuntimeEvaluationInputs(tx *WebAPIReadTx, profileID WebAPIID, proof *webRuntimeEvaluationProvenance) error {
	if proof == nil {
		return webEvaluationUnavailable()
	}
	profile, err := tx.Profile(profileID)
	if err != nil || profile.Type == "custom" || profile.ExtendType != "script" || profile.ScriptID == nil || *profile.ScriptID != proof.ScriptID || webEvaluateRealLabel(profile) != proof.Label {
		return webEvaluationUnavailable()
	}
	source, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentProfile, ID: profileID})
	if err != nil || webContentDigest(source.Bytes) != proof.SourceDigest {
		return webEvaluationUnavailable()
	}
	script, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentScript, ID: proof.ScriptID})
	if err != nil || webContentDigest(script.Bytes) != proof.ScriptDigest {
		return webEvaluationUnavailable()
	}
	return nil
}

func (rt *WebAPIRuntime) resolveEvaluationSource(ctx context.Context, tx *WebAPIReadTx, source *webRuntimeSource, id string, revision WebAPIRevision, budget *webRuntimeSnapshotBudget) error {
	if source.Profile.ExtendType != "script" || source.Profile.Type == "custom" {
		if id != "" {
			return webEvaluationUnavailable()
		}
		return nil
	}
	if id == "" {
		return webAPIError(409, "evaluation_required", "script candidates/providers require their committed evaluation snapshot")
	}
	// The resolver returns at most 32 MiB and a defensive copy. Reserve that
	// closed consumer bound before invoking it; it acquires no nested View.
	if err := budget.reserve("evaluation/"+id, WebAPIMaxContentBytes); err != nil {
		return err
	}
	evaluation, err := ResolveWebAPIEvaluation(ctx, rt.store, id, source.Profile.ID, revision)
	if err != nil {
		return err
	}
	proof := &webRuntimeEvaluationProvenance{ID: id, Revision: revision, ScriptID: evaluation.ScriptID, SourceDigest: evaluation.SourceDigest, ScriptDigest: evaluation.ScriptDigest, ConfigSHA256: evaluation.SHA256, Label: webEvaluateRealLabel(source.Profile), SessionID: WebAPIRequestSessionID(ctx)}
	if webContentDigest(evaluation.Config) != proof.ConfigSHA256 {
		return webEvaluationUnavailable()
	}
	if err = webRuntimeEvaluationInputs(tx, source.Profile.ID, proof); err != nil {
		return err
	}
	var root map[string]any
	if err = json.Unmarshal(evaluation.Config, &root); err != nil {
		return webEvaluationUnavailable()
	}
	plain, err := webRuntimeMarshalYAML(root)
	if err != nil {
		return err
	}
	if _, err = webRuntimeDocument(plain); err != nil {
		return err
	}
	source.Plain, source.Evaluation = plain, proof
	return nil
}

func (rt *WebAPIRuntime) evaluatedProviderDescriptors(ctx context.Context, tx *WebAPIReadTx, profileID WebAPIID, evaluationID string) ([]webRuntimeProviderDescriptor, error) {
	source, err := webRuntimeProfileSource(tx, profileID)
	if err != nil {
		return nil, err
	}
	meta, err := tx.Meta()
	if err != nil {
		return nil, err
	}
	if err = rt.resolveEvaluationSource(ctx, tx, &source, evaluationID, meta.Revision, nil); err != nil {
		return nil, err
	}
	return webRuntimeProviderDescriptorsFromSource(source, rt.Events.storeID)
}

func (rt *WebAPIRuntime) appliedEvaluatedProviderTx(tx *WebAPIReadTx, known webRuntimeProviderDescriptor) (webRuntimeProviderDescriptor, bool) {
	if known.Evaluation == nil || rt.effect.needsReconcile.Load() || rt.effect.transition.Load() {
		return webRuntimeProviderDescriptor{}, false
	}
	if pending, err := tx.runtimeTransitionPending(); err != nil || pending {
		return webRuntimeProviderDescriptor{}, false
	}
	rt.applied.mu.RLock()
	op, available := rt.applied.operationID, rt.applied.known && rt.applied.actualConfig == "candidate"
	rt.applied.mu.RUnlock()
	if !available {
		return webRuntimeProviderDescriptor{}, false
	}
	record, err := tx.runtimeRecord(op)
	if err != nil || record.State.Phase != WebAPIRuntimeApplied {
		return webRuntimeProviderDescriptor{}, false
	}
	var desired webRuntimeDesiredApply
	if json.Unmarshal(record.Desired, &desired) != nil {
		return webRuntimeProviderDescriptor{}, false
	}
	for _, source := range desired.ExternalProviders {
		proof := source.Evaluation
		if source.Binding.Kind == "external-provider" && source.Binding.ID == string(known.Source.ID) && source.Binding.SourceVersion == known.Source.SourceVersion && proof != nil && proof.ConfigSHA256 == known.Evaluation.ConfigSHA256 && proof.SourceDigest == known.Evaluation.SourceDigest && proof.ScriptDigest == known.Evaluation.ScriptDigest && webRuntimeEvaluationInputs(tx, known.Source.ProfileID, proof) == nil {
			// An installed resource is now anchored in the confirmed apply journal,
			// not an expired preview cache. This never authorizes an old candidate.
			known.Evaluation = proof
			return known, true
		}
	}
	return webRuntimeProviderDescriptor{}, false
}

func (rt *WebAPIRuntime) lookupEvaluatedProviderTx(ctx context.Context, tx *WebAPIReadTx, known webRuntimeProviderDescriptor) (webRuntimeProviderDescriptor, error) {
	if applied, ok := rt.appliedEvaluatedProviderTx(tx, known); ok {
		return applied, nil
	}
	proof := known.Evaluation
	if proof == nil || proof.SessionID != WebAPIRequestSessionID(ctx) {
		return webRuntimeProviderDescriptor{}, webEvaluationUnavailable()
	}
	if err := webRuntimeEvaluationInputs(tx, known.Source.ProfileID, proof); err != nil {
		return webRuntimeProviderDescriptor{}, err
	}
	meta, err := tx.Meta()
	if err != nil {
		return webRuntimeProviderDescriptor{}, err
	}
	if operationID, _ := ctx.Value(webRuntimeAcceptedEvaluationKey{}).(string); operationID != "" {
		// This exception is only the already accepted fetch, not a new command
		// using an old evaluation ID. Its one owned revision bump must be the
		// exact current revision; any other commit requires a new actual read.
		cached, err := tx.runtimeProvider(known.Source.ID)
		if err != nil {
			return webRuntimeProviderDescriptor{}, err
		}
		record, err := tx.runtimeRecord(operationID)
		if err != nil {
			return webRuntimeProviderDescriptor{}, err
		}
		var acceptedRevision WebAPIRevision
		var acceptedDescriptor webRuntimeProviderDescriptor
		switch record.State.Action {
		case webRuntimeRefreshProvider:
			var desired webRuntimeDesiredProviderRefresh
			if json.Unmarshal(record.Desired, &desired) != nil {
				return webRuntimeProviderDescriptor{}, webEvaluationUnavailable()
			}
			acceptedRevision, acceptedDescriptor = desired.Request.ExpectedRevision, desired.Descriptor
		case webRuntimeWriteProvider, webRuntimeSideloadProvider:
			var desired webRuntimeDesiredProviderWrite
			if json.Unmarshal(record.Desired, &desired) != nil {
				return webRuntimeProviderDescriptor{}, webEvaluationUnavailable()
			}
			acceptedRevision, acceptedDescriptor = desired.Request.ExpectedRevision, desired.Descriptor
		default:
			return webRuntimeProviderDescriptor{}, webEvaluationUnavailable()
		}
		if cached.OperationID != operationID || cached.Phase != "pending" || record.State.DesiredRevision != meta.Revision || acceptedRevision != proof.Revision || acceptedDescriptor.Source.ID != known.Source.ID || acceptedDescriptor.Evaluation == nil || acceptedDescriptor.Evaluation.ID != proof.ID {
			return webRuntimeProviderDescriptor{}, webEvaluationUnavailable()
		}
		return known, nil
	}
	current, err := rt.evaluatedProviderDescriptors(ctx, tx, known.Source.ProfileID, proof.ID)
	if err != nil {
		return webRuntimeProviderDescriptor{}, err
	}
	for _, descriptor := range current {
		if descriptor.Source.ID == known.Source.ID {
			return descriptor, nil
		}
	}
	return webRuntimeProviderDescriptor{}, webAPIError(409, "source_target_changed", "provider is not part of the frozen evaluated configuration")
}
