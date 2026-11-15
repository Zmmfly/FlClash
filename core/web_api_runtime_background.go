//go:build webpanel && !android

package core

import (
	"context"
)

func webRuntimeRestoreAuthorization(ctx context.Context, original context.Context) context.Context {
	if browser, ok := original.Value(webAPIRequestAuthorizationKey{}).(webAPIRequestAuthorization); ok {
		return context.WithValue(ctx, webAPIRequestAuthorizationKey{}, browser)
	}
	if internal, ok := original.Value(webAPIInternalAuthorizationKey{}).(webAPIInternalAuthorization); ok {
		return context.WithValue(ctx, webAPIInternalAuthorizationKey{}, internal)
	}
	return ctx
}

func (rt *WebAPIRuntime) RuntimeProviderSources(ctx context.Context, tx *WebAPIReadTx, profileID WebAPIID) ([]WebAPIRuntimeProviderSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	descriptors, err := webRuntimeProviderDescriptors(tx, profileID, rt.Events.storeID)
	if err != nil {
		return nil, err
	}
	out := make([]WebAPIRuntimeProviderSource, 0, len(descriptors))
	for _, descriptor := range descriptors {
		rt.rememberProvider(descriptor)
		out = append(out, descriptor.Source)
	}
	return out, nil
}

func (rt *WebAPIRuntime) BackgroundRefreshGeo(ctx context.Context, originalRevision WebAPIRevision, opaqueID, operationID string) (WebAPIReceipt, error) {
	var empty WebAPIReceipt
	if !WebAPIInternalAuthorized(ctx) {
		return empty, webAPIError(403, "internal_authority_required", "background runtime refresh requires the service lifetime authority")
	}
	if rt.backend.SafeMode {
		return empty, webRuntimeSafeMode()
	}
	if rt.backend.InstallGeo == nil {
		return empty, webRuntimeUnavailable()
	}
	mutation := WebAPIMutation{ExpectedRevision: originalRevision, OperationID: operationID}
	request := struct {
		WebAPIMutation
		ID string `json:"id"`
	}{mutation, opaqueID}
	const command = "runtime/background:geo-refresh"
	if receipt, found, err := rt.preflightApply(ctx, mutation, command, request); err != nil {
		return empty, err
	} else if found {
		return receipt, nil
	}
	_, existed := rt.providers.sessions.LoadOrStore(operationID, context.WithoutCancel(ctx))
	receipt, err := rt.store.Transaction(ctx, mutation, command, request, func(tx *WebAPIWriteTx) (any, error) {
		descriptor, err := webRuntimeGeoByID(tx.WebAPIReadTx, rt.Events.storeID, WebAPIRuntimeProviderID(opaqueID))
		if err != nil {
			return nil, err
		}
		p := WebAPIRuntimeGeoRequest{WebAPIMutation: mutation, Type: descriptor.Type, SourceID: descriptor.ID}
		if _, err = tx.tx.CreateBucketIfNotExists([]byte(webRuntimeGeoBucket)); err != nil {
			return nil, err
		}
		if err = webAPIPut(tx.tx, webRuntimeGeoBucket, string(descriptor.ID), descriptor); err != nil {
			return nil, err
		}
		epoch := tx.SchedulerEpoch()
		return tx.RecordRuntimeIntent(operationID, webRuntimeGeoUpdate, webRuntimeDesiredGeo{Request: p, Descriptor: descriptor, SchedulerEpoch: &epoch})
	})
	if err != nil {
		if !existed {
			rt.providers.sessions.Delete(operationID)
		}
		return empty, err
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	} else if !existed {
		rt.providers.sessions.Delete(operationID)
	}
	return receipt, nil
}

func (rt *WebAPIRuntime) BackgroundRefreshExternalProvider(ctx context.Context, originalRevision WebAPIRevision, opaqueID, operationID string) (WebAPIReceipt, error) {
	var empty WebAPIReceipt
	if !WebAPIInternalAuthorized(ctx) {
		return empty, webAPIError(403, "internal_authority_required", "background runtime refresh requires the service lifetime authority")
	}
	if rt.backend.SafeMode {
		return empty, webRuntimeSafeMode()
	}
	mutation := WebAPIMutation{ExpectedRevision: originalRevision, OperationID: operationID}
	request := struct {
		WebAPIMutation
		ID string `json:"id"`
	}{mutation, opaqueID}
	const command = "runtime/background:provider-refresh"
	if receipt, found, err := rt.preflightApply(ctx, mutation, command, request); err != nil {
		return empty, err
	} else if found {
		return receipt, nil
	}
	descriptor, err := rt.lookupProvider(ctx, WebAPIRuntimeProviderID(opaqueID))
	if err != nil {
		return empty, err
	}
	_, existed := rt.providers.sessions.LoadOrStore(operationID, context.WithoutCancel(ctx))
	receipt, err := rt.store.Transaction(ctx, mutation, command, request, func(tx *WebAPIWriteTx) (any, error) {
		current, err := rt.lookupProviderTx(ctx, tx.WebAPIReadTx, descriptor.Source.ID)
		if err != nil {
			return nil, err
		}
		p := WebAPIRuntimeProviderRefresh{WebAPIMutation: mutation, ID: current.Source.ID, Purpose: current.Source.Purpose}
		prior, _ := tx.runtimeProvider(current.Source.ID)
		prior.Descriptor, prior.Phase, prior.OperationID = current, "pending", operationID
		if err := tx.saveRuntimeProvider(prior); err != nil {
			return nil, err
		}
		epoch := tx.SchedulerEpoch()
		return tx.RecordRuntimeIntent(operationID, webRuntimeRefreshProvider, webRuntimeDesiredProviderRefresh{Request: p, Descriptor: current, SchedulerEpoch: &epoch})
	})
	if err != nil {
		if !existed {
			rt.providers.sessions.Delete(operationID)
		}
		return empty, err
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	} else if !existed {
		rt.providers.sessions.Delete(operationID)
	}
	return receipt, nil
}

func webRuntimeCheckBackgroundEpoch(tx *WebAPIReadTx, expected *string) error {
	if expected != nil && tx.SchedulerEpoch() != *expected {
		return webAPIError(409, "background_scope_changed", "background runtime authority was detached by reset or restore")
	}
	return nil
}

func (rt *WebAPIRuntime) phaseNotification() <-chan struct{} {
	rt.phaseMu.Lock()
	defer rt.phaseMu.Unlock()
	return rt.phaseChanged
}
func (rt *WebAPIRuntime) notifyPhaseChange() {
	rt.phaseMu.Lock()
	defer rt.phaseMu.Unlock()
	if rt.phaseChanged != nil {
		close(rt.phaseChanged)
	}
	rt.phaseChanged = make(chan struct{})
}
func (rt *WebAPIRuntime) publishRuntimeState(state WebAPIRuntimeOperation) {
	rt.notifyPhaseChange()
	rt.Events.PublishRuntime(state)
}

func (rt *WebAPIRuntime) WaitBackgroundOperation(ctx context.Context, operationID string) (WebAPIRuntimeOperation, error) {
	var result WebAPIRuntimeOperation
	if !WebAPIInternalAuthorized(ctx) {
		return result, webAPIError(403, "internal_authority_required", "runtime background status requires the service authority")
	}
	for {
		changed := rt.phaseNotification()
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return result, err
		}
		_, err = rt.store.View(ctx, func(tx *WebAPIReadTx) error {
			var err error
			result, err = tx.RuntimeOperation(operationID)
			return err
		})
		release()
		if err != nil {
			return result, err
		}
		if result.Phase != WebAPIRuntimePending && result.Phase != WebAPIRuntimeApplying {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-rt.ctx.Done():
			return result, webAPIError(503, "runtime_closed", "runtime is closed")
		case <-changed:
		}
	}
}
