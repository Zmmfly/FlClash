//go:build webpanel && !android

package core

import (
	"context"
	"encoding/json"
)

func WebAPISchedulerServiceBackend(content *WebAPIContentService, runtime *WebAPIRuntime, sourceOptions WebAPISourceFetchOptions) WebAPISchedulerBackend {
	backend := WebAPISchedulerContentBackend(content, sourceOptions)
	if runtime == nil {
		return backend
	}
	backend.RefreshGeo = func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if task.Kind != WebAPISchedulerGeo {
			return WebAPISchedulerEffect{}, webAPIError(400, "invalid_scheduler_target", "unsupported geo target")
		}
		if err := webSchedulerValidateTask(ctx, runtime.store, task); err != nil {
			return WebAPISchedulerEffect{}, err
		}
		receipt, err := runtime.BackgroundRefreshGeo(ctx, task.Mutation.ExpectedRevision, task.ID, task.Mutation.OperationID)
		if err != nil {
			return WebAPISchedulerEffect{}, err
		}
		return webSchedulerWaitRuntime(ctx, runtime, task, receipt)
	}
	backend.RefreshExternalProvider = func(ctx context.Context, task WebAPISchedulerTask) (WebAPISchedulerEffect, error) {
		if task.Kind != WebAPISchedulerExternalProvider {
			return WebAPISchedulerEffect{}, webAPIError(400, "invalid_scheduler_target", "unsupported provider target")
		}
		if err := webSchedulerValidateTask(ctx, runtime.store, task); err != nil {
			return WebAPISchedulerEffect{}, err
		}
		found := false
		revision, err := runtime.store.View(ctx, func(tx *WebAPIReadTx) error {
			settings, err := tx.Settings()
			if err != nil {
				return err
			}
			if settings.CurrentProfileID == nil {
				return webAPIError(409, "source_target_changed", "active provider owner changed")
			}
			sources, err := runtime.RuntimeProviderSources(ctx, tx, *settings.CurrentProfileID)
			if err != nil {
				return err
			}
			for _, source := range sources {
				if string(source.ID) == task.ID && source.Purpose == task.Name {
					found = true
				}
			}
			return nil
		})
		if err != nil {
			return WebAPISchedulerEffect{}, err
		}
		if revision != task.Mutation.ExpectedRevision || !found {
			return WebAPISchedulerEffect{}, webAPIError(409, "revision_conflict", "provider inputs changed before background fetch")
		}
		receipt, err := runtime.BackgroundRefreshExternalProvider(ctx, task.Mutation.ExpectedRevision, task.ID, task.Mutation.OperationID)
		if err != nil {
			return WebAPISchedulerEffect{}, err
		}
		return webSchedulerWaitRuntime(ctx, runtime, task, receipt)
	}
	return backend
}

func webSchedulerWaitRuntime(ctx context.Context, runtime *WebAPIRuntime, task WebAPISchedulerTask, receipt WebAPIReceipt) (WebAPISchedulerEffect, error) {
	effect := WebAPISchedulerEffect{Revision: receipt.Revision, OperationID: receipt.OperationID, Phase: "unknown"}
	state, err := runtime.WaitBackgroundOperation(ctx, receipt.OperationID)
	if err != nil {
		return effect, webAPIError(409, "effect_unknown", "runtime background effect needs reconciliation")
	}
	if state.OperationID != task.Mutation.OperationID || state.DesiredRevision != receipt.Revision {
		return effect, webAPIError(409, "effect_unknown", "runtime background effect identity changed")
	}
	if state.Phase == WebAPIRuntimeFailed && state.Error != nil {
		return effect, state.Error
	}
	if state.Phase != WebAPIRuntimeApplied {
		return effect, webAPIError(409, "effect_unknown", "runtime background effect did not reach a confirmed terminal phase")
	}
	var result struct {
		Installed     bool `json:"installed"`
		Prepared      bool `json:"prepared"`
		RequiresApply bool `json:"requiresApply"`
	}
	if err = json.Unmarshal(state.Result, &result); err != nil || task.Kind == WebAPISchedulerGeo && !result.Installed || task.Kind == WebAPISchedulerExternalProvider && (!result.Prepared || !result.RequiresApply) {
		return effect, webAPIError(409, "effect_unknown", "runtime background effect has no confirmed typed result")
	}
	effect.Phase, effect.RequiresApply = "completed", result.RequiresApply
	return effect, nil
}
